# Google Uploader

A command-line tool written in Go that automatically uploads media from one or more folders to Google services — photos go to Google Photos (with albums) and videos go to YouTube (with playlists). Includes robust error handling and resume capability.

## Features

- 📁 Scans one or more folders for media files
- 📷 Uploads photos to Google Photos using the Photos Library API (supports jpg, jpeg, png, gif, bmp, webp, heic, heif, tiff, raw, ico)
- 🎬 Uploads videos to YouTube using the YouTube Data API v3 (supports mp4, mov, avi, mkv, flv, wmv, webm, m4v, mpg, mpeg, 3gp)
- 📝 Automatically creates an album (Google Photos) and playlist (YouTube) per folder
- 🔀 Auto-detects file types and routes to the correct service
- ⚡ Two-pass upload: photos first, then videos (optimizes API connections and quota)
- 💾 Saves progress after each upload (resume from where you left off)
- 🔄 Automatic retry logic for failed uploads (3 attempts with delays)
- 📊 Real-time progress tracking
- 🔐 OAuth2 authentication (one-time setup)

## Prerequisites

### 1. Install Go

Download and install Go from [golang.org](https://golang.org/dl/)

Verify installation:
```bash
go version
```

### 2. Set up Google API Credentials

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project or select an existing one
3. Enable the **YouTube Data API v3**:
   - Navigate to "APIs & Services" > "Library"
   - Search for "YouTube Data API v3"
   - Click "Enable"
4. Enable the **Photos Library API**:
   - In the same Library page, search for "Photos Library API"
   - Click "Enable"
5. Create OAuth2 credentials:
   - Go to "APIs & Services" > "Credentials"
   - Click "Create Credentials" > "OAuth client ID"
   - Choose "Desktop app" as the application type
   - Download the JSON file
   - Save it as `client_secret.json` in the project directory

> **Note**: If you previously used this tool for YouTube only, delete `token.json` and re-authorize to grant the new Google Photos scope.

## Installation

1. Clone or navigate to this directory:
```bash
cd /Users/sergiosanc/dev/yt_uploader_tool
```

2. Download dependencies:
```bash
go mod download
```

3. Build the application:
```bash
go build -o google_uploader
```

## Usage

### First Time Setup

On first run, you'll need to authorize the application:

```bash
./google_uploader /path/to/your/videos
```

The application will:
1. Open a browser authorization URL
2. Ask you to grant permissions
3. Provide an authorization code
4. Save the token for future use (stored in `token.json`)

### Basic Usage

Upload all videos from a single folder:
```bash
./google_uploader /path/to/your/videos
```

Upload videos from multiple folders (each folder gets its own playlist):
```bash
./google_uploader /path/to/folder1 /path/to/folder2 /path/to/folder3
```

### Resume Interrupted Upload

If an upload is interrupted, resume from where it left off:
```bash
./google_uploader -resume
```

You can also add new folders while resuming:
```bash
./google_uploader -resume /path/to/new/folder
```

## Configuration

### Privacy Settings

By default, videos and playlists are set to **unlisted**. To change this, edit `main.go`:

**For videos** (line ~290):
```go
PrivacyStatus: "private", // Change to "public" or "unlisted"
```

**For playlists** (line ~212):
```go
PrivacyStatus: "private", // Change to "public" or "unlisted"
```

### Video Category

The default category is "People & Blogs" (ID: 22). Common category IDs:
- 1: Film & Animation
- 10: Music
- 17: Sports
- 20: Gaming
- 22: People & Blogs
- 23: Comedy
- 24: Entertainment
- 25: News & Politics
- 26: Howto & Style
- 27: Education
- 28: Science & Technology

Change it in `main.go` (line ~289):
```go
CategoryId: "22", // Change to your preferred category
```

### Retry Settings

Modify retry behavior in `main.go` (lines 24-25):
```go
maxRetries = 3
retryDelay = 5 * time.Second
```

## How It Works

1. **Scan**: Reads all media files from each specified folder, separating photos and videos
2. **Authenticate**: Uses OAuth2 to authenticate with both YouTube and Google Photos
3. **Photos Pass**: For each folder, uploads all photos to Google Photos and creates an album
4. **Videos Pass**: For each folder, uploads all videos to YouTube and creates a playlist
5. **Save State**: After each successful upload, saves progress to `.upload_state.json`
6. **Retry**: If an upload fails, retries up to 3 times before stopping
7. **Resume**: Can resume from the saved state if interrupted

## State Management

The application creates a `.upload_state.json` file that tracks:
- All folders being processed
- Per-folder: album ID (Photos), playlist ID (YouTube), uploaded files, progress
- Completion status per folder (photos pass and videos pass tracked separately)

This file allows you to resume uploads if interrupted. It's automatically deleted when all folders complete successfully.

## Error Handling

- **Network interruptions**: Automatic retry with exponential backoff
- **API errors**: Retries up to 3 times per operation
- **File errors**: Clear error messages with exit
- **Authentication errors**: Prompts for re-authorization if token is invalid

## Supported Formats

### Photo Formats (→ Google Photos)
jpg, jpeg, png, gif, bmp, webp, heic, heif, tiff, tif, raw, ico

### Video Formats (→ YouTube)
mp4, mov, avi, mkv, flv, wmv, webm, m4v, mpg, mpeg, 3gp

## Troubleshooting

### "command not found: go"
Install Go from [golang.org](https://golang.org/dl/)

### "unable to read client secret file"
Make sure `client_secret.json` is in the project directory

### "Error 403: insufficientPermissions"
Make sure you've enabled both the YouTube Data API v3 and the Photos Library API in Google Cloud Console

### "Error 401: Invalid Credentials"
Delete `token.json` and re-authorize the application

### Upload fails repeatedly
Check your internet connection and YouTube API quota limits

## API Quota Limits

### YouTube Data API v3
- Default quota: 10,000 units per day
- Upload cost: ~1,600 units per video
- You can upload approximately 6 videos per day with the default quota

### Google Photos Library API
- Default quota: 10,000 requests per day
- Each photo upload uses ~2 requests (upload bytes + create media item)
- You can upload approximately 5,000 photos per day

To increase quota, request a quota extension in Google Cloud Console.

## Files Created

- `token.json` - OAuth2 access token (keep this secure!)
- `.upload_state.json` - Upload progress state (temporary, auto-deleted on completion)

## Security Notes

- Keep `client_secret.json` and `token.json` private
- Never commit these files to version control
- Videos default to unlisted - change if needed
- Photos are uploaded at original quality and count against Google storage
- The application only requests necessary scopes (YouTube upload + Photos append-only)

## Example Output

```
=== Google Uploader ===
Processing 2 folder(s)
📷 Photos will be uploaded to Google Photos
🎬 Videos will be uploaded to YouTube

[Folder 1/2] vacation — 2 photo(s), 1 video(s)
  📷 Uploading photos to Google Photos...
  Created album: vacation (ID: ALBxxx...)
  ------------------------------------------------
  [1/2] Uploading: sunset.jpg
    Uploading sunset.jpg (3.45 MB)...
    ✓ Photo uploaded successfully! ID: media123
    Progress: 1/2 photos completed

  [2/2] Uploading: beach.png
    Uploading beach.png (5.12 MB)...
    ✓ Photo uploaded successfully! ID: media456
    Progress: 2/2 photos completed
  ✓ Photos complete for vacation
  🎬 Uploading videos to YouTube...
  Created playlist: vacation (ID: PLxxx...)
  ------------------------------------------------
  [1/1] Uploading: drone_flight.mp4
    Uploading drone_flight.mp4 (45.32 MB)...
    ✓ Video uploaded successfully! ID: abc123
    ✓ Added to playlist
    Progress: 1/1 videos completed
  ✓ Folder complete! Album ID: ALBxxx... Playlist: https://www.youtube.com/playlist?list=PLxxx...

[Folder 2/2] tutorials — 0 photo(s), 2 video(s)
  🎬 Uploading videos to YouTube...
  Created playlist: tutorials (ID: PLyyy...)
  ------------------------------------------------
  [1/2] Uploading: lesson1.mp4
    ✓ Video uploaded successfully! ID: def456
    ✓ Added to playlist
    Progress: 1/2 videos completed

  [2/2] Uploading: lesson2.mp4
    ✓ Video uploaded successfully! ID: ghi789
    ✓ Added to playlist
    Progress: 2/2 videos completed
  ✓ Folder complete! Playlist: https://www.youtube.com/playlist?list=PLyyy...

==================================================
✓ All uploads completed successfully!
Total photos uploaded: 2
Total videos uploaded: 3
  vacation: 2 photos (album: ALBxxx...) | 1 videos (playlist: https://www.youtube.com/playlist?list=PLxxx...)
  tutorials: 2 videos (playlist: https://www.youtube.com/playlist?list=PLyyy...)
```

## License

This project uses the official Google API client libraries for Go.

## References

- [YouTube Data API v3 Documentation](https://developers.google.com/youtube/v3)
- [Go API Client](https://pkg.go.dev/google.golang.org/api/youtube/v3)
- [OAuth2 Setup Guide](https://developers.google.com/youtube/v3/guides/authentication)
