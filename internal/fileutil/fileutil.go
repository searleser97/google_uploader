package fileutil

import (
	"os"
	"path/filepath"
	"strings"
)

var SkipExtensions = map[string]bool{
	".bin": true,
}

var ImageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".bmp": true,
	".tiff": true, ".tif": true, ".webp": true, ".heic": true,
	".heif": true, ".avif": true,
	// RAW formats
	".orf": true, ".cr2": true, ".cr3": true, ".nef": true,
	".arw": true, ".dng": true, ".rw2": true, ".raf": true,
	".srw": true, ".pef": true,
}

var VideoExtensions = map[string]bool{
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true,
	".wmv": true, ".flv": true, ".webm": true, ".m4v": true,
	".mpg": true, ".mpeg": true, ".3gp": true,
}

type CollectOptions struct {
	ImagesOnly     bool
	SkipDirPattern func(name string) bool // return true to skip a directory
	SkipDir        string                 // absolute path to skip entirely
}

func CollectFiles(dir string, opts CollectOptions) ([]string, error) {
	var results []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			if opts.SkipDir != "" && fullPath == opts.SkipDir {
				continue
			}
			if opts.SkipDirPattern != nil && opts.SkipDirPattern(entry.Name()) {
				continue
			}
			sub, err := CollectFiles(fullPath, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, sub...)
		} else if entry.Type().IsRegular() {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if SkipExtensions[ext] {
				continue
			}
			if opts.ImagesOnly && !ImageExtensions[ext] {
				continue
			}
			results = append(results, fullPath)
		}
	}
	return results, nil
}

type FileInfo struct {
	Path string
	Name string
	Size int64
}

func GetFileInfo(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Path: path,
		Name: info.Name(),
		Size: info.Size(),
	}, nil
}

func IsImageFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ImageExtensions[ext]
}

func IsVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return VideoExtensions[ext]
}
