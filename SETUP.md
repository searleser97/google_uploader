# Quick Setup Guide

Follow these steps to get the Google Uploader running:

## Step 1: Install Go

If you don't have Go installed:

### macOS (using Homebrew)
```bash
brew install go
```

### macOS/Linux (manual)
1. Download from https://golang.org/dl/
2. Extract and install
3. Add to PATH

### Verify installation
```bash
go version
```

## Step 2: Get YouTube API Credentials

### 2.1 Create/Select Google Cloud Project
1. Visit https://console.cloud.google.com/
2. Click "Select a project" → "New Project"
3. Name it (e.g., "Google Uploader")
4. Click "Create"

### 2.2 Enable YouTube Data API v3
1. In the Cloud Console, go to "APIs & Services" → "Library"
2. Search for "YouTube Data API v3"
3. Click on it and press "Enable"

### 2.3 Create OAuth2 Credentials
1. Go to "APIs & Services" → "Credentials"
2. Click "Create Credentials" → "OAuth client ID"
3. If prompted, configure the OAuth consent screen:
   - User Type: External
   - App name: Google Uploader
   - User support email: Your email
   - Developer contact: Your email
   - Click "Save and Continue"
   - Scopes: Skip this (click "Save and Continue")
   - Test users: Add your Google account email
   - Click "Save and Continue"
4. Back to "Create OAuth client ID":
   - Application type: **Desktop app**
   - Name: Google Uploader Client
   - Click "Create"
5. Download the JSON file
6. Rename it to `client_secret.json`
7. Move it to this project directory: `/Users/sergiosanc/dev/yt_uploader_tool/`

## Step 3: Build the Application

```bash
cd /Users/sergiosanc/dev/yt_uploader_tool
go mod download
go build -o google_uploader
```

## Step 4: First Run (Authorization)

```bash
./google_uploader /path/to/your/videos
```

The first time you run it:
1. A URL will be displayed in the terminal
2. Copy and open it in your browser
3. Sign in with your Google account
4. Grant the requested permissions
5. Copy the authorization code shown
6. Paste it back in the terminal
7. Press Enter

A `token.json` file will be created - you won't need to authorize again unless you delete this file.

## Step 5: Upload Videos

Now you're ready! Just run:

```bash
./google_uploader /path/to/your/videos
```

If something goes wrong and you need to resume:

```bash
./google_uploader -resume
```

## Common Issues

### "command not found: go"
Go is not installed or not in your PATH. Complete Step 1.

### "unable to read client secret file"
The `client_secret.json` file is missing or in the wrong location. It should be in `/Users/sergiosanc/dev/yt_uploader_tool/`

### "API has not been enabled"
You didn't enable the YouTube Data API v3. Go back to Step 2.2.

### "Access blocked: This app's request is invalid"
Your OAuth consent screen is not properly configured. Make sure to add your email as a test user in Step 2.3.

## Quick Test

1. Create a test folder with 1-2 small video files
2. Run: `./google_uploader /path/to/test/folder`
3. Check your YouTube account for the uploaded videos and playlist

## What Gets Created

After setup, your directory will contain:
- `google_uploader` - The compiled executable
- `client_secret.json` - Your API credentials (keep private!)
- `token.json` - Your OAuth token (keep private!)
- `.upload_state.json` - Temporary progress file (auto-deleted when complete)

## Tips

- Videos and playlists are set to **private** by default
- To change privacy or category, edit `main.go` before building (see README.md)
- You can upload ~6 videos per day with the default API quota
- Keep your credentials files secure and never share them
