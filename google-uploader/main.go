package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/searleser97/media_workflow_tools/internal/tracker"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

const (
	stateFilePrefix = "upload_state_"
	configDir       = ".google_uploader"
	maxRetries      = 3
	retryDelay      = 5 * time.Second
	photosUploadURL = "https://photoslibrary.googleapis.com/v1/uploads"
	photosAPIURL    = "https://photoslibrary.googleapis.com/v1"
	photosScope     = "https://www.googleapis.com/auth/photoslibrary.appendonly"
)

var (
	session     = flag.String("session", "", "Session name (required) — state is saved to upload_state_<name>.json")
	collection  = flag.String("collection", "", "Single album/playlist name for all media (instead of per-folder)")
	consolidate = flag.Bool("consolidate", false, "Move already-uploaded items into the collection (requires -collection)")
)

type FolderState struct {
	FolderName      string            `json:"folder_name"`
	FolderPath      string            `json:"folder_path"`
	PlaylistID      string            `json:"playlist_id"`
	AlbumID         string            `json:"album_id"`
	UploadedVideos  map[string]string `json:"uploaded_videos"`  // filename -> videoID
	UploadedPhotos  map[string]string `json:"uploaded_photos"`  // filename -> mediaItemID
	FailedFiles     map[string]string `json:"failed_files"`     // filename -> error message
	LastProcessed   string            `json:"last_processed"`
	TotalVideos     int               `json:"total_videos"`
	CompletedVideos int               `json:"completed_videos"`
	TotalPhotos     int               `json:"total_photos"`
	CompletedPhotos int               `json:"completed_photos"`
	PhotosDone      bool              `json:"photos_done"`
	VideosDone      bool              `json:"videos_done"`
	Completed       bool              `json:"completed"`
}

type MultiUploadState struct {
	Folders              map[string]*FolderState `json:"folders"`                         // folder abs path -> state
	CollectionName       string                  `json:"collection_name,omitempty"`       // single collection name
	CollectionAlbumID    string                  `json:"collection_album_id,omitempty"`   // single shared album ID
	CollectionPlaylistID string                  `json:"collection_playlist_id,omitempty"` // single shared playlist ID
	ConsolidatedPhotos   map[string]bool         `json:"consolidated_photos,omitempty"`   // media item IDs already added to collection album
	ConsolidatedVideos   map[string]bool         `json:"consolidated_videos,omitempty"`   // video IDs already added to collection playlist
	InvalidPhotoIDs      map[string]bool         `json:"invalid_photo_ids,omitempty"`     // media item IDs that failed consolidation
}

func main() {
	flag.Parse()
	folders := flag.Args()

	if *session == "" {
		log.Fatal("Error: -session flag is required\nUsage: google_uploader -session <name> [-collection <name>] [-consolidate] /path/to/folder1 [/path/to/folder2 ...]")
	}

	stateFile := stateFilePrefix + *session + ".json"

	// Always load existing state for this session
	state := loadState(stateFile)

	// Save state on Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n⚠ Interrupted — saving state...")
		saveState(state, stateFile)
		fmt.Printf("State saved to %s\n", stateFile)
		os.Exit(1)
	}()

	// Resolve collection name: CLI flag takes priority, then saved state
	useCollection := *collection
	if useCollection == "" && state.CollectionName != "" {
		useCollection = state.CollectionName
		fmt.Printf("Using saved collection: %s\n", useCollection)
	}
	if useCollection != "" {
		state.CollectionName = useCollection
	}

	if *consolidate && useCollection == "" {
		log.Fatal("Error: -consolidate requires -collection (or a session with a saved collection)\nUsage: google_uploader -session <name> -collection <name> -consolidate")
	}

	// Consolidate mode: move already-uploaded items into a single collection
	if *consolidate {
		ctx := context.Background()
		config, err := getOAuthConfig()
		if err != nil {
			log.Fatalf("Error loading credentials: %v", err)
		}
		httpClient := getClient(ctx, config)
		runConsolidate(state, httpClient, stateFile)
		return
	}

	// Merge arg folders into state
	for _, folder := range folders {
		absPath, err := filepath.Abs(folder)
		if err != nil {
			log.Fatalf("Error resolving path %s: %v", folder, err)
		}
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			log.Fatalf("Error: Folder does not exist: %s", absPath)
		}
		if _, exists := state.Folders[absPath]; !exists {
			state.Folders[absPath] = &FolderState{
				FolderName:     filepath.Base(absPath),
				FolderPath:     absPath,
				UploadedVideos: make(map[string]string),
				UploadedPhotos: make(map[string]string),
				FailedFiles:    make(map[string]string),
			}
		}
	}

	if len(state.Folders) == 0 {
		log.Fatal("Error: no folders in session — provide at least one folder path\nUsage: google_uploader -session <name> [-collection <name>] /path/to/folder1 [/path/to/folder2 ...]")
	}

	// Determine the common parent for the top-level tracker
	trackerRoot := ""
	for path := range state.Folders {
		parent := filepath.Dir(path)
		if trackerRoot == "" {
			trackerRoot = parent
		} else if trackerRoot != parent {
			trackerRoot = ""
			break
		}
	}

	// Load top-level tracker (if all folders share a parent)
	var topTracker *tracker.TopLevelTracker
	if trackerRoot != "" {
		topTracker = tracker.LoadTopLevel(trackerRoot)
	}

	// Migrate session state uploads into per-folder trackers
	for absPath, fs := range state.Folders {
		ft := tracker.LoadFolder(absPath)
		migrated := false
		if ft.MergePhotos(fs.UploadedPhotos) > 0 {
			migrated = true
		}
		if ft.MergeVideos(fs.UploadedVideos) > 0 {
			migrated = true
		}
		if migrated {
			tracker.SaveFolder(absPath, ft)
		}
	}

	// Collect folders still needing work
	var folderPaths []string
	for path, fs := range state.Folders {
		// Clear failed files so they get retried
		if len(fs.FailedFiles) > 0 {
			fs.FailedFiles = make(map[string]string)
			fs.Completed = false
			fs.PhotosDone = false
			fs.VideosDone = false
		}

		// Check top-level tracker for completion
		folderName := filepath.Base(path)
		if topTracker != nil && topTracker.IsCompleted(folderName) && fs.Completed {
			continue
		}

		// If top-level says incomplete but session says complete, reopen it
		if fs.Completed {
			fs.Completed = false
			fs.PhotosDone = false
			fs.VideosDone = false
		}

		folderPaths = append(folderPaths, path)
	}
	sort.Strings(folderPaths)

	if len(folderPaths) == 0 {
		fmt.Println("All folders have been fully uploaded.")
		return
	}

	ctx := context.Background()

	config, err := getOAuthConfig()
	if err != nil {
		log.Fatalf("Error loading credentials: %v", err)
	}
	httpClient := getClient(ctx, config)

	fmt.Printf("\n=== Google Uploader ===\n")
	fmt.Printf("Processing %d folder(s)\n", len(folderPaths))

	totalPhotosUploaded := 0
	totalVideosUploaded := 0

	// Pre-scan all folders for total counts (using per-folder trackers)
	overallTotalPhotos := 0
	overallTotalVideos := 0
	overallCompletedPhotos := 0
	overallCompletedVideos := 0
	folderTrackers := make(map[string]*tracker.FolderTracker)
	for _, folderPath := range folderPaths {
		ft := tracker.LoadFolder(folderPath)
		folderTrackers[folderPath] = ft
		photos, _ := getImageFiles(folderPath)
		videos, _ := getVideoFiles(folderPath)
		overallTotalPhotos += len(photos)
		overallTotalVideos += len(videos)
		overallCompletedPhotos += len(ft.UploadedPhotos)
		overallCompletedVideos += len(ft.UploadedVideos)
	}

	// ── Global Pass 1: Photos across all folders ──
	fmt.Println("\n📷 Pass 1: Uploading photos to Google Photos...")
	fmt.Printf("  Overall: %d/%d photos completed\n", overallCompletedPhotos, overallTotalPhotos)

	// In collection mode, create or reuse the single album
	if useCollection != "" && state.CollectionAlbumID == "" {
		albumID, err := createPhotosAlbum(httpClient, state.CollectionName)
		if err != nil {
			log.Printf("ERROR: Failed to create collection album: %v\n", err)
		} else {
			state.CollectionAlbumID = albumID
			fmt.Printf("  Created collection album: %s (ID: %s)\n", state.CollectionName, albumID)
			saveState(state, stateFile)
		}
	} else if useCollection != "" {
		fmt.Printf("  Using existing collection album ID: %s\n", state.CollectionAlbumID)
	}

	for folderIdx, folderPath := range folderPaths {
		fs := state.Folders[folderPath]
		ft := folderTrackers[folderPath]

		photos, err := getImageFiles(folderPath)
		if err != nil {
			log.Printf("ERROR: Failed to read image files from %s: %v\n", folderPath, err)
			continue
		}

		if len(photos) == 0 {
			fmt.Printf("\n[Folder %d/%d] %s — no photos found, skipping\n", folderIdx+1, len(folderPaths), fs.FolderName)
			continue
		}

		fs.TotalPhotos = len(photos)
		completedCount := 0
		for _, p := range photos {
			if ft.HasPhoto(filepath.Base(p)) {
				completedCount++
			}
		}
		fmt.Printf("\n[Folder %d/%d] %s — %d photo(s)\n", folderIdx+1, len(folderPaths), fs.FolderName, len(photos))

		if completedCount > 0 {
			fmt.Printf("  Resuming: %d/%d photos already uploaded\n", completedCount, fs.TotalPhotos)
		}

		// Check if all photos are already uploaded via per-folder tracker
		allPhotosDone := completedCount == len(photos)
		if allPhotosDone {
			fs.PhotosDone = true
			fmt.Printf("  ✓ Photos already complete for %s\n", fs.FolderName)
			continue
		}

		// Determine which album to use
		albumID := ""
		if useCollection != "" {
			albumID = state.CollectionAlbumID
			if albumID == "" {
				log.Printf("ERROR: No collection album available — skipping photos for %s\n", fs.FolderName)
				continue
			}
		} else {
			if fs.AlbumID == "" {
				id, err := createPhotosAlbum(httpClient, fs.FolderName)
				if err != nil {
					log.Printf("ERROR: Failed to create album for %s: %v — skipping photos for this folder\n", fs.FolderName, err)
					continue
				}
				fs.AlbumID = id
				fmt.Printf("  Created album: %s (ID: %s)\n", fs.FolderName, id)
				saveState(state, stateFile)
			} else {
				fmt.Printf("  Using existing album ID: %s\n", fs.AlbumID)
			}
			albumID = fs.AlbumID
		}

		fmt.Println("  " + strings.Repeat("-", 46))

		for i, photoPath := range photos {
			filename := filepath.Base(photoPath)

			if itemID, exists := ft.UploadedPhotos[filename]; exists {
				fmt.Printf("  [%d/%d] ✓ Skipping (already uploaded): %s (ID: %s)\n", i+1, len(photos), filename, itemID)
				continue
			}

			if _, failed := fs.FailedFiles[filename]; failed {
				fmt.Printf("  [%d/%d] ✗ Skipping (previously failed): %s\n", i+1, len(photos), filename)
				continue
			}

			fmt.Printf("  [%d/%d] Uploading: %s\n", i+1, len(photos), filename)

			itemID, err := uploadPhotoWithRetry(httpClient, photoPath, albumID, maxRetries)
			if err != nil {
				log.Printf("  ✗ Failed to upload %s after %d attempts: %v — skipping\n", filename, maxRetries, err)
				fs.FailedFiles[filename] = err.Error()
				saveState(state, stateFile)
				continue
			}

			fmt.Printf("    ✓ Photo uploaded successfully! ID: %s\n", itemID)

			// Write to both per-folder tracker and session state
			ft.UploadedPhotos[filename] = itemID
			fs.UploadedPhotos[filename] = itemID
			fs.LastProcessed = filename
			fs.CompletedPhotos++
			totalPhotosUploaded++
			overallCompletedPhotos++
			tracker.SaveFolder(folderPath, ft)
			saveState(state, stateFile)

			fmt.Printf("    Progress: %d/%d photos (folder) | %d/%d photos (overall)\n", fs.CompletedPhotos, fs.TotalPhotos, overallCompletedPhotos, overallTotalPhotos)
		}

		fs.PhotosDone = true
		saveState(state, stateFile)
		fmt.Printf("  ✓ Photos complete for %s\n", fs.FolderName)
	}

	// ── Global Pass 2: Videos across all folders ──
	fmt.Println("\n🎬 Pass 2: Uploading videos to YouTube...")
	fmt.Printf("  Overall: %d/%d videos completed\n", overallCompletedVideos, overallTotalVideos)

	// Determine if YouTube service is needed
	needsYouTube := false
	for _, folderPath := range folderPaths {
		ft := folderTrackers[folderPath]
		videos, _ := getVideoFiles(folderPath)
		allVideosDone := true
		for _, v := range videos {
			if !ft.HasVideo(filepath.Base(v)) {
				allVideosDone = false
				break
			}
		}
		if !allVideosDone && len(videos) > 0 {
			needsYouTube = true
			break
		}
	}

	var ytService *youtube.Service
	if needsYouTube {
		ytService, err = getYouTubeService(ctx, httpClient)
		if err != nil {
			log.Fatalf("Error creating YouTube service: %v", err)
		}

		// In collection mode, create or reuse the single playlist
		if useCollection != "" && state.CollectionPlaylistID == "" {
			playlistID, err := createPlaylist(ytService, state.CollectionName)
			if err != nil {
				log.Printf("ERROR: Failed to create collection playlist: %v\n", err)
			} else {
				state.CollectionPlaylistID = playlistID
				fmt.Printf("  Created collection playlist: %s (ID: %s)\n", state.CollectionName, playlistID)
				saveState(state, stateFile)
			}
		} else if useCollection != "" {
			fmt.Printf("  Using existing collection playlist ID: %s\n", state.CollectionPlaylistID)
		}
	}

	for folderIdx, folderPath := range folderPaths {
		fs := state.Folders[folderPath]
		ft := folderTrackers[folderPath]

		videos, err := getVideoFiles(folderPath)
		if err != nil {
			log.Printf("ERROR: Failed to read video files from %s: %v\n", folderPath, err)
			continue
		}

		if len(videos) == 0 {
			fmt.Printf("\n[Folder %d/%d] %s — no videos found, skipping\n", folderIdx+1, len(folderPaths), fs.FolderName)
			continue
		}

		fs.TotalVideos = len(videos)
		completedCount := 0
		for _, v := range videos {
			if ft.HasVideo(filepath.Base(v)) {
				completedCount++
			}
		}
		fmt.Printf("\n[Folder %d/%d] %s — %d video(s)\n", folderIdx+1, len(folderPaths), fs.FolderName, len(videos))

		if completedCount > 0 {
			fmt.Printf("  Resuming: %d/%d videos already uploaded\n", completedCount, fs.TotalVideos)
		}

		allVideosDone := completedCount == len(videos)
		if allVideosDone {
			fs.VideosDone = true
			fmt.Printf("  ✓ Videos already complete for %s\n", fs.FolderName)
			continue
		}

		// Determine which playlist to use
		playlistID := ""
		if useCollection != "" {
			playlistID = state.CollectionPlaylistID
			if playlistID == "" {
				log.Printf("ERROR: No collection playlist available — skipping videos for %s\n", fs.FolderName)
				continue
			}
		} else {
			if fs.PlaylistID == "" {
				id, err := createPlaylist(ytService, fs.FolderName)
				if err != nil {
					log.Printf("ERROR: Failed to create playlist for %s: %v — skipping videos for this folder\n", fs.FolderName, err)
					continue
				}
				fs.PlaylistID = id
				fmt.Printf("  Created playlist: %s (ID: %s)\n", fs.FolderName, id)
				saveState(state, stateFile)
			} else {
				fmt.Printf("  Using existing playlist ID: %s\n", fs.PlaylistID)
			}
			playlistID = fs.PlaylistID
		}

		fmt.Println("  " + strings.Repeat("-", 46))

		for i, videoPath := range videos {
			filename := filepath.Base(videoPath)

			if videoID, exists := ft.UploadedVideos[filename]; exists {
				fmt.Printf("  [%d/%d] ✓ Skipping (already uploaded): %s (ID: %s)\n", i+1, len(videos), filename, videoID)
				continue
			}

			if _, failed := fs.FailedFiles[filename]; failed {
				fmt.Printf("  [%d/%d] ✗ Skipping (previously failed): %s\n", i+1, len(videos), filename)
				continue
			}

			fmt.Printf("  [%d/%d] Uploading: %s\n", i+1, len(videos), filename)

			videoID, err := uploadVideoWithRetry(ytService, videoPath, maxRetries)
			if err != nil {
				log.Printf("  ✗ Failed to upload %s after %d attempts: %v — skipping\n", filename, maxRetries, err)
				fs.FailedFiles[filename] = err.Error()
				saveState(state, stateFile)
				continue
			}

			fmt.Printf("    ✓ Video uploaded successfully! ID: %s\n", videoID)

			err = addToPlaylistWithRetry(ytService, playlistID, videoID, maxRetries)
			if err != nil {
				log.Printf("WARNING: Failed to add video %s to playlist: %v\n", videoID, err)
			} else {
				fmt.Printf("    ✓ Added to playlist\n")
			}

			// Write to both per-folder tracker and session state
			ft.UploadedVideos[filename] = videoID
			fs.UploadedVideos[filename] = videoID
			fs.LastProcessed = filename
			fs.CompletedVideos++
			totalVideosUploaded++
			overallCompletedVideos++
			tracker.SaveFolder(folderPath, ft)
			saveState(state, stateFile)

			fmt.Printf("    Progress: %d/%d videos (folder) | %d/%d videos (overall)\n", fs.CompletedVideos, fs.TotalVideos, overallCompletedVideos, overallTotalVideos)
		}

		fs.VideosDone = true
		saveState(state, stateFile)
		fmt.Printf("  ✓ Videos complete for %s\n", fs.FolderName)
	}

	// ── Mark folders as completed and update top-level tracker ──
	for _, folderPath := range folderPaths {
		fs := state.Folders[folderPath]
		ft := folderTrackers[folderPath]

		if !fs.PhotosDone || !fs.VideosDone {
			continue
		}

		// Only mark complete if all current files are in the tracker and no failures
		photos, _ := getImageFiles(folderPath)
		videos, _ := getVideoFiles(folderPath)
		if len(fs.FailedFiles) == 0 && ft.IsFullyUploaded(photos, videos) {
			fs.Completed = true
			if topTracker != nil {
				topTracker.MarkComplete(filepath.Base(folderPath))
			}
		}
	}
	saveState(state, stateFile)
	if topTracker != nil && trackerRoot != "" {
		tracker.SaveTopLevel(trackerRoot, topTracker)
	}

	fmt.Println("\n" + strings.Repeat("=", 50))

	// Collect total failures
	totalFailed := 0
	for _, folderPath := range folderPaths {
		totalFailed += len(state.Folders[folderPath].FailedFiles)
	}

	if totalFailed > 0 {
		fmt.Printf("⚠ Uploads completed with %d failed file(s)\n", totalFailed)
	} else {
		fmt.Printf("✓ All uploads completed successfully!\n")
	}
	if totalPhotosUploaded > 0 {
		fmt.Printf("Total photos uploaded: %d\n", totalPhotosUploaded)
	}
	if totalVideosUploaded > 0 {
		fmt.Printf("Total videos uploaded: %d\n", totalVideosUploaded)
	}
	if useCollection != "" {
		if state.CollectionAlbumID != "" {
			fmt.Printf("  Collection album: %s\n", state.CollectionAlbumID)
		}
		if state.CollectionPlaylistID != "" {
			fmt.Printf("  Collection playlist: https://www.youtube.com/playlist?list=%s\n", state.CollectionPlaylistID)
		}
	} else {
		for _, folderPath := range folderPaths {
			fs := state.Folders[folderPath]
			parts := []string{fs.FolderName + ":"}
			if fs.CompletedPhotos > 0 {
				parts = append(parts, fmt.Sprintf("%d photos (album: %s)", fs.CompletedPhotos, fs.AlbumID))
			}
			if fs.CompletedVideos > 0 {
				parts = append(parts, fmt.Sprintf("%d videos (playlist: https://www.youtube.com/playlist?list=%s)", fs.CompletedVideos, fs.PlaylistID))
			}
			if len(fs.FailedFiles) > 0 {
				parts = append(parts, fmt.Sprintf("%d failed", len(fs.FailedFiles)))
			}
			fmt.Printf("  %s\n", strings.Join(parts, " | "))
		}
	}

	if totalFailed > 0 {
		fmt.Printf("\nState saved to %s — re-run with same -session to retry failed files\n", stateFile)
	} else {
		fmt.Printf("\nState saved to %s\n", stateFile)
	}
}

func configDirPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Unable to determine home directory: %v", err)
	}
	return filepath.Join(home, configDir)
}

func credFilePath() string {
	return filepath.Join(configDirPath(), "client_secret.json")
}

func tokenFilePath() string {
	return filepath.Join(configDirPath(), "token.json")
}

func getOAuthConfig() (*oauth2.Config, error) {
	credFile := credFilePath()
	credentials, err := os.ReadFile(credFile)
	if err != nil {
		return nil, fmt.Errorf("unable to read client secret file: %v\nPlease download your OAuth2 credentials from Google Cloud Console and save as '%s'", err, credFile)
	}

	config, err := google.ConfigFromJSON(credentials,
		youtube.YoutubeUploadScope,
		youtube.YoutubeScope,
		photosScope,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %v", err)
	}

	return config, nil
}

func getYouTubeService(ctx context.Context, httpClient *http.Client) (*youtube.Service, error) {
	service, err := youtube.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("unable to create YouTube service: %v", err)
	}
	return service, nil
}

func getClient(ctx context.Context, config *oauth2.Config) *http.Client {
	tokenFile := tokenFilePath()
	token, err := tokenFromFile(tokenFile)
	if err != nil {
		token = getTokenFromWeb(config)
		saveToken(tokenFile, token)
	}

	return config.Client(ctx, token)
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("\nAuthorization required!\n")
	fmt.Printf("Visit this URL to authorize the application:\n\n%s\n\n", authURL)
	fmt.Print("Enter the authorization code: ")

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code: %v", err)
	}

	token, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return token
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	token := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(token)
	return token, err
}

func saveToken(path string, token *oauth2.Token) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Fatalf("Unable to create config directory: %v", err)
	}
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func loadState(stateFile string) *MultiUploadState {
	state := &MultiUploadState{
		Folders: make(map[string]*FolderState),
	}

	data, err := os.ReadFile(stateFile)
	if err == nil {
		if err := json.Unmarshal(data, state); err == nil {
			for _, fs := range state.Folders {
				if fs.UploadedVideos == nil {
					fs.UploadedVideos = make(map[string]string)
				}
				if fs.UploadedPhotos == nil {
					fs.UploadedPhotos = make(map[string]string)
				}
				if fs.FailedFiles == nil {
					fs.FailedFiles = make(map[string]string)
				}
			}
			fmt.Printf("Loaded existing session from %s\n", stateFile)
			return state
		}
	}

	return state
}

func saveState(state *MultiUploadState, stateFile string) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("Warning: Failed to marshal state: %v", err)
		return
	}

	if err := os.WriteFile(stateFile, data, 0644); err != nil {
		log.Printf("Warning: Failed to save state: %v", err)
	}
}

func runConsolidate(state *MultiUploadState, httpClient *http.Client, stateFile string) {
	fmt.Printf("\n=== Consolidating into collection: %s ===\n", state.CollectionName)

	// Initialize tracking maps if needed
	if state.ConsolidatedPhotos == nil {
		state.ConsolidatedPhotos = make(map[string]bool)
	}
	if state.ConsolidatedVideos == nil {
		state.ConsolidatedVideos = make(map[string]bool)
	}
	if state.InvalidPhotoIDs == nil {
		state.InvalidPhotoIDs = make(map[string]bool)
	}

	// Collect photo and video IDs not yet consolidated
	var pendingPhotoIDs []string
	var pendingVideoIDs []string
	for _, fs := range state.Folders {
		for _, itemID := range fs.UploadedPhotos {
			if !state.ConsolidatedPhotos[itemID] && !state.InvalidPhotoIDs[itemID] {
				pendingPhotoIDs = append(pendingPhotoIDs, itemID)
			}
		}
		for _, videoID := range fs.UploadedVideos {
			if !state.ConsolidatedVideos[videoID] {
				pendingVideoIDs = append(pendingVideoIDs, videoID)
			}
		}
	}

	fmt.Printf("Photos: %d pending, %d already consolidated, %d invalid\n",
		len(pendingPhotoIDs), len(state.ConsolidatedPhotos), len(state.InvalidPhotoIDs))
	fmt.Printf("Videos: %d pending, %d already consolidated\n",
		len(pendingVideoIDs), len(state.ConsolidatedVideos))

	// Consolidate photos into single album
	if len(pendingPhotoIDs) > 0 {
		fmt.Println("\n📷 Consolidating photos...")

		if state.CollectionAlbumID == "" {
			albumID, err := createPhotosAlbum(httpClient, state.CollectionName)
			if err != nil {
				log.Printf("ERROR: Failed to create collection album: %v\n", err)
			} else {
				state.CollectionAlbumID = albumID
				fmt.Printf("  Created album: %s (ID: %s)\n", state.CollectionName, albumID)
				saveState(state, stateFile)
			}
		} else {
			fmt.Printf("  Using existing album ID: %s\n", state.CollectionAlbumID)
		}

		if state.CollectionAlbumID != "" {
			added := 0
			failed := 0
			pending := len(pendingPhotoIDs)
			writeCount := 0

			for i := 0; i < len(pendingPhotoIDs); i += 50 {
				// Rate limit: 30 writes/min — wait between requests
				if writeCount > 0 && writeCount%25 == 0 {
					fmt.Println("  ⏳ Pausing 60s for rate limit...")
					time.Sleep(60 * time.Second)
				}

				end := i + 50
				if end > len(pendingPhotoIDs) {
					end = len(pendingPhotoIDs)
				}
				batch := pendingPhotoIDs[i:end]

				err := batchAddMediaItemsToAlbum(httpClient, state.CollectionAlbumID, batch)
				writeCount++
				if err != nil {
					log.Printf("  ✗ Batch %d-%d failed: %v\n", i+1, end, err)

					// Check if rate limited — if so, wait and retry the batch
					if isRateLimited(err) {
						fmt.Println("  ⏳ Rate limited — waiting 60s before retry...")
						time.Sleep(60 * time.Second)
						err = batchAddMediaItemsToAlbum(httpClient, state.CollectionAlbumID, batch)
						writeCount++
						if err == nil {
							for _, id := range batch {
								state.ConsolidatedPhotos[id] = true
							}
							added += len(batch)
							pending -= len(batch)
							saveState(state, stateFile)
							fmt.Printf("  Progress: %d added, %d failed, %d pending\n", added, failed, pending)
							continue
						}
					}

					fmt.Println("    Retrying individually to find invalid IDs...")
					for j, id := range batch {
						if writeCount > 0 && writeCount%25 == 0 {
							fmt.Println("  ⏳ Pausing 60s for rate limit...")
							time.Sleep(60 * time.Second)
						}
						err := batchAddMediaItemsToAlbum(httpClient, state.CollectionAlbumID, []string{id})
						writeCount++
						if err != nil {
							if isRateLimited(err) {
								fmt.Println("  ⏳ Rate limited — waiting 60s...")
								time.Sleep(60 * time.Second)
								err = batchAddMediaItemsToAlbum(httpClient, state.CollectionAlbumID, []string{id})
								writeCount++
							}
						}
						if err != nil {
							log.Printf("    ✗ [%d/%d] Invalid media item ID: %s\n", j+1, len(batch), id)
							state.InvalidPhotoIDs[id] = true
							failed++
							pending--
						} else {
							fmt.Printf("    ✓ [%d/%d] Added\n", j+1, len(batch))
							state.ConsolidatedPhotos[id] = true
							added++
							pending--
						}
					}
					saveState(state, stateFile)
				} else {
					for _, id := range batch {
						state.ConsolidatedPhotos[id] = true
					}
					added += len(batch)
					pending -= len(batch)
					saveState(state, stateFile)
				}
				fmt.Printf("  Progress: %d added, %d failed, %d pending\n", added, failed, pending)
			}
		}
	}

	// Consolidate videos into single playlist
	if len(pendingVideoIDs) > 0 {
		fmt.Println("\n🎬 Consolidating videos...")

		ctx := context.Background()
		ytService, err := getYouTubeService(ctx, httpClient)
		if err != nil {
			log.Printf("ERROR: Failed to create YouTube service: %v\n", err)
			saveState(state, stateFile)
			return
		}

		if state.CollectionPlaylistID == "" {
			playlistID, err := createPlaylist(ytService, state.CollectionName)
			if err != nil {
				log.Printf("ERROR: Failed to create collection playlist: %v\n", err)
			} else {
				state.CollectionPlaylistID = playlistID
				fmt.Printf("  Created playlist: %s (ID: %s)\n", state.CollectionName, playlistID)
				saveState(state, stateFile)
			}
		} else {
			fmt.Printf("  Using existing playlist ID: %s\n", state.CollectionPlaylistID)
		}

		if state.CollectionPlaylistID != "" {
			added := 0
			for i, videoID := range pendingVideoIDs {
				err := addToPlaylistWithRetry(ytService, state.CollectionPlaylistID, videoID, maxRetries)
				if err != nil {
					log.Printf("  ✗ Failed to add video %s to playlist: %v\n", videoID, err)
				} else {
					state.ConsolidatedVideos[videoID] = true
					added++
				}
				saveState(state, stateFile)
				fmt.Printf("  Progress: %d/%d videos\n", i+1, len(pendingVideoIDs))
			}
		}
	}

	saveState(state, stateFile)
	fmt.Printf("\n✓ Consolidation complete!\n")
	fmt.Printf("  Photos: %d consolidated, %d invalid\n", len(state.ConsolidatedPhotos), len(state.InvalidPhotoIDs))
	fmt.Printf("  Videos: %d consolidated\n", len(state.ConsolidatedVideos))
	if state.CollectionAlbumID != "" {
		fmt.Printf("  Album: %s\n", state.CollectionAlbumID)
	}
	if state.CollectionPlaylistID != "" {
		fmt.Printf("  Playlist: https://www.youtube.com/playlist?list=%s\n", state.CollectionPlaylistID)
	}
	fmt.Printf("  State saved to %s\n", stateFile)
}

type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

func batchAddMediaItemsToAlbum(client *http.Client, albumID string, mediaItemIDs []string) error {
	body := map[string]interface{}{
		"mediaItemIds": mediaItemIDs,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("error marshaling request: %v", err)
	}

	url := fmt.Sprintf("%s/albums/%s:batchAddMediaItems", photosAPIURL, albumID)
	resp, err := client.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("error adding media items: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return &apiError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	return nil
}

func isRateLimited(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusTooManyRequests
}

func getVideoFiles(folderPath string) ([]string, error) {
	var videos []string
	supportedExts := map[string]bool{
		".mp4": true, ".mov": true, ".avi": true,
		".mkv": true, ".flv": true, ".wmv": true,
		".webm": true, ".m4v": true, ".mpg": true,
		".mpeg": true, ".3gp": true,
	}

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if supportedExts[ext] {
			videos = append(videos, filepath.Join(folderPath, entry.Name()))
		}
	}

	return videos, nil
}

func getImageFiles(folderPath string) ([]string, error) {
	var images []string
	supportedExts := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true,
		".gif": true, ".bmp": true, ".webp": true,
		".heic": true, ".heif": true, ".tiff": true,
		".tif": true, ".raw": true, ".ico": true,
		".orf": true, ".cr2": true, ".cr3": true,
		".nef": true, ".arw": true, ".dng": true,
		".rw2": true, ".raf": true, ".srw": true,
		".pef": true,
	}

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if supportedExts[ext] {
			images = append(images, filepath.Join(folderPath, entry.Name()))
		}
	}

	return images, nil
}

// Google Photos API functions (direct REST calls)

func createPhotosAlbum(client *http.Client, title string) (string, error) {
	body := map[string]interface{}{
		"album": map[string]string{
			"title": title,
		},
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("error marshaling album request: %v", err)
	}

	resp, err := client.Post(photosAPIURL+"/albums", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("error creating album: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("error creating album (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("error decoding album response: %v", err)
	}

	return result.ID, nil
}

func uploadPhotoBytes(client *http.Client, photoPath string) (string, error) {
	file, err := os.Open(photoPath)
	if err != nil {
		return "", fmt.Errorf("error opening photo file: %v", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("error getting file info: %v", err)
	}

	filename := filepath.Base(photoPath)
	fmt.Printf("    Uploading %s (%.2f MB)...\n", filename, float64(fileInfo.Size())/(1024*1024))

	req, err := http.NewRequest("POST", photosUploadURL, file)
	if err != nil {
		return "", fmt.Errorf("error creating upload request: %v", err)
	}

	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Goog-Upload-Content-Type", getMimeType(photoPath))
	req.Header.Set("X-Goog-Upload-Protocol", "raw")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error uploading photo bytes: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("error uploading photo (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	uploadToken, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading upload token: %v", err)
	}

	return string(uploadToken), nil
}

func createMediaItem(client *http.Client, uploadToken, albumID, filename string) (string, error) {
	body := map[string]interface{}{
		"albumId": albumID,
		"newMediaItems": []map[string]interface{}{
			{
				"description": fmt.Sprintf("Uploaded via Google Uploader\nOriginal filename: %s", filename),
				"simpleMediaItem": map[string]string{
					"uploadToken": uploadToken,
					"fileName":    filename,
				},
			},
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("error marshaling media item request: %v", err)
	}

	resp, err := client.Post(photosAPIURL+"/mediaItems:batchCreate", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("error creating media item: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("error creating media item (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		NewMediaItemResults []struct {
			Status struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"status"`
			MediaItem struct {
				ID string `json:"id"`
			} `json:"mediaItem"`
		} `json:"newMediaItemResults"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("error decoding media item response: %v", err)
	}

	if len(result.NewMediaItemResults) == 0 {
		return "", fmt.Errorf("no media item results returned")
	}

	item := result.NewMediaItemResults[0]
	if item.Status.Code != 0 {
		return "", fmt.Errorf("media item creation failed: %s", item.Status.Message)
	}

	return item.MediaItem.ID, nil
}

func uploadPhotoWithRetry(client *http.Client, photoPath, albumID string, maxRetries int) (string, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			fmt.Printf("    Retry attempt %d/%d...\n", attempt, maxRetries)
			time.Sleep(retryDelay)
		}

		uploadToken, err := uploadPhotoBytes(client, photoPath)
		if err != nil {
			lastErr = err
			fmt.Printf("    Upload failed: %v\n", err)
			continue
		}

		filename := filepath.Base(photoPath)
		mediaItemID, err := createMediaItem(client, uploadToken, albumID, filename)
		if err != nil {
			lastErr = err
			fmt.Printf("    Media item creation failed: %v\n", err)
			continue
		}

		return mediaItemID, nil
	}

	return "", fmt.Errorf("all retry attempts failed: %w", lastErr)
}

func getMimeType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	mimeTypes := map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".bmp":  "image/bmp",
		".webp": "image/webp",
		".heic": "image/heic",
		".heif": "image/heif",
		".tiff": "image/tiff",
		".tif":  "image/tiff",
		".raw":  "image/raw",
		".ico":  "image/x-icon",
		".orf":  "image/x-olympus-orf",
		".cr2":  "image/x-canon-cr2",
		".cr3":  "image/x-canon-cr3",
		".nef":  "image/x-nikon-nef",
		".arw":  "image/x-sony-arw",
		".dng":  "image/x-adobe-dng",
		".rw2":  "image/x-panasonic-rw2",
		".raf":  "image/x-fuji-raf",
		".srw":  "image/x-samsung-srw",
		".pef":  "image/x-pentax-pef",
	}
	if mime, ok := mimeTypes[ext]; ok {
		return mime
	}
	return "application/octet-stream"
}

func createPlaylist(service *youtube.Service, title string) (string, error) {
	playlist := &youtube.Playlist{
		Snippet: &youtube.PlaylistSnippet{
			Title:       title,
			Description: fmt.Sprintf("Playlist created from folder: %s", title),
		},
		Status: &youtube.PlaylistStatus{
			PrivacyStatus: "unlisted", // Change to "public" or "private" as needed
		},
	}

	call := service.Playlists.Insert([]string{"snippet", "status"}, playlist)
	response, err := call.Do()
	if err != nil {
		return "", err
	}

	return response.Id, nil
}

func uploadVideoWithRetry(service *youtube.Service, videoPath string, maxRetries int) (string, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			fmt.Printf("  Retry attempt %d/%d...\n", attempt, maxRetries)
			time.Sleep(retryDelay)
		}

		videoID, err := uploadVideo(service, videoPath)
		if err == nil {
			return videoID, nil
		}

		lastErr = err
		fmt.Printf("  Upload failed: %v\n", err)
	}

	return "", fmt.Errorf("all retry attempts failed: %w", lastErr)
}

func uploadVideo(service *youtube.Service, videoPath string) (string, error) {
	file, err := os.Open(videoPath)
	if err != nil {
		return "", fmt.Errorf("error opening video file: %v", err)
	}
	defer file.Close()

	// Get file info
	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("error getting file info: %v", err)
	}

	filename := filepath.Base(videoPath)
	title := strings.TrimSuffix(filename, filepath.Ext(filename))

	video := &youtube.Video{
		Snippet: &youtube.VideoSnippet{
			Title:       title,
			Description: fmt.Sprintf("Uploaded via Google Uploader\nOriginal filename: %s", filename),
			CategoryId:  "22", // People & Blogs - change as needed
		},
		Status: &youtube.VideoStatus{
			PrivacyStatus:           "unlisted", // Change to "public" or "private" as needed
			SelfDeclaredMadeForKids: false,
		},
	}

	call := service.Videos.Insert([]string{"snippet", "status"}, video)

	// Show upload progress
	fmt.Printf("  Uploading %s (%.2f MB)...\n", filename, float64(fileInfo.Size())/(1024*1024))

	response, err := call.Media(file).Do()
	if err != nil {
		return "", fmt.Errorf("error uploading video: %v", err)
	}

	return response.Id, nil
}

func addToPlaylistWithRetry(service *youtube.Service, playlistID, videoID string, maxRetries int) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			time.Sleep(retryDelay)
		}

		err := addToPlaylist(service, playlistID, videoID)
		if err == nil {
			return nil
		}

		lastErr = err
	}

	return lastErr
}

func addToPlaylist(service *youtube.Service, playlistID, videoID string) error {
	playlistItem := &youtube.PlaylistItem{
		Snippet: &youtube.PlaylistItemSnippet{
			PlaylistId: playlistID,
			ResourceId: &youtube.ResourceId{
				Kind:    "youtube#video",
				VideoId: videoID,
			},
		},
	}

	call := service.PlaylistItems.Insert([]string{"snippet"}, playlistItem)
	_, err := call.Do()
	return err
}
