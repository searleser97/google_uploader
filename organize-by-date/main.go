package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/searleser97/media_workflow_tools/internal/exif"
	"github.com/searleser97/media_workflow_tools/internal/fileutil"
	"github.com/searleser97/media_workflow_tools/internal/progress"
)

var dateFolderPattern = regexp.MustCompile(`^\d{4}_\d{2}_\d{2}$`)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: organize-by-date <source-folder> [destination-folder]\n")
		os.Exit(1)
	}

	folder, err := filepath.Abs(os.Args[1])
	if err != nil {
		log.Fatalf("Error resolving source path: %v", err)
	}

	destFolder := folder
	if len(os.Args) >= 3 {
		destFolder, err = filepath.Abs(os.Args[2])
		if err != nil {
			log.Fatalf("Error resolving destination path: %v", err)
		}
	}

	info, err := os.Stat(folder)
	if err != nil || !info.IsDir() {
		log.Fatalf("Error: Source folder '%s' does not exist.", folder)
	}

	if err := os.MkdirAll(destFolder, 0755); err != nil {
		log.Fatalf("Error creating destination folder: %v", err)
	}

	// Collect files (skip date-named folders and dest folder)
	opts := fileutil.CollectOptions{
		SkipDirPattern: func(name string) bool {
			return dateFolderPattern.MatchString(name)
		},
		SkipDir: destFolder,
	}
	files, err := fileutil.CollectFiles(folder, opts)
	if err != nil {
		log.Fatalf("Error scanning files: %v", err)
	}

	// Load metadata cache
	cacheFile := filepath.Join(folder, ".organize-cache.json")
	cache := loadCache(cacheFile)

	fmt.Println("Scanning metadata...")

	type fileEntry struct {
		filePath string
		dateStr  string
	}

	plan := make(map[string][]fileEntry)
	var unknownFiles []string

	bar := progress.NewBar(30)
	for i, f := range files {
		dateStr := getCachedDate(f, cache)

		folderName := ""
		if dateStr != "" {
			t, err := exif.ParseDateString(dateStr)
			if err == nil {
				folderName = exif.FormatAsFolder(t)
			}
		}

		if folderName != "" {
			plan[folderName] = append(plan[folderName], fileEntry{filePath: f, dateStr: dateStr})
		} else {
			unknownFiles = append(unknownFiles, f)
		}
		bar.Print(i+1, len(files))
	}

	// Save cache after scan
	saveCache(cacheFile, cache)
	bar.Finish()
	fmt.Println()

	// Show plan
	sortedDates := make([]string, 0, len(plan))
	for k := range plan {
		sortedDates = append(sortedDates, k)
	}
	sort.Strings(sortedDates)

	for _, date := range sortedDates {
		fmt.Printf("  %s/ (%d files)\n", date, len(plan[date]))
	}
	if len(unknownFiles) > 0 {
		fmt.Printf("  Unknown date: %d file(s) (will be skipped)\n", len(unknownFiles))
		for _, f := range unknownFiles {
			info, err := os.Stat(f)
			sizeLabel := "?"
			if err == nil {
				size := info.Size()
				switch {
				case size == 0:
					sizeLabel = "0 bytes"
				case size < 1024:
					sizeLabel = fmt.Sprintf("%d B", size)
				case size < 1048576:
					sizeLabel = fmt.Sprintf("%.1f KB", float64(size)/1024)
				default:
					sizeLabel = fmt.Sprintf("%.1f MB", float64(size)/1048576)
				}
			}
			rel, _ := filepath.Rel(folder, f)
			fmt.Printf("    - %s (%s)\n", rel, sizeLabel)
		}
	}

	organized := len(files) - len(unknownFiles)
	fmt.Printf("\nTotal: %d file(s) into %d folder(s)\n", organized, len(sortedDates))

	if len(sortedDates) == 0 {
		fmt.Println("Nothing to organize.")
		os.Remove(cacheFile)
		return
	}

	// Prompt user
	fmt.Print("\nProceed with moving files? (y/n): ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" {
		fmt.Println("Skipped.")
		os.Remove(cacheFile)
		return
	}

	total := organized
	moved := 0
	renamed := 0
	var failedDeletes []string
	bar = progress.NewBar(30)

	for _, date := range sortedDates {
		destDir := filepath.Join(destFolder, date)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			log.Printf("Error creating %s: %v", destDir, err)
			continue
		}

		// Track used filenames
		usedNames := make(map[string]bool)
		entries, _ := os.ReadDir(destDir)
		for _, e := range entries {
			usedNames[strings.ToLower(e.Name())] = true
		}

		for _, entry := range plan[date] {
			// Skip files already in the correct dest folder
			if filepath.Dir(entry.filePath) == destDir {
				moved++
				continue
			}

			baseName := filepath.Base(entry.filePath)
			destName := baseName

			if usedNames[strings.ToLower(destName)] {
				ext := filepath.Ext(baseName)
				stem := strings.TrimSuffix(baseName, ext)
				suffix := ""
				t, err := exif.ParseDateString(entry.dateStr)
				if err == nil {
					suffix = exif.FormatAsDateTimeSuffix(t)
				} else {
					suffix = fmt.Sprintf("%d", os.Getpid())
				}
				destName = fmt.Sprintf("%s_%s%s", stem, suffix, ext)
				counter := 2
				for usedNames[strings.ToLower(destName)] {
					destName = fmt.Sprintf("%s_%s_%d%s", stem, suffix, counter, ext)
					counter++
				}
				renamed++
			}

			usedNames[strings.ToLower(destName)] = true
			destPath := filepath.Join(destDir, destName)

			// Check for interrupted copy from previous run
			destInfo, destErr := os.Stat(destPath)
			srcInfo, srcErr := os.Stat(entry.filePath)
			if destErr == nil && srcErr == nil && srcInfo.Size() == destInfo.Size() {
				if rmErr := os.Remove(entry.filePath); rmErr != nil {
					log.Printf("Warning: could not remove duplicate source %s: %v", entry.filePath, rmErr)
				}
				moved++
				continue
			}

			// Try rename first (fast, same filesystem)
			err := os.Rename(entry.filePath, destPath)
			if err != nil {
				// Cross-device: copy + delete
				if copyErr := copyFile(entry.filePath, destPath); copyErr != nil {
					log.Printf("Error copying %s: %v", entry.filePath, copyErr)
					continue
				}
				if rmErr := os.Remove(entry.filePath); rmErr != nil {
					if os.IsPermission(rmErr) {
						failedDeletes = append(failedDeletes, entry.filePath)
					} else {
						log.Printf("Error removing %s: %v", entry.filePath, rmErr)
					}
				}
			}

			moved++
			bar.Print(moved, total)
		}
	}
	bar.Finish()

	fmt.Printf("\nMoved %d file(s) into %d folder(s).\n", moved, len(sortedDates))
	if renamed > 0 {
		fmt.Printf("Renamed %d file(s) to avoid duplicates.\n", renamed)
	}
	if len(failedDeletes) > 0 {
		fmt.Printf("\n⚠️  Could not delete %d source file(s) (permission denied).\n", len(failedDeletes))
		fmt.Println("   Files were copied successfully but originals remain on the source drive.")
	}

	// Clean up empty directories
	protectedDirs := make(map[string]bool)
	protectedDirs[destFolder] = true
	for _, d := range sortedDates {
		protectedDirs[filepath.Join(destFolder, d)] = true
	}
	removeEmptyDirs(folder, protectedDirs)

	os.Remove(cacheFile)
}

func getCachedDate(filePath string, cache map[string]string) string {
	key := cacheKey(filePath)
	if cached, ok := cache[key]; ok {
		return cached
	}

	// Try native EXIF first
	t, err := exif.GetCreationDate(filePath)
	dateStr := ""
	if err == nil {
		dateStr = exif.FormatForDisplay(t)
	}
	cache[key] = dateStr
	return dateStr
}

func cacheKey(filePath string) string {
	info, err := os.Stat(filePath)
	if err != nil {
		return filePath
	}
	return fmt.Sprintf("%s|%d|%d", filepath.Base(filePath), info.Size(), info.ModTime().UnixMilli())
}

func loadCache(path string) map[string]string {
	cache := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	json.Unmarshal(data, &cache)
	return cache
}

func saveCache(path string, cache map[string]string) {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0644)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Close()
}

func removeEmptyDirs(dir string, protectedDirs map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		if protectedDirs[fullPath] {
			continue
		}
		removeEmptyDirs(fullPath, protectedDirs)
		remaining, _ := os.ReadDir(fullPath)
		if len(remaining) == 0 {
			os.Remove(fullPath)
			rel, _ := filepath.Rel(dir, fullPath)
			fmt.Printf("  Removed empty directory: %s/\n", rel)
		}
	}
}
