package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/searleser97/media_workflow_tools/internal/exif"
	"github.com/searleser97/media_workflow_tools/internal/fileutil"
	"github.com/searleser97/media_workflow_tools/internal/progress"
)

func main() {
	imagesOnly := flag.Bool("images-only", false, "Only process image files")
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: copy-missing-files [--images-only] <origin_folder1> [origin_folder2 ...] <target_folder>\n")
		os.Exit(1)
	}

	target := args[len(args)-1]
	origins := args[:len(args)-1]

	// Validate paths
	for _, origin := range origins {
		info, err := os.Stat(origin)
		if err != nil || !info.IsDir() {
			log.Fatalf("Error: Origin folder '%s' does not exist.", origin)
		}
	}
	targetInfo, err := os.Stat(target)
	if err != nil || !targetInfo.IsDir() {
		log.Fatalf("Error: Target folder '%s' does not exist.", target)
	}

	// Collect all files from origins
	var originFiles []string
	opts := fileutil.CollectOptions{ImagesOnly: *imagesOnly}
	for _, origin := range origins {
		files, err := fileutil.CollectFiles(origin, opts)
		if err != nil {
			log.Fatalf("Error reading origin folder '%s': %v", origin, err)
		}
		originFiles = append(originFiles, files...)
	}
	fmt.Printf("Total files found in origin(s): %d\n\n", len(originFiles))

	// Build lookup: filename -> []entry (name+size)
	type originEntry struct {
		fullPath string
		size     int64
		found    bool
	}
	lookup := make(map[string][]*originEntry)
	for _, f := range originFiles {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		name := filepath.Base(f)
		lookup[name] = append(lookup[name], &originEntry{
			fullPath: f,
			size:     info.Size(),
		})
	}

	remaining := len(originFiles)

	// Scan target recursively to mark matches
	var scanTarget func(dir string)
	scanTarget = func(dir string) {
		if remaining == 0 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if remaining == 0 {
				return
			}
			fullPath := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				scanTarget(fullPath)
			} else if entries, ok := lookup[entry.Name()]; ok {
				info, err := os.Stat(fullPath)
				if err != nil {
					continue
				}
				for _, e := range entries {
					if !e.found && e.size == info.Size() {
						e.found = true
						remaining--
						break
					}
				}
			}
		}
	}
	scanTarget(target)

	// Collect missing files
	var missing []string
	for _, entries := range lookup {
		for _, e := range entries {
			if !e.found {
				missing = append(missing, e.fullPath)
			}
		}
	}

	fmt.Printf("Missing files in target: %d\n\n", len(missing))

	// Display missing files with creation dates
	for _, f := range missing {
		dateStr := "Unknown"
		t, err := exif.GetCreationDate(f)
		if err == nil {
			dateStr = exif.FormatForDisplay(t)
		}
		fmt.Printf("  %s  (Created: %s)\n", f, dateStr)
	}

	if len(missing) == 0 {
		return
	}

	// Prompt user
	fmt.Print("Copy missing files to target? (y/n): ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" {
		fmt.Println("Skipped.")
		return
	}

	// Copy to MISSING_FROM_ORIGIN
	destDir := filepath.Join(target, "MISSING_FROM_ORIGIN")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		log.Fatalf("Error creating destination directory: %v", err)
	}

	// Track used names for deduplication
	usedNames := make(map[string]bool)
	entries, _ := os.ReadDir(destDir)
	for _, e := range entries {
		usedNames[strings.ToLower(e.Name())] = true
	}

	bar := progress.NewBar(30)
	total := len(missing)
	copied := 0

	for i, f := range missing {
		baseName := filepath.Base(f)
		destName := baseName

		if usedNames[strings.ToLower(destName)] {
			ext := filepath.Ext(baseName)
			stem := strings.TrimSuffix(baseName, ext)
			counter := 2
			destName = fmt.Sprintf("%s_%d%s", stem, counter, ext)
			for usedNames[strings.ToLower(destName)] {
				counter++
				destName = fmt.Sprintf("%s_%d%s", stem, counter, ext)
			}
		}
		usedNames[strings.ToLower(destName)] = true

		if err := copyFile(f, filepath.Join(destDir, destName)); err != nil {
			log.Printf("Error copying %s: %v", f, err)
			continue
		}
		copied++
		bar.Print(i+1, total)
	}
	bar.Finish()
	fmt.Printf("\nCopied %d file(s) to %s\n", copied, destDir)
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
