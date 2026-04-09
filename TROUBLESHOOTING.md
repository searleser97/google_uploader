# Troubleshooting Guide

## OAuth / Authorization Issues

### "App hasn't completed Google verification process"

**Cause**: Your email isn't added as a test user in the OAuth consent screen.

**Solution**:
1. Go to https://console.cloud.google.com/
2. Select your project
3. Navigate to **APIs & Services** → **OAuth consent screen**
4. Scroll to **Test users** section
5. Click **+ ADD USERS**
6. Add your Gmail address
7. Click **SAVE**
8. Try authorizing again

**Alternative**: Click **PUBLISH APP** at the top of the OAuth consent screen (you'll see a warning, but it's safe for personal use).

---

### "Access blocked: This app's request is invalid"

**Cause**: OAuth consent screen not properly configured.

**Solution**:
1. Go to **OAuth consent screen** in Google Cloud Console
2. Make sure these are filled:
   - App name: (anything, e.g., "Google Uploader")
   - User support email: Your email
   - Developer contact: Your email
3. Click **SAVE AND CONTINUE** through all steps
4. Add your email as a test user (see above)

---

### "Error 401: Invalid Credentials"

**Cause**: Token expired or corrupted.

**Solution**:
```bash
rm token.json
./google_uploader /path/to/videos
```
Re-authorize when prompted.

---

## API Issues

### "Error 403: insufficientPermissions"

**Cause**: YouTube Data API v3 not enabled.

**Solution**:
1. Go to https://console.cloud.google.com/
2. Navigate to **APIs & Services** → **Library**
3. Search for "YouTube Data API v3"
4. Click **ENABLE**

---

### "Error 403: quotaExceeded"

**Cause**: Daily API quota limit reached (default: 10,000 units/day, ~6 videos).

**Solution**:
- Wait until tomorrow (quota resets at midnight Pacific Time)
- Or request quota increase: https://console.cloud.google.com/apis/api/youtube.googleapis.com/quotas

---

## File Issues

### "unable to read client secret file"

**Cause**: `client_secret.json` missing or in wrong location.

**Solution**:
1. Make sure the file is named exactly `client_secret.json`
2. Place it in the same directory as `google_uploader`
3. Verify: `ls -la client_secret.json`

---

### "No video files found"

**Cause**: No supported video formats in the folder.

**Supported formats**: mp4, mov, avi, mkv, flv, wmv, webm, m4v, mpg, mpeg, 3gp

**Solution**:
- Check your video file extensions
- Make sure you're pointing to the correct folder
- Verify: `ls /path/to/videos/*.mp4`

---

## Upload Issues

### Upload fails repeatedly

**Possible causes**:
1. **Internet connection** - Check your connection
2. **File too large** - YouTube has a 256GB/12 hour limit
3. **Unsupported format** - Convert to mp4
4. **Corrupted file** - Try playing the video locally first

**Solution**:
- Check the error message in the terminal
- Try uploading a small test video first
- Use `-resume` flag to continue after fixing the issue

---

### "Error uploading video: googleapi: Error 400: Bad Request"

**Cause**: Video metadata or file format issue.

**Solution**:
- Make sure video file is not corrupted
- Try with a different video file
- Check if filename has special characters (rename if needed)

---

## Build Issues

### "command not found: go"

**Cause**: Go not installed or not in PATH.

**Solution**:
```bash
# macOS with Homebrew
brew install go

# Or download from
# https://golang.org/dl/
```

Verify: `go version`

---

### "missing go.sum entry"

**Cause**: Dependencies not downloaded.

**Solution**:
```bash
go mod tidy
go build -o google_uploader
```

---

## Progress/Resume Issues

### Can't resume after interruption

**Cause**: `.upload_state.json` file missing or corrupted.

**Solution**:
- If the file exists but corrupted, delete it: `rm .upload_state.json`
- Start fresh without `-resume` flag
- The app will skip already uploaded videos automatically if they're still in your YouTube account

---

### Videos uploaded but not in playlist

**Cause**: Playlist creation or adding failed (but upload succeeded).

**Workaround**:
1. Find your uploaded videos in YouTube Studio
2. Manually add them to a playlist
3. Or fix the issue and re-run with `-resume` (won't re-upload, will add to playlist)

---

## Privacy/Visibility Issues

### Videos are private but I want them public

**Solution**: Edit `main.go` before building:

Line 290:
```go
PrivacyStatus: "public", // Changed from "private"
```

Line 212 (for playlist):
```go
PrivacyStatus: "public", // Changed from "private"
```

Then rebuild:
```bash
go build -o google_uploader
```

---

## Getting Help

If your issue isn't listed here:

1. **Check the error message** - It usually tells you what's wrong
2. **Check Google Cloud Console** - Make sure API is enabled and quota isn't exceeded
3. **Try with a test video** - Use a small, simple mp4 file
4. **Check the state file** - Look at `.upload_state.json` to see what was uploaded

### Useful Commands

```bash
# Check if Go is installed
go version

# Check if binary exists
ls -lh google_uploader

# Check if credentials exist
ls -la client_secret.json token.json

# Check current upload state
cat .upload_state.json

# Start fresh (removes all state)
rm token.json .upload_state.json

# Test with verbose errors (add -v flag if implemented)
./google_uploader /path/to/videos
```

### Common File Locations

- `client_secret.json` - Your OAuth credentials (download from Google Cloud)
- `token.json` - Auto-generated after first authorization
- `.upload_state.json` - Progress tracker (auto-deleted when complete)
- `google_uploader` - The compiled binary

### Project Structure

```
yt_uploader_tool/
├── google_uploader       # Binary (run this)
├── main.go              # Source code
├── go.mod               # Dependencies
├── go.sum               # Dependency checksums
├── client_secret.json   # Your OAuth credentials (YOU create this)
├── token.json           # Auto-generated OAuth token
├── .upload_state.json   # Temporary progress file
├── README.md            # Full documentation
├── SETUP.md             # Setup instructions
└── TROUBLESHOOTING.md   # This file
```
