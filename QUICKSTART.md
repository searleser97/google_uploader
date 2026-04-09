# Quick Start Guide

## Installation (One-Time)

```bash
# 1. Install Go (if not installed)
brew install go  # macOS with Homebrew
# OR download from https://golang.org/dl/

# 2. Navigate to project
cd /Users/sergiosanc/dev/yt_uploader_tool

# 3. Download dependencies and build
go mod download
go build -o google_uploader
```

## Get API Credentials (One-Time)

1. Go to https://console.cloud.google.com/
2. Create a new project
3. Enable "YouTube Data API v3"
4. Create OAuth2 Desktop credentials
5. Download JSON and save as `client_secret.json` in this directory

See SETUP.md for detailed instructions.

## Usage

### Upload videos from a folder
```bash
./google_uploader /path/to/videos
```

### Resume interrupted upload
```bash
./google_uploader -resume
```

## What It Does

1. ✅ Scans folder for video files
2. ✅ Creates a YouTube playlist (named after the folder)
3. ✅ Uploads each video (with retry on failure)
4. ✅ Adds all videos to the playlist
5. ✅ Saves progress after each upload
6. ✅ Can resume if interrupted

## Default Settings

- **Privacy**: Private (videos and playlist)
- **Category**: People & Blogs
- **Retry**: 3 attempts per video
- **Retry Delay**: 5 seconds

To change these, edit `main.go` before building (see README.md).

## Command Reference

| Command | Description |
|---------|-------------|
| `./google_uploader /path/to/videos` | Upload videos from folder |
| `./google_uploader -resume` | Resume from previous session |
| `./google_uploader -h` | Show help |

## Files

| File | Purpose |
|------|---------|
| `client_secret.json` | API credentials (you provide) |
| `token.json` | OAuth token (auto-generated) |
| `.upload_state.json` | Progress tracking (auto-deleted when done) |

## Supported Video Formats

mp4, mov, avi, mkv, flv, wmv, webm, m4v, mpg, mpeg, 3gp

## Need Help?

- Detailed setup: See `SETUP.md`
- Full documentation: See `README.md`
- Troubleshooting: See README.md → "Troubleshooting" section
