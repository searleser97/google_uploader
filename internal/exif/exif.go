package exif

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	goexif "github.com/rwcarlsen/goexif/exif"
)

var dateLayouts = []string{
	"2006:01:02 15:04:05-07:00",
	"2006:01:02 15:04:05",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

// Formats that goexif can read natively
var goexifExtensions = map[string]bool{
	".jpg": true, ".jpeg": true,
	".tiff": true, ".tif": true,
	".cr2": true, ".nef": true, ".dng": true,
}

var hasExiftool *bool

func exiftoolAvailable() bool {
	if hasExiftool == nil {
		_, err := exec.LookPath("exiftool")
		result := err == nil
		hasExiftool = &result
	}
	return *hasExiftool
}

// GetCreationDate reads the creation date from a file.
// Tries native Go EXIF for supported formats, falls back to exiftool,
// then to file modification time.
func GetCreationDate(filePath string) (time.Time, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	// Try native Go EXIF for supported formats
	if goexifExtensions[ext] {
		t, err := getExifDate(filePath)
		if err == nil {
			return t, nil
		}
	}

	// Try exiftool for broader format support (HEIC, MOV, MP4, PNG, etc.)
	if exiftoolAvailable() {
		t, err := getExiftoolDate(filePath)
		if err == nil {
			return t, nil
		}
	}

	// Fallback: file modification time
	info, err := os.Stat(filePath)
	if err != nil {
		return time.Time{}, fmt.Errorf("unable to get file info: %w", err)
	}
	return info.ModTime(), nil
}

func getExifDate(filePath string) (time.Time, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()

	x, err := goexif.Decode(f)
	if err != nil {
		return time.Time{}, fmt.Errorf("no EXIF data: %w", err)
	}

	t, err := x.DateTime()
	if err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("no date in EXIF: %w", err)
}

// getExiftoolDate shells out to exiftool with the same fallback chain
// as the original JS scripts: CreateDate, DateTimeOriginal, FileModifyDate
func getExiftoolDate(filePath string) (time.Time, error) {
	out, err := exec.Command("exiftool", "-s3", "-CreateDate", "-DateTimeOriginal", "-FileModifyDate", filePath).Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("exiftool failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "0000:00:00 00:00:00" {
			continue
		}
		t, err := ParseDateString(line)
		if err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("no valid date from exiftool")
}

func FormatAsFolder(t time.Time) string {
	return t.Format("2006_01_02")
}

func FormatAsDateTimeSuffix(t time.Time) string {
	return t.Format("20060102_150405")
}

func FormatForDisplay(t time.Time) string {
	return t.Format("2006:01:02 15:04:05")
}

// ParseDateString parses a date string from EXIF-like formats.
// Tries timezone-aware layouts first, then strips timezone and retries.
func ParseDateString(s string) (time.Time, error) {
	s = strings.TrimSpace(s)

	// First pass: try parsing the full string (preserves timezone)
	for _, layout := range dateLayouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t, nil
		}
	}

	// Second pass: strip timezone suffix and retry
	if len(s) > 19 {
		cleaned := s[:19]
		for _, layout := range dateLayouts {
			t, err := time.Parse(layout, cleaned)
			if err == nil {
				return t, nil
			}
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse date: %s", s)
}
