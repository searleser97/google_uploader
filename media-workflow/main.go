package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	session := flag.String("session", "", "Session name (deprecated, ignored)")
	collection := flag.String("collection", "", "Collection name (deprecated, ignored)")
	imagesOnly := flag.Bool("images-only", false, "Only process image files in copy-missing-files")
	skipCopy := flag.Bool("skip-copy", false, "Skip the copy-missing-files step")
	skipOrganize := flag.Bool("skip-organize", false, "Skip the organize-by-date step")
	skipUpload := flag.Bool("skip-upload", false, "Skip the google-uploader step")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: media-workflow [options] <origin1> [origin2 ...] <target>

Runs the full media workflow pipeline:
  1. copy-missing-files  — copy new files from origin(s) to target
  2. organize-by-date    — sort files in target into YYYY_MM_DD folders
  3. google-uploader     — upload organized folders to Google Photos/YouTube

Arguments:
  origin(s)   Source folder(s) to copy from (e.g., SD card mount)
  target      Working directory where files are organized and uploaded from

Options:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	// Warn about deprecated flags
	if *session != "" {
		fmt.Fprintf(os.Stderr, "Warning: -session flag is deprecated and ignored (tracking is now global)\n")
	}
	if *collection != "" {
		fmt.Fprintf(os.Stderr, "Warning: -collection flag is deprecated and ignored\n")
	}

	args := flag.Args()
	if len(args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	target := args[len(args)-1]
	origins := args[:len(args)-1]

	var err error
	target, err = filepath.Abs(target)
	if err != nil {
		log.Fatalf("Error resolving target path: %v", err)
	}
	for i, o := range origins {
		origins[i], err = filepath.Abs(o)
		if err != nil {
			log.Fatalf("Error resolving origin path: %v", err)
		}
	}

	selfDir := filepath.Dir(selfPath())

	// Step 1: copy-missing-files
	if !*skipCopy {
		fmt.Println("═══════════════════════════════════════════")
		fmt.Println("  Step 1/3: Copying missing files")
		fmt.Println("═══════════════════════════════════════════")
		copyArgs := []string{}
		if *imagesOnly {
			copyArgs = append(copyArgs, "--images-only")
		}
		copyArgs = append(copyArgs, origins...)
		copyArgs = append(copyArgs, target)

		if err := runTool(selfDir, "copy-missing-files", copyArgs...); err != nil {
			log.Fatalf("copy-missing-files failed: %v", err)
		}
		fmt.Println()
	}

	// Step 2: organize-by-date
	if !*skipOrganize {
		fmt.Println("═══════════════════════════════════════════")
		fmt.Println("  Step 2/3: Organizing by date")
		fmt.Println("═══════════════════════════════════════════")
		missingDir := filepath.Join(target, "MISSING_FROM_ORIGIN")
		if _, err := os.Stat(missingDir); os.IsNotExist(err) {
			fmt.Println("No MISSING_FROM_ORIGIN folder found, skipping.")
		} else if err := runTool(selfDir, "organize-by-date", missingDir, target); err != nil {
			log.Fatalf("organize-by-date failed: %v", err)
		}
		fmt.Println()
	}

	// Step 3: google-uploader
	if !*skipUpload {
		fmt.Println("═══════════════════════════════════════════")
		fmt.Println("  Step 3/3: Uploading")
		fmt.Println("═══════════════════════════════════════════")

		folders, err := getDateFolders(target)
		if err != nil {
			log.Fatalf("Error scanning target for date folders: %v", err)
		}
		if len(folders) == 0 {
			fmt.Println("No date folders found to upload.")
		} else {
			uploadArgs := folders

			if err := runTool(selfDir, "google-uploader", uploadArgs...); err != nil {
				log.Fatalf("google-uploader failed: %v", err)
			}
		}
		fmt.Println()
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Workflow complete!")
	fmt.Println("═══════════════════════════════════════════")
}

func selfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe
	}
	return resolved
}

func findTool(selfDir, name string) string {
	candidate := filepath.Join(selfDir, name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	p, err := exec.LookPath(name)
	if err == nil {
		return p
	}
	return name
}

func runTool(selfDir, name string, args ...string) error {
	toolPath := findTool(selfDir, name)
	fmt.Printf("Running: %s %s\n\n", name, strings.Join(args, " "))
	cmd := exec.Command(toolPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func getDateFolders(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var folders []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) == 10 &&
			name[4] == '_' && name[7] == '_' &&
			isDigits(name[0:4]) && isDigits(name[5:7]) && isDigits(name[8:10]) {
			folders = append(folders, filepath.Join(dir, name))
		}
	}
	return folders, nil
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
