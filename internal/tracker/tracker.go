package tracker

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	TopLevelFileName = ".upload_tracker.json"
	FolderFileName   = ".upload_status.json"
)

// TopLevelTracker lives at the root of the organized media directory.
// It provides a quick lookup to know whether a folder has been fully backed up.
type TopLevelTracker struct {
	Folders map[string]*FolderCompletion `json:"folders"`
}

type FolderCompletion struct {
	Completed bool `json:"completed"`
}

// FolderTracker lives inside each date folder and tracks individual uploaded items.
type FolderTracker struct {
	UploadedPhotos map[string]string `json:"uploaded_photos"` // filename -> mediaItemID
	UploadedVideos map[string]string `json:"uploaded_videos"` // filename -> videoID
	FailedFiles    map[string]string `json:"failed_files"`    // filename -> error message
}

// LoadTopLevel loads the top-level tracker from the given directory.
func LoadTopLevel(dir string) *TopLevelTracker {
	t := &TopLevelTracker{Folders: make(map[string]*FolderCompletion)}
	data, err := os.ReadFile(filepath.Join(dir, TopLevelFileName))
	if err != nil {
		return t
	}
	json.Unmarshal(data, t)
	if t.Folders == nil {
		t.Folders = make(map[string]*FolderCompletion)
	}
	return t
}

// SaveTopLevel saves the top-level tracker to the given directory.
func SaveTopLevel(dir string, t *TopLevelTracker) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, TopLevelFileName), data, 0644)
}

// IsCompleted checks if a folder is marked as completed.
func (t *TopLevelTracker) IsCompleted(folderName string) bool {
	if fc, ok := t.Folders[folderName]; ok {
		return fc.Completed
	}
	return false
}

// MarkComplete marks a folder as completed.
func (t *TopLevelTracker) MarkComplete(folderName string) {
	if t.Folders[folderName] == nil {
		t.Folders[folderName] = &FolderCompletion{}
	}
	t.Folders[folderName].Completed = true
}

// MarkIncomplete marks a folder as incomplete.
func (t *TopLevelTracker) MarkIncomplete(folderName string) {
	if t.Folders[folderName] == nil {
		t.Folders[folderName] = &FolderCompletion{}
	}
	t.Folders[folderName].Completed = false
}

// LoadFolder loads the per-folder tracker from the given folder path.
func LoadFolder(folderPath string) *FolderTracker {
	ft := &FolderTracker{
		UploadedPhotos: make(map[string]string),
		UploadedVideos: make(map[string]string),
		FailedFiles:    make(map[string]string),
	}
	data, err := os.ReadFile(filepath.Join(folderPath, FolderFileName))
	if err != nil {
		return ft
	}
	json.Unmarshal(data, ft)
	if ft.UploadedPhotos == nil {
		ft.UploadedPhotos = make(map[string]string)
	}
	if ft.UploadedVideos == nil {
		ft.UploadedVideos = make(map[string]string)
	}
	if ft.FailedFiles == nil {
		ft.FailedFiles = make(map[string]string)
	}
	return ft
}

// SaveFolder saves the per-folder tracker to the given folder path.
func SaveFolder(folderPath string, ft *FolderTracker) error {
	data, err := json.MarshalIndent(ft, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(folderPath, FolderFileName), data, 0644)
}

// HasPhoto checks if a photo filename is already tracked as uploaded.
func (ft *FolderTracker) HasPhoto(filename string) bool {
	_, ok := ft.UploadedPhotos[filename]
	return ok
}

// HasVideo checks if a video filename is already tracked as uploaded.
func (ft *FolderTracker) HasVideo(filename string) bool {
	_, ok := ft.UploadedVideos[filename]
	return ok
}

// MergePhotos adds photo entries that don't already exist in the tracker.
// Returns the number of new entries added.
func (ft *FolderTracker) MergePhotos(photos map[string]string) int {
	added := 0
	for filename, id := range photos {
		if _, exists := ft.UploadedPhotos[filename]; !exists {
			ft.UploadedPhotos[filename] = id
			added++
		}
	}
	return added
}

// MergeVideos adds video entries that don't already exist in the tracker.
// Returns the number of new entries added.
func (ft *FolderTracker) MergeVideos(videos map[string]string) int {
	added := 0
	for filename, id := range videos {
		if _, exists := ft.UploadedVideos[filename]; !exists {
			ft.UploadedVideos[filename] = id
			added++
		}
	}
	return added
}

// IsFullyUploaded checks if all given photo and video files are tracked as uploaded.
func (ft *FolderTracker) IsFullyUploaded(photoFiles, videoFiles []string) bool {
	for _, f := range photoFiles {
		if _, ok := ft.UploadedPhotos[filepath.Base(f)]; !ok {
			return false
		}
	}
	for _, f := range videoFiles {
		if _, ok := ft.UploadedVideos[filepath.Base(f)]; !ok {
			return false
		}
	}
	return true
}
