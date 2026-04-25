package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	configDir       = ".google_uploader"
	maxRetries      = 3
	retryDelay      = 5 * time.Second
	photosUploadURL = "https://photoslibrary.googleapis.com/v1/uploads"
	photosAPIURL    = "https://photoslibrary.googleapis.com/v1"
	photosScope     = "https://www.googleapis.com/auth/photoslibrary.appendonly"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: google-uploader /path/to/folder1 [/path/to/folder2 ...]\n")
		os.Exit(1)
	}

	folders := os.Args[1:]

	// Resolve and validate folder paths
	var folderPaths []string
	for _, folder := range folders {
		absPath, err := filepath.Abs(folder)
		if err != nil {
			log.Fatalf("Error resolving path %s: %v", folder, err)
		}
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			log.Fatalf("Error: Folder does not exist: %s", absPath)
		}
		folderPaths = append(folderPaths, absPath)
	}
	sort.Strings(folderPaths)

	// Determine the common parent for the top-level tracker
	trackerRoot := ""
	for _, path := range folderPaths {
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

	// Filter out completed folders
	var activePaths []string
	for _, path := range folderPaths {
		folderName := filepath.Base(path)
		if topTracker != nil && topTracker.IsCompleted(folderName) {
			continue
		}
		activePaths = append(activePaths, path)
	}

	if len(activePaths) == 0 {
		fmt.Println("All folders have been fully uploaded.")
		return
	}

	// Load per-folder trackers and clear failed files for retry
	folderTrackers := make(map[string]*tracker.FolderTracker)
	for _, folderPath := range activePaths {
		ft := tracker.LoadFolder(folderPath)
		// Clear failed files so they get retried
		if len(ft.FailedFiles) > 0 {
			ft.FailedFiles = make(map[string]string)
		}
		folderTrackers[folderPath] = ft
	}

	// Save trackers on Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n⚠ Interrupted — saving tracker state...")
		for folderPath, ft := range folderTrackers {
			tracker.SaveFolder(folderPath, ft)
		}
		if topTracker != nil && trackerRoot != "" {
			tracker.SaveTopLevel(trackerRoot, topTracker)
		}
		fmt.Println("Tracker state saved.")
		os.Exit(1)
	}()

	ctx := context.Background()

	config, err := getOAuthConfig()
	if err != nil {
		log.Fatalf("Error loading credentials: %v", err)
	}
	httpClient := getClient(ctx, config)

	fmt.Printf("\n=== Google Uploader ===\n")
	fmt.Printf("Processing %d folder(s)\n", len(activePaths))

	totalPhotosUploaded := 0
	totalVideosUploaded := 0

	// Pre-scan all folders for total counts
	overallTotalPhotos := 0
	overallTotalVideos := 0
	overallCompletedPhotos := 0
	overallCompletedVideos := 0
	for _, folderPath := range activePaths {
		ft := folderTrackers[folderPath]
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

	for folderIdx, folderPath := range activePaths {
		ft := folderTrackers[folderPath]
		folderName := filepath.Base(folderPath)

		photos, err := getImageFiles(folderPath)
		if err != nil {
			log.Printf("ERROR: Failed to read image files from %s: %v\n", folderPath, err)
			continue
		}

		if len(photos) == 0 {
			fmt.Printf("\n[Folder %d/%d] %s — no photos found, skipping\n", folderIdx+1, len(activePaths), folderName)
			continue
		}

		completedCount := 0
		for _, p := range photos {
			if ft.HasPhoto(filepath.Base(p)) {
				completedCount++
			}
		}
		fmt.Printf("\n[Folder %d/%d] %s — %d photo(s)\n", folderIdx+1, len(activePaths), folderName, len(photos))

		if completedCount > 0 {
			fmt.Printf("  Resuming: %d/%d photos already uploaded\n", completedCount, len(photos))
		}

		if completedCount == len(photos) {
			fmt.Printf("  ✓ Photos already complete for %s\n", folderName)
			continue
		}

		fmt.Println("  " + strings.Repeat("-", 46))

		for i, photoPath := range photos {
			filename := filepath.Base(photoPath)

			if itemID, exists := ft.UploadedPhotos[filename]; exists {
				fmt.Printf("  [%d/%d] ✓ Skipping (already uploaded): %s (ID: %s)\n", i+1, len(photos), filename, itemID)
				continue
			}

			if _, failed := ft.FailedFiles[filename]; failed {
				fmt.Printf("  [%d/%d] ✗ Skipping (previously failed): %s\n", i+1, len(photos), filename)
				continue
			}

			fmt.Printf("  [%d/%d] Uploading: %s\n", i+1, len(photos), filename)

			itemID, err := uploadPhotoWithRetry(httpClient, photoPath, maxRetries)
			if err != nil {
				log.Printf("  ✗ Failed to upload %s after %d attempts: %v — skipping\n", filename, maxRetries, err)
				ft.FailedFiles[filename] = err.Error()
				tracker.SaveFolder(folderPath, ft)
				continue
			}

			fmt.Printf("    ✓ Photo uploaded successfully! ID: %s\n", itemID)

			ft.UploadedPhotos[filename] = itemID
			totalPhotosUploaded++
			overallCompletedPhotos++
			tracker.SaveFolder(folderPath, ft)

			fmt.Printf("    Progress: %d/%d photos (folder) | %d/%d photos (overall)\n",
				completedCount+totalPhotosUploaded, len(photos), overallCompletedPhotos, overallTotalPhotos)
		}

		fmt.Printf("  ✓ Photos pass complete for %s\n", folderName)
	}

	// ── Global Pass 2: Videos across all folders ──
	fmt.Println("\n🎬 Pass 2: Uploading videos to YouTube...")
	fmt.Printf("  Overall: %d/%d videos completed\n", overallCompletedVideos, overallTotalVideos)

	// Determine if YouTube service is needed
	needsYouTube := false
	for _, folderPath := range activePaths {
		ft := folderTrackers[folderPath]
		videos, _ := getVideoFiles(folderPath)
		for _, v := range videos {
			if !ft.HasVideo(filepath.Base(v)) {
				needsYouTube = true
				break
			}
		}
		if needsYouTube {
			break
		}
	}

	var ytService *youtube.Service
	if needsYouTube {
		ytService, err = getYouTubeService(ctx, httpClient)
		if err != nil {
			log.Fatalf("Error creating YouTube service: %v", err)
		}
	}

	for folderIdx, folderPath := range activePaths {
		ft := folderTrackers[folderPath]
		folderName := filepath.Base(folderPath)

		videos, err := getVideoFiles(folderPath)
		if err != nil {
			log.Printf("ERROR: Failed to read video files from %s: %v\n", folderPath, err)
			continue
		}

		if len(videos) == 0 {
			fmt.Printf("\n[Folder %d/%d] %s — no videos found, skipping\n", folderIdx+1, len(activePaths), folderName)
			continue
		}

		completedCount := 0
		for _, v := range videos {
			if ft.HasVideo(filepath.Base(v)) {
				completedCount++
			}
		}
		fmt.Printf("\n[Folder %d/%d] %s — %d video(s)\n", folderIdx+1, len(activePaths), folderName, len(videos))

		if completedCount > 0 {
			fmt.Printf("  Resuming: %d/%d videos already uploaded\n", completedCount, len(videos))
		}

		if completedCount == len(videos) {
			fmt.Printf("  ✓ Videos already complete for %s\n", folderName)
			continue
		}

		fmt.Println("  " + strings.Repeat("-", 46))

		for i, videoPath := range videos {
			filename := filepath.Base(videoPath)

			if videoID, exists := ft.UploadedVideos[filename]; exists {
				fmt.Printf("  [%d/%d] ✓ Skipping (already uploaded): %s (ID: %s)\n", i+1, len(videos), filename, videoID)
				continue
			}

			if _, failed := ft.FailedFiles[filename]; failed {
				fmt.Printf("  [%d/%d] ✗ Skipping (previously failed): %s\n", i+1, len(videos), filename)
				continue
			}

			fmt.Printf("  [%d/%d] Uploading: %s\n", i+1, len(videos), filename)

			videoID, err := uploadVideoWithRetry(ytService, videoPath, maxRetries)
			if err != nil {
				log.Printf("  ✗ Failed to upload %s after %d attempts: %v — skipping\n", filename, maxRetries, err)
				ft.FailedFiles[filename] = err.Error()
				tracker.SaveFolder(folderPath, ft)
				continue
			}

			fmt.Printf("    ✓ Video uploaded successfully! ID: %s\n", videoID)

			ft.UploadedVideos[filename] = videoID
			totalVideosUploaded++
			overallCompletedVideos++
			tracker.SaveFolder(folderPath, ft)

			fmt.Printf("    Progress: %d/%d videos (folder) | %d/%d videos (overall)\n",
				completedCount+totalVideosUploaded, len(videos), overallCompletedVideos, overallTotalVideos)
		}

		fmt.Printf("  ✓ Videos pass complete for %s\n", folderName)
	}

	// ── Mark folders as completed in top-level tracker ──
	for _, folderPath := range activePaths {
		ft := folderTrackers[folderPath]
		photos, _ := getImageFiles(folderPath)
		videos, _ := getVideoFiles(folderPath)
		if len(ft.FailedFiles) == 0 && ft.IsFullyUploaded(photos, videos) {
			if topTracker != nil {
				topTracker.MarkComplete(filepath.Base(folderPath))
			}
		}
		tracker.SaveFolder(folderPath, ft)
	}
	if topTracker != nil && trackerRoot != "" {
		tracker.SaveTopLevel(trackerRoot, topTracker)
	}

	fmt.Println("\n" + strings.Repeat("=", 50))

	// Collect total failures
	totalFailed := 0
	for _, folderPath := range activePaths {
		totalFailed += len(folderTrackers[folderPath].FailedFiles)
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

	for _, folderPath := range activePaths {
		ft := folderTrackers[folderPath]
		folderName := filepath.Base(folderPath)
		parts := []string{folderName + ":"}
		if len(ft.UploadedPhotos) > 0 {
			parts = append(parts, fmt.Sprintf("%d photos", len(ft.UploadedPhotos)))
		}
		if len(ft.UploadedVideos) > 0 {
			parts = append(parts, fmt.Sprintf("%d videos", len(ft.UploadedVideos)))
		}
		if len(ft.FailedFiles) > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", len(ft.FailedFiles)))
		}
		fmt.Printf("  %s\n", strings.Join(parts, " | "))
	}

	if totalFailed > 0 {
		fmt.Println("\nRe-run to retry failed files.")
	}
}

// ── OAuth & Auth ──

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

// ── File scanning ──

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

// ── Google Photos upload ──

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

func createMediaItem(client *http.Client, uploadToken, filename string) (string, error) {
	body := map[string]interface{}{
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

func uploadPhotoWithRetry(client *http.Client, photoPath string, maxRetries int) (string, error) {
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
		mediaItemID, err := createMediaItem(client, uploadToken, filename)
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

// ── YouTube upload ──

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
			CategoryId:  "22",
		},
		Status: &youtube.VideoStatus{
			PrivacyStatus:           "unlisted",
			SelfDeclaredMadeForKids: false,
		},
	}

	call := service.Videos.Insert([]string{"snippet", "status"}, video)

	fmt.Printf("  Uploading %s (%.2f MB)...\n", filename, float64(fileInfo.Size())/(1024*1024))

	response, err := call.Media(file).Do()
	if err != nil {
		return "", fmt.Errorf("error uploading video: %v", err)
	}

	return response.Id, nil
}
