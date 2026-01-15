# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Structure

This is a parent directory containing two related Go projects for managing Put.io downloads and Plex media:

- **local-dl**: Local download manager service that downloads files to local Plex directories
- **putio-go-server**: Discord bot and webhook service for Put.io/Plex integration

Both projects work together in a distributed architecture where `putio-go-server` manages Put.io transfers and Discord notifications, while `local-dl` handles the actual file downloads to Plex directories.

## Project: local-dl

**Purpose**: HTTP service that downloads files from Put.io to local Plex media directories with intelligent TV show organization.

### Key Architecture

- **Main file**: `main.go` (single file application)
- **Web framework**: Gin (HTTP server on configurable PORT)
- **Background workers**: Two goroutines (`Dequeue` and `GetQueue`) manage download queue
- **Concurrency model**: Max 3 concurrent downloads with mutex-protected job queues
- **TV Show Intelligence**:
  - Parses multiple episode formats: `S##E##`, `Season ## - ##`, `Show - ##`
  - Standardizes all filenames to format: `ShowName.SXXEXX.quality.ext`
  - Organizes files into `ShowName/Season_XX/` directory structure
  - Strips bracketed content from show names (e.g., `[Fansub]`, `[1080p]`)
  - Preserves quality/resolution information (720p, 1080p, codec details)
  - Normalizes show names to Title_Case with underscores (prevents duplicates)

### Required Environment Variables

```bash
MOVIES_PATH=/path/to/movies    # Where movie files are saved
TVSHOW_PATH=/path/to/tvshows   # Where TV show files are saved
PORT=8080                      # HTTP server port
CATALOG_DB=./tvshows_catalog.db # SQLite catalog database (optional, default shown)
```

### API Endpoints

**Download Management:**
- `POST /download` - Add new download to queue (requires JSON: url, type, name, file_id)
- `GET /queue` - View current queue and active downloads with progress
- `GET /ping` - Health check

**Catalog Management:**
- `GET /catalog/stats` - Get catalog statistics (total shows, episodes, size)
- `POST /catalog/scan` - Trigger full catalog scan
- `GET /catalog/search?show=X&season=Y&episode=Z` - Search catalog

### Catalog System

The catalog system automatically tracks all TV show files in a SQLite database:

**Features:**
- Automatically catalogs files when downloads complete
- Tracks: filename, show name, season, episode, file size, dates, status
- Detects additions, deletions, modifications
- Initial scan on startup
- Recursive directory scanning
- Event history logging
- **Syncs to putio-go-server:** Sends complete catalog every 5 minutes + immediately on new additions

**Database Location:** `./tvshows_catalog.db` (or `$CATALOG_DB`)

**Catalog Sync:**
- Sends catalog to `https://putio.bramsoft.com/catalogUpdate`
- Includes: all shows, seasons, episodes, statistics, recent additions
- Syncs every 5 minutes (with queue polling)
- **Debounced sync** when new files are added (10 second delay to batch changes)
- Single sync after initial scan completes (prevents overwhelming server)
- **Mutex-protected** to prevent concurrent syncs
- Non-blocking background operation

**Bidirectional Status Communication:**
- local-dl polls `/queue` endpoint every 5 minutes
- putio-go-server includes `catalog_status` in `/queue` response
- If `needs_resync` flag is set, local-dl triggers immediate catalog sync
- Discord command `!resync` sets the flag
- Flag auto-clears after successful catalog update

**Database Schema (tvshows_catalog.db):**
```sql
-- Files table: All TV show files
CREATE TABLE files (
    id INTEGER PRIMARY KEY,
    file_path TEXT UNIQUE NOT NULL,
    filename TEXT NOT NULL,
    show_name TEXT,
    season TEXT,
    episode TEXT,
    file_size INTEGER,
    created_at DATETIME,
    modified_at DATETIME,
    added_to_catalog DATETIME,
    last_seen_at DATETIME,
    status TEXT DEFAULT 'active'  -- 'active', 'deleted', 'moved'
);

-- Catalog events: History of all changes
CREATE TABLE catalog_events (
    id INTEGER PRIMARY KEY,
    event_type TEXT NOT NULL,     -- 'added', 'updated', 'deleted'
    file_path TEXT NOT NULL,
    show_name TEXT,
    season TEXT,
    episode TEXT,
    timestamp DATETIME,
    details TEXT
);
```

**CLI Tool:** Use `/Users/bharat/putio/catalog-cli.sh` for command-line queries:
```bash
./catalog-cli.sh stats                    # Show statistics
./catalog-cli.sh search "Breaking Bad"    # Search shows
./catalog-cli.sh show "Breaking_Bad" 5    # Show season 5 episodes
./catalog-cli.sh recent 50                # Show 50 recently added
./catalog-cli.sh missing                  # Find missing episodes
./catalog-cli.sh query "SELECT ..."       # Custom SQL query
```

**Catalog Sync Architecture:**

```
┌─────────────────────────────────────────────────────────────────┐
│ local-dl (Source)                                               │
├─────────────────────────────────────────────────────────────────┤
│ 1. Download completes → AddFileToCatalog()                      │
│    ├─ Updates tvshows_catalog.db                                │
│    └─ Triggers DebouncedCatalogSync() (10s delay)               │
│                                                                  │
│ 2. Every 5 minutes (with GetQueue poll)                         │
│    ├─ Polls https://putio.bramsoft.com/queue                    │
│    ├─ Receives: {items: [...], catalog_status: {...}}           │
│    ├─ Checks catalog_status.needs_resync flag                   │
│    └─ If true: Triggers immediate SendCatalogUpdate()           │
│                                                                  │
│ SendCatalogUpdate() sends:                                      │
│    ├─ Statistics (total shows, episodes, size)                  │
│    ├─ All shows with complete season/episode data               │
│    └─ 50 most recently added episodes                           │
│                                                                  │
│         │                                                        │
│         │ HTTP POST to /catalogUpdate                           │
│         ▼                                                        │
│                                                                  │
│ putio-go-server (Consumer)                                      │
├─────────────────────────────────────────────────────────────────┤
│ Receives catalog update → UpdateCatalog()                       │
│    ├─ Stores in library_catalog.db                              │
│    ├─ Replaces all data (transaction-based)                     │
│    ├─ Clears needs_resync flag                                  │
│    └─ Ready for Discord queries                                 │
│                                                                  │
│ Discord Commands:                                               │
│    ├─ !library  → Query library_catalog.db                      │
│    ├─ !shows    → Query library_catalog.db                      │
│    ├─ !show X   → Query library_catalog.db                      │
│    ├─ !recent   → Query library_catalog.db                      │
│    └─ !resync   → Sets needs_resync flag (polled by local-dl)   │
│                                                                  │
│ GET /queue response includes catalog status:                    │
│    ├─ needs_resync: true/false                                  │
│    ├─ resync_reason: "user_requested" / etc.                    │
│    ├─ show_count, episode_count                                 │
│    └─ last_updated timestamp                                    │
└─────────────────────────────────────────────────────────────────┘
```

**Sync Payload Structure:**
```json
{
  "timestamp": "2025-01-15T10:30:00Z",
  "statistics": {
    "total_shows": 142,
    "total_episodes": 3847,
    "total_size_gb": 2847.32,
    "total_size_bytes": 3057234567890
  },
  "shows": [
    {
      "show_name": "The_Bear",
      "seasons": [
        {
          "season": "01",
          "episodes": [
            {
              "episode": "01",
              "filename": "The_Bear.S01E01.1080p.mkv",
              "file_path": "/media/tvshows/The_Bear/Season_01/The_Bear.S01E01.1080p.mkv",
              "file_size": 2147483648,
              "modified_at": "2025-01-15 09:00:00"
            }
          ]
        }
      ]
    }
  ],
  "recent": [
    {
      "show_name": "The_Bear",
      "season": "01",
      "episode": "01",
      "filename": "The_Bear.S01E01.1080p.mkv",
      "added_to_catalog": "2025-01-15 09:00:00"
    }
  ]
}
```

### Common Commands

```bash
# Build and run locally
cd local-dl
go mod download
go run main.go

# Build Docker image (CGO enabled for sqlite3)
docker build -t local-dl .

# Run with Docker (requires volume mounts for media directories)
docker run -e MOVIES_PATH=/media/movies \
           -e TVSHOW_PATH=/media/tvshows \
           -e PORT=8080 \
           -v /path/to/movies:/media/movies \
           -v /path/to/tvshows:/media/tvshows \
           -v /path/to/catalog:/go/src \
           -p 8080:8080 local-dl

# Note: Volume mount for catalog recommended to persist tvshows_catalog.db
```

### Download Flow

1. `GetQueue` polls `https://putio.bramsoft.com/queue` every 5 minutes for new items
2. Items added to `Jobs` slice (pending queue)
3. `Dequeue` worker checks every 30 seconds, processes up to 3 concurrent downloads
4. Downloads organized by content type (movie vs tvshow) with TV shows auto-sorted into season folders
5. Progress tracked and sent to `https://putio.bramsoft.com/updateQueue`
6. On completion, webhook sent to `https://putio.bramsoft.com/update`

### ContentType Detection

- **TVShow**: Filename matches one of three patterns (case insensitive):
  - `S##E##` - Standard format (e.g., `The.Office.S02E03.720p.mkv`)
  - `Season ## - ##` - Spelled out format (e.g., `[Anime Time] Mob Psycho 100 Season 02 - 01.mkv`)
  - `Show - ##` - Simple format, implies Season 01 (e.g., `[Fansub] Show Name - 05.mkv`)
- **Movies**: Everything else
- **Filename Standardization**: All TV show files are automatically renamed to `ShowName.SXXEXX.quality.ext` format

## Project: putio-go-server

**Purpose**: Discord bot and webhook orchestrator that manages Put.io transfers, Plex webhooks, and Discord notifications.

### Key Architecture

- **Core controller**: `AppController` struct manages all state (Put.io client, Discord session, job maps)
- **Web framework**: Gin (HTTP server on port 8080)
- **Discord framework**: discordgo + dgrouter for command routing
- **Put.io SDK**: Official `go-putio` client library

### File Organization

- `main.go` - Application initialization and routing setup
- `BotHandlers.go` - Discord bot commands (plex, dl, queue, library commands)
- `WebHandlers.go` - HTTP handlers for Put.io callbacks and Plex integration
- `Utils.go` - Helper functions for content type detection, URL generation, directory handling
- `plex-webhook.go` - Plex webhook event handling (play, pause, resume, stop, scrobble)
- `plex-client.go` - Plex API client for querying show/episode metadata
- `catalog.go` - Library catalog database functions
- `Discord.go` - Discord-related utilities (currently minimal)

### Required Environment Variables

```bash
PUTIO_OAUTH=<your_putio_token>     # Put.io OAuth token
DISCORD_TOKEN=<your_bot_token>     # Discord bot token
%=<command_prefix>                 # Discord command prefix (e.g., "!")

# Optional: Plex API integration (for episode titles/summaries in !show command)
PLEX_URL=http://plex.server:32400  # Plex server URL
PLEX_TOKEN=<your_plex_token>       # Plex authentication token
```

### Common Commands

```bash
# Build and run locally (requires CGO for sqlite3)
cd putio-go-server
go mod download
CGO_ENABLED=1 go build -o putio-go-server *.go
./putio-go-server

# Or run directly
CGO_ENABLED=1 go run *.go

# Build Docker image (CGO enabled for sqlite3)
docker build -t putio-go-server .

# Run with Docker
docker run -e PUTIO_OAUTH=token \
           -e DISCORD_TOKEN=token \
           -v /path/to/data:/data \
           -p 8080:8080 putio-go-server

# Note: Volume mount recommended to persist library_catalog.db
```

**Important:** The binary MUST be compiled with `CGO_ENABLED=1` to support sqlite3. If you see errors like "Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work", rebuild with CGO enabled.

### Discord Bot Commands

All commands use configured prefix (e.g., `%plex <link>`):

**Download Commands:**
- `plex <magnet/torrent link>` - Download to Plex (triggers local-dl download after Put.io transfer)
- `dl <magnet/torrent link>` - Download and get direct link (not sent to Plex)
- `queue` or `status` - Show current download queue with progress

**Library Commands:**
- `library` - Show overall library statistics (total shows, episodes, size)
- `shows` - List all TV shows in your library (up to 50)
- `show <name>` - Show all episodes for a specific show (e.g., `!show The Flash`)
- `recent` - Show 10 most recently added episodes
- `resync` - Request immediate catalog resync from local-dl (triggers on next poll within 5 minutes)

**Note:** Library commands accept natural names with spaces. Examples:
- `!show The Flash` ✓
- `!show Flash` ✓ (fuzzy search)
- Shows are displayed with spaces: "The Bear" not "The_Bear"

**Other:**
- `help` - List all commands

### HTTP Endpoints

- `POST /dl` - Put.io transfer completion callback (triggers local-dl download)
- `POST /plex` - Plex webhook receiver (media events: play, pause, resume, stop, scrobble, library.new)
- `POST /queue` - Returns items not yet in local-dl queue
- `POST /updateQueue` - Update item status from local-dl
- `POST /update` - Send Discord notification when item added to Plex
- `POST /catalogUpdate` - Receives catalog updates from local-dl (every 5 min + on new additions)
- `GET /focus/:location` - Time-based focus score endpoint (1-4 based on hour)

### Library Catalog System

putio-go-server maintains a SQLite database (`./library_catalog.db`) of all TV shows:

**Database Schema (library_catalog.db):**
```sql
-- Statistics table: Overall library stats
CREATE TABLE catalog_stats (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    total_shows INTEGER,
    total_episodes INTEGER,
    total_size_gb REAL,
    total_size_bytes INTEGER,
    last_updated DATETIME
);

-- Shows table: All TV shows
CREATE TABLE shows (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    show_name TEXT UNIQUE NOT NULL,
    episode_count INTEGER DEFAULT 0,
    last_updated DATETIME
);

-- Episodes table: Complete episode database
CREATE TABLE episodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    show_name TEXT NOT NULL,
    season TEXT NOT NULL,
    episode TEXT NOT NULL,
    filename TEXT NOT NULL,
    file_path TEXT UNIQUE NOT NULL,
    file_size INTEGER,
    modified_at DATETIME,
    added_to_catalog DATETIME,
    UNIQUE(show_name, season, episode)
);

-- Indexes for fast queries
CREATE INDEX idx_show_name ON episodes(show_name);
CREATE INDEX idx_season ON episodes(season);
CREATE INDEX idx_added ON episodes(added_to_catalog);
```

**Data Source:**
- Receives updates from local-dl via POST `/catalogUpdate`
- Updates every 5 minutes (with queue polling)
- Updates immediately when new files are added
- Complete replacement strategy: deletes all data and re-inserts (transaction-based)

**Discord Integration:**
- All library commands query the local database (fast responses)
- No need to hit local-dl for library queries
- Shows are displayed with episode counts and season info

**Natural Name Support:**

The library system supports natural names with spaces for better UX:

```go
// Display format: "The Bear" (with spaces)
func formatShowName(dbName string) string {
    return strings.ReplaceAll(dbName, "_", " ")
}

// Storage format: "The_Bear" (with underscores)
func normalizeShowName(input string) string {
    normalized := strings.ReplaceAll(input, " ", "_")
    return toTitleCase(normalized)  // Title_Case
}

// Fuzzy search: finds partial matches
func SearchShows(query string) ([]map[string]interface{}, error) {
    // WHERE show_name LIKE %query%
}
```

**Discord Command Examples:**

```
User: !library
Bot: 📚 **Library Statistics**
     🎬 Total Shows: **142**
     📺 Total Episodes: **3,847**
     💾 Total Size: **2847.32 GB**
     🕐 Last Updated: 2025-01-15 10:30:00

User: !shows
Bot: 📺 **TV Shows** (142 total)

     • The Bear (18 episodes)
     • The Flash (184 episodes)
     • Breaking Bad (62 episodes)
     • Better Call Saul (63 episodes)
     • The Office (201 episodes)
     ...and 137 more shows

User: !show The Flash
Bot: 📺 **The Flash** (184 episodes)

     **Season 01** (23 episodes):
       • E01
       • E02
       • E03
       ...
       • E23

     **Season 02** (23 episodes):
       • E01
       ...

     ...and 6 more seasons

User: !show flash
Bot: 📺 **The Flash** (184 episodes)
     [fuzzy match works]

User: !show TheFlash
Bot: 📺 No exact match for **Theflash**

     Did you mean:
     • The Flash
     • The Flash (2014)

User: !recent
Bot: 📺 **Recently Added Episodes**

     1. The Bear S03E10
        Added: 2025-01-15 09:45:23
     2. Breaking Bad S05E16
        Added: 2025-01-15 08:32:11
     3. Better Call Saul S06E13
        Added: 2025-01-14 22:15:47
     ...
```

**Query Functions:**

- `GetLibraryStats()` - Returns total shows, episodes, size, last update
- `GetAllShows()` - Returns all shows sorted by name with episode counts
- `GetShowEpisodes(showName)` - Returns all episodes for a specific show (exact match)
- `SearchShows(query)` - Fuzzy search for shows (LIKE %query%), returns top 10 matches
- `GetRecentEpisodes(limit)` - Returns N most recently added episodes (ordered by added_to_catalog DESC)

### Plex API Integration

The bot can optionally connect to your Plex server to display rich episode information:

**Setup:**
- Set `PLEX_URL` environment variable (e.g., `http://192.168.1.100:32400`)
- Set `PLEX_TOKEN` environment variable (get from Plex: Settings → Account → Get Token)
- If not configured, the `!show` command falls back to catalog-only display

**Features:**
- Automatic show search by name in Plex library
- Episode titles and summaries from Plex metadata
- Displays up to 5 episodes per season with titles
- Falls back to catalog data if Plex is unavailable

**Example Output with Plex:**
```
User: !show 30 Rock
Bot: 📺 **30 Rock** (104 episodes)

     **Season 2** (15 episodes):
       E01: SeinfeldVision
       E02: Jack Gets in the Game
       E03: The Collection
       E04: Rosemary's Baby
       E05: Greenzo
       ...and 10 more episodes

     **Season 3** (22 episodes):
       E01: Do-Over
       E02: Believe in the Stars
       E03: The One with the Cast of Night Court
       E04: Gavin Volure
       E05: Reunion
       ...and 17 more episodes
```

**Plex Client Functions:**
- `InitPlexClient(url, token)` - Initialize Plex connection on startup
- `GetShowInfoFromPlex(showName)` - Query Plex for complete show/episode metadata
- Returns episode numbers, titles, and summaries for all seasons

**Implementation:**
- Uses `jrudio/go-plex-client` library
- Searches Plex TV Shows library for matching show
- Retrieves all seasons and episodes with metadata
- Gracefully degrades to catalog-only if Plex is unavailable

### Data Flow: Plex Downloads

1. User runs `%plex <magnet>` in Discord
2. Bot adds transfer to Put.io with callback URL `https://putio.bramsoft.com/dl`
3. When Put.io transfer completes, callback hits `/dl` endpoint
4. If file is directory: recursively processes all files >1MB
5. Generates download URLs and sends to local-dl service via `ac.send(item)`
6. Files auto-deleted from Put.io after 12 hours (43200 seconds)

### Discord Notifications

- Hardcoded channel: `1119807724868337724`
- Events: Plex play/pause/resume/stop/scrobble, new library items, download updates
- Uses rich embeds with show/movie metadata from Plex webhooks

### Content Type Detection

Uses regex pattern: `.*S[0-9]+E[0-9]+.*|.*s[0-9]+e[0-9]+.*|.*[0-9]+x[0-9]+.*|.* - [0-9].*`
- Matches → TVShow
- No match → Movies

## Integration Between Projects

**putio-go-server** → **local-dl**:
- Sends download jobs to local-dl's `/download` endpoint
- Polls local-dl's `/queue` endpoint (not documented in local-dl but implied)
- Receives status updates via `/updateQueue` endpoint

**local-dl** → **putio-go-server**:
- Sends completion webhooks to `/update` endpoint
- Sends progress updates to `/updateQueue` endpoint

## Additional Scripts

### organize-media.sh

**Purpose**: Standalone bash script to organize existing TV show files into the standardized structure.

**Location**: `/Users/bharat/putio/organize-media.sh`

**Features**:
- Scans directories for video files (mkv, mp4, avi, mov, etc.)
- Supports all three episode formats (S##E##, Season ## - ##, Show - ##)
- Standardizes filenames to `ShowName.SXXEXX.quality.ext` format
- Organizes files into `ShowName/Season_XX/` structure
- Dry-run mode by default (preview changes before applying)
- Can move or copy files
- Recursive directory scanning

**Usage**:
```bash
# Preview changes (dry-run)
./organize-media.sh -s ~/Downloads -d /media/tvshows

# Move files to organized structure
./organize-media.sh -s ~/Downloads -d /media/tvshows -m move

# Copy files recursively
./organize-media.sh -s ~/Downloads -d /media/tvshows -m copy -r
```

See `ORGANIZE-MEDIA.md` for detailed documentation.

### consolidate-shows.sh

**Purpose**: Merges duplicate TV show directories created by old code (before underscores/Title_Case normalization).

**Location**: `/Users/bharat/putio/consolidate-shows.sh`

**Features**:
- Finds and merges duplicate directories (spaces vs underscores, case differences)
- Moves all files to the canonical version (underscores + Title_Case)
- Dry-run mode by default
- Safe handling of existing files (won't overwrite)

**Usage**:
```bash
# Preview consolidation (dry-run)
./consolidate-shows.sh -d /media/TVShows

# Actually consolidate
./consolidate-shows.sh -d /media/TVShows -x
```

**Example**: Merges "The Bear", "the bear", "the_bear", and "The_Bear" into single directory "The_Bear".

## Testing

Both projects lack automated tests. When adding tests:

```bash
# Run tests for specific project
cd local-dl  # or putio-go-server
go test ./... -v

# Run with coverage
go test ./... -cover
```

## Important Notes

- Both services expect external URL `https://putio.bramsoft.com` to be accessible
- Discord channel ID and webhook URLs are hardcoded (see WebHandlers.go:89, main.go:112)
- File deletion logic in putio-go-server runs every hour, deletes files older than 12 hours
- Local-dl retries failed downloads by re-queueing
- Both services use panic recovery to restart background workers
- TV show parsing supports three formats: `S01E02`, `Season 02 - 01`, and `Show - 01` (all case insensitive)
- All TV show filenames are standardized to `ShowName.SXXEXX.quality.ext` format
- Show names are normalized to Title_Case with underscores to prevent duplicates
- **CGO Required**: Both local-dl and putio-go-server MUST be compiled with `CGO_ENABLED=1` for sqlite3 support

## Troubleshooting

### Catalog System Issues

**Problem:** Discord bot commands return "Binary was compiled with 'CGO_ENABLED=0'"
```bash
# Solution: Rebuild with CGO enabled
cd putio-go-server
CGO_ENABLED=1 go build -o putio-go-server *.go
# Restart the service
```

**Problem:** No catalog data appearing in Discord bot
```bash
# Check if catalog is syncing from local-dl
cd local-dl
curl http://localhost:8080/catalog/stats

# Check if putio-go-server received the data
# Look for log entry: "[Catalog] Updated: X shows, Y episodes"

# Manually trigger catalog sync
curl -X POST http://localhost:8080/catalog/scan
```

**Problem:** Shows appear duplicated (spaces vs underscores)
```bash
# Use consolidate-shows.sh to merge duplicates
./consolidate-shows.sh -d /path/to/TVShows -x
```

**Problem:** Catalog database is corrupted
```bash
# local-dl: Delete and reinitialize
rm ./tvshows_catalog.db
# Restart local-dl - it will recreate and scan

# putio-go-server: Delete and wait for next sync
rm ./library_catalog.db
# Restart putio-go-server - it will recreate on next catalog update
```

**Problem:** Catalog not updating when new files are added
```bash
# Check if AddFileToCatalog() is being called in local-dl
# Look for log entry: "[Catalog] Added file to catalog: ..."

# Check if sync is sending to putio-go-server
# Look for log entry: "[CatalogSync] Catalog update sent successfully"

# Verify endpoint is accessible
curl -X POST https://putio.bramsoft.com/catalogUpdate \
  -H "Content-Type: application/json" \
  -d '{"timestamp":"2025-01-15T00:00:00Z","statistics":{"total_shows":0,"total_episodes":0,"total_size_gb":0,"total_size_bytes":0},"shows":[],"recent":[]}'
```

**Problem:** putio-go-server overwhelmed with 500 errors on `/catalogUpdate`
```bash
# This happens when local-dl restarts and scans many episodes at once
# Symptoms: 100+ concurrent POST /catalogUpdate requests, 15+ second response times

# The fix (already implemented):
# - Catalog sync is debounced (10 second delay to batch changes)
# - Mutex prevents concurrent syncs
# - Initial scan doesn't trigger immediate syncs
# - One sync sent after initial scan completes

# To resolve:
# 1. Wait for local-dl initial scan to complete
# 2. Wait for the single catalog update to finish
# 3. Server will recover automatically

# If server is stuck, restart putio-go-server to clear pending requests
```

### Build Issues

**Problem:** `go-sqlite3` installation fails
```bash
# Ensure CGO is enabled and gcc is installed
# macOS:
xcode-select --install

# Linux:
sudo apt-get install gcc

# Then rebuild
CGO_ENABLED=1 go build
```

**Problem:** Binary doesn't work on different architecture
```bash
# Cross-compilation with CGO requires specific setup
# For example, building linux/amd64 on macOS:
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o putio-go-server *.go
# Note: Cross-compilation with CGO is complex and may require additional tools
```

**Problem:** Docker build fails or sqlite3 doesn't work in container
```bash
# Ensure Dockerfile has CGO_ENABLED=1 and gcc installed
# Both Dockerfiles are configured for sqlite3 support:
# - CGO_ENABLED=1 during build
# - gcc and libc6-dev installed in build stage
# - Ubuntu base image (not scratch) for runtime with C libraries

# Rebuild Docker images
cd local-dl && docker build -t local-dl .
cd putio-go-server && docker build -t putio-go-server .
```
