package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Catalog database path
var CatalogDB *sql.DB
var CatalogPath = os.Getenv("CATALOG_DB")

// FileEntry represents a cataloged file
type FileEntry struct {
	ID              int64
	FilePath        string
	Filename        string
	MediaType       string // "tv" or "movie"
	ShowName        string // For TV shows
	Season          string // For TV shows
	Episode         string // For TV shows
	Title           string // For movies
	Year            string // For movies
	Quality         string // For movies (720p, 1080p, etc.)
	FileSize        int64
	CreatedAt       time.Time
	ModifiedAt      time.Time
	AddedToCatalog  time.Time
	LastSeenAt      time.Time
	Status          string // active, deleted, moved
}

// InitCatalog initializes the catalog database
func InitCatalog() error {
	if CatalogPath == "" {
		CatalogPath = "./tvshows_catalog.db"
	}

	var err error
	CatalogDB, err = sql.Open("sqlite3", CatalogPath)
	if err != nil {
		return fmt.Errorf("failed to open catalog database: %w", err)
	}

	// Create basic tables first (without new columns to support old databases)
	basicSchema := `
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_path TEXT UNIQUE NOT NULL,
		filename TEXT NOT NULL,
		show_name TEXT,
		season TEXT,
		episode TEXT,
		file_size INTEGER,
		created_at DATETIME,
		modified_at DATETIME,
		added_to_catalog DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		status TEXT DEFAULT 'active'
	);

	CREATE TABLE IF NOT EXISTS catalog_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_type TEXT NOT NULL,
		file_path TEXT NOT NULL,
		show_name TEXT,
		season TEXT,
		episode TEXT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		details TEXT
	);
	`

	_, err = CatalogDB.Exec(basicSchema)
	if err != nil {
		return fmt.Errorf("failed to create basic catalog schema: %w", err)
	}

	// Run migrations to add new columns for existing databases
	if err := migrateCatalogSchema(); err != nil {
		return fmt.Errorf("failed to migrate catalog schema: %w", err)
	}

	// Create indexes after migration ensures columns exist
	indexes := `
	CREATE INDEX IF NOT EXISTS idx_media_type ON files(media_type);
	CREATE INDEX IF NOT EXISTS idx_show_name ON files(show_name);
	CREATE INDEX IF NOT EXISTS idx_season ON files(season);
	CREATE INDEX IF NOT EXISTS idx_title ON files(title);
	CREATE INDEX IF NOT EXISTS idx_status ON files(status);
	CREATE INDEX IF NOT EXISTS idx_file_path ON files(file_path);
	`

	_, err = CatalogDB.Exec(indexes)
	if err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	logMessage(LogLevelInfo, "Catalog", "Catalog database initialized at %s", CatalogPath)
	return nil
}

// migrateCatalogSchema adds missing columns to existing databases
func migrateCatalogSchema() error {
	// Check if media_type column exists
	rows, err := CatalogDB.Query("PRAGMA table_info(files)")
	if err != nil {
		return fmt.Errorf("failed to check table schema: %w", err)
	}
	defer rows.Close()

	hasMediaType := false
	hasTitle := false
	hasYear := false
	hasQuality := false

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue interface{}

		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			continue
		}

		switch name {
		case "media_type":
			hasMediaType = true
		case "title":
			hasTitle = true
		case "year":
			hasYear = true
		case "quality":
			hasQuality = true
		}
	}

	// Add missing columns
	if !hasMediaType {
		logMessage(LogLevelInfo, "Catalog", "Adding media_type column to existing database")
		if _, err := CatalogDB.Exec("ALTER TABLE files ADD COLUMN media_type TEXT DEFAULT 'tv'"); err != nil {
			return fmt.Errorf("failed to add media_type column: %w", err)
		}
	}

	if !hasTitle {
		logMessage(LogLevelInfo, "Catalog", "Adding title column to existing database")
		if _, err := CatalogDB.Exec("ALTER TABLE files ADD COLUMN title TEXT"); err != nil {
			return fmt.Errorf("failed to add title column: %w", err)
		}
	}

	if !hasYear {
		logMessage(LogLevelInfo, "Catalog", "Adding year column to existing database")
		if _, err := CatalogDB.Exec("ALTER TABLE files ADD COLUMN year TEXT"); err != nil {
			return fmt.Errorf("failed to add year column: %w", err)
		}
	}

	if !hasQuality {
		logMessage(LogLevelInfo, "Catalog", "Adding quality column to existing database")
		if _, err := CatalogDB.Exec("ALTER TABLE files ADD COLUMN quality TEXT"); err != nil {
			return fmt.Errorf("failed to add quality column: %w", err)
		}
	}

	// Migrate catalog_events table as well
	eventRows, err := CatalogDB.Query("PRAGMA table_info(catalog_events)")
	if err != nil {
		return fmt.Errorf("failed to check catalog_events schema: %w", err)
	}
	defer eventRows.Close()

	hasEventMediaType := false
	hasEventTitle := false
	hasEventYear := false

	for eventRows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue interface{}

		if err := eventRows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			continue
		}

		switch name {
		case "media_type":
			hasEventMediaType = true
		case "title":
			hasEventTitle = true
		case "year":
			hasEventYear = true
		}
	}

	// Add missing columns to catalog_events
	if !hasEventMediaType {
		logMessage(LogLevelInfo, "Catalog", "Adding media_type column to catalog_events table")
		if _, err := CatalogDB.Exec("ALTER TABLE catalog_events ADD COLUMN media_type TEXT"); err != nil {
			return fmt.Errorf("failed to add media_type to catalog_events: %w", err)
		}
	}

	if !hasEventTitle {
		logMessage(LogLevelInfo, "Catalog", "Adding title column to catalog_events table")
		if _, err := CatalogDB.Exec("ALTER TABLE catalog_events ADD COLUMN title TEXT"); err != nil {
			return fmt.Errorf("failed to add title to catalog_events: %w", err)
		}
	}

	if !hasEventYear {
		logMessage(LogLevelInfo, "Catalog", "Adding year column to catalog_events table")
		if _, err := CatalogDB.Exec("ALTER TABLE catalog_events ADD COLUMN year TEXT"); err != nil {
			return fmt.Errorf("failed to add year to catalog_events: %w", err)
		}
	}

	return nil
}

// AddFileToCatalog adds or updates a file in the catalog
func AddFileToCatalog(filePath string) error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database not initialized")
	}

	// Get file info
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	filename := filepath.Base(filePath)

	// Determine if this is a TV show or movie based on path
	mediaType := "tv"
	isMoviePath := strings.Contains(filePath, MoviesPath)

	if isMoviePath {
		mediaType = "movie"
	}

	// Check if file already exists in catalog
	var existingID int64
	err = CatalogDB.QueryRow("SELECT id FROM files WHERE file_path = ?", filePath).Scan(&existingID)

	if err == sql.ErrNoRows {
		// New file - insert based on media type
		if mediaType == "movie" {
			// Parse movie info
			movieInfo := parseMovieInfo(filename)

			result, err := CatalogDB.Exec(`
				INSERT INTO files (file_path, filename, media_type, title, year, quality, file_size, created_at, modified_at, last_seen_at, status)
				VALUES (?, ?, 'movie', ?, ?, ?, ?, ?, ?, ?, 'active')
			`,
				filePath,
				filename,
				movieInfo.Title,
				movieInfo.Year,
				movieInfo.Quality,
				fileInfo.Size(),
				fileInfo.ModTime(),
				fileInfo.ModTime(),
				time.Now(),
			)

			if err != nil {
				return fmt.Errorf("failed to insert movie: %w", err)
			}

			_, _ = result.LastInsertId()

			// Log event
			logCatalogEvent("added", filePath, "movie", "", "", "", movieInfo.Title, movieInfo.Year, "")

			logMessage(LogLevelInfo, "Catalog", "Added movie: %s (%s) [%s]", movieInfo.Title, movieInfo.Year, movieInfo.Quality)

			// Schedule debounced catalog update (batches multiple changes)
			DebouncedCatalogSync()

			return nil
		} else {
			// Parse TV show info
			tvInfo := parseTVShowInfo(filename)

			result, err := CatalogDB.Exec(`
				INSERT INTO files (file_path, filename, media_type, show_name, season, episode, file_size, created_at, modified_at, last_seen_at, status)
				VALUES (?, ?, 'tv', ?, ?, ?, ?, ?, ?, ?, 'active')
			`,
				filePath,
				filename,
				tvInfo.ShowName,
				tvInfo.Season,
				tvInfo.Episode,
				fileInfo.Size(),
				fileInfo.ModTime(),
				fileInfo.ModTime(),
				time.Now(),
			)

			if err != nil {
				return fmt.Errorf("failed to insert TV show: %w", err)
			}

			_, _ = result.LastInsertId()

			// Log event
			logCatalogEvent("added", filePath, "tv", tvInfo.ShowName, tvInfo.Season, tvInfo.Episode, "", "", "")

			logMessage(LogLevelInfo, "Catalog", "Added TV show: %s S%sE%s", tvInfo.ShowName, tvInfo.Season, tvInfo.Episode)

			// Schedule debounced catalog update (batches multiple changes)
			DebouncedCatalogSync()

			return nil
		}
	} else if err != nil {
		return fmt.Errorf("failed to check existing file: %w", err)
	}

	// File exists - update based on media type
	if mediaType == "movie" {
		movieInfo := parseMovieInfo(filename)

		_, err = CatalogDB.Exec(`
			UPDATE files
			SET filename = ?, title = ?, year = ?, quality = ?, file_size = ?, modified_at = ?, last_seen_at = ?, status = 'active'
			WHERE id = ?
		`,
			filename,
			movieInfo.Title,
			movieInfo.Year,
			movieInfo.Quality,
			fileInfo.Size(),
			fileInfo.ModTime(),
			time.Now(),
			existingID,
		)

		if err != nil {
			return fmt.Errorf("failed to update movie: %w", err)
		}

		logCatalogEvent("updated", filePath, "movie", "", "", "", movieInfo.Title, movieInfo.Year, "")
		logMessage(LogLevelInfo, "Catalog", "Updated movie: %s", filename)
	} else {
		tvInfo := parseTVShowInfo(filename)

		_, err = CatalogDB.Exec(`
			UPDATE files
			SET filename = ?, show_name = ?, season = ?, episode = ?, file_size = ?, modified_at = ?, last_seen_at = ?, status = 'active'
			WHERE id = ?
		`,
			filename,
			tvInfo.ShowName,
			tvInfo.Season,
			tvInfo.Episode,
			fileInfo.Size(),
			fileInfo.ModTime(),
			time.Now(),
			existingID,
		)

		if err != nil {
			return fmt.Errorf("failed to update TV show: %w", err)
		}

		logCatalogEvent("updated", filePath, "tv", tvInfo.ShowName, tvInfo.Season, tvInfo.Episode, "", "", "")
		logMessage(LogLevelInfo, "Catalog", "Updated TV show: %s", filename)
	}

	return nil
}

// RemoveFileFromCatalog marks a file as deleted
func RemoveFileFromCatalog(filePath string) error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database not initialized")
	}

	// Get file info before marking as deleted
	var mediaType, showName, season, episode, title, year string
	err := CatalogDB.QueryRow("SELECT media_type, show_name, season, episode, title, year FROM files WHERE file_path = ?", filePath).Scan(&mediaType, &showName, &season, &episode, &title, &year)
	if err != nil {
		return err
	}

	_, err = CatalogDB.Exec("UPDATE files SET status = 'deleted', last_seen_at = ? WHERE file_path = ?", time.Now(), filePath)
	if err != nil {
		return fmt.Errorf("failed to mark file as deleted: %w", err)
	}

	logCatalogEvent("deleted", filePath, mediaType, showName, season, episode, title, year, "")
	logMessage(LogLevelInfo, "Catalog", "Marked as deleted: %s", filePath)

	return nil
}

// ScanAndUpdateCatalog scans the TV shows and movies directories and updates the catalog
func ScanAndUpdateCatalog() error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database not initialized")
	}

	if TVShowPath == "" && MoviesPath == "" {
		return fmt.Errorf("neither TVSHOW_PATH nor MOVIES_PATH is set")
	}

	logMessage(LogLevelInfo, "Catalog", "Starting catalog scan (TV: %s, Movies: %s)", TVShowPath, MoviesPath)

	// Get all files currently in catalog
	catalogedFiles := make(map[string]bool)
	rows, err := CatalogDB.Query("SELECT file_path FROM files WHERE status = 'active'")
	if err != nil {
		return fmt.Errorf("failed to query catalog: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err == nil {
			catalogedFiles[path] = false // Mark as not seen
		}
	}

	// Scan filesystem
	videoExtensions := map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
		".mov": true, ".wmv": true, ".flv": true, ".webm": true, ".ts": true,
	}

	addedCount := 0
	updatedCount := 0

	scanPath := func(basePath string) error {
		if basePath == "" {
			return nil
		}

		return filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip errors
			}

			if info.IsDir() {
				return nil
			}

			// Check if it's a video file
			ext := strings.ToLower(filepath.Ext(path))
			if !videoExtensions[ext] {
				return nil
			}

			// Add or update file in catalog
			if err := AddFileToCatalog(path); err == nil {
				if _, exists := catalogedFiles[path]; exists {
					catalogedFiles[path] = true // Mark as seen
					updatedCount++
				} else {
					addedCount++
				}
			}

			return nil
		})
	}

	// Scan TV shows directory
	if err := scanPath(TVShowPath); err != nil {
		logMessage(LogLevelWarn, "Catalog", "Error scanning TV shows: %v", err)
	}

	// Scan movies directory
	if err := scanPath(MoviesPath); err != nil {
		logMessage(LogLevelWarn, "Catalog", "Error scanning movies: %v", err)
	}

	// Mark files not seen as deleted
	deletedCount := 0
	for path, seen := range catalogedFiles {
		if !seen {
			if err := RemoveFileFromCatalog(path); err == nil {
				deletedCount++
			}
		}
	}

	logMessage(LogLevelInfo, "Catalog", "Scan complete: %d added, %d updated, %d deleted", addedCount, updatedCount, deletedCount)

	return nil
}

// logCatalogEvent logs an event to the catalog_events table
func logCatalogEvent(eventType, filePath, mediaType, showName, season, episode, title, year, details string) {
	if CatalogDB == nil {
		return
	}

	_, err := CatalogDB.Exec(`
		INSERT INTO catalog_events (event_type, file_path, media_type, show_name, season, episode, title, year, details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, eventType, filePath, mediaType, showName, season, episode, title, year, details)

	if err != nil {
		logMessage(LogLevelWarn, "Catalog", "Failed to log event: %v", err)
	}
}

// GetCatalogStats returns statistics about the catalog
func GetCatalogStats() (map[string]interface{}, error) {
	if CatalogDB == nil {
		return nil, fmt.Errorf("catalog database not initialized")
	}

	stats := make(map[string]interface{})

	// Total files
	var totalFiles, activeFiles, deletedFiles int
	CatalogDB.QueryRow("SELECT COUNT(*) FROM files").Scan(&totalFiles)
	CatalogDB.QueryRow("SELECT COUNT(*) FROM files WHERE status = 'active'").Scan(&activeFiles)
	CatalogDB.QueryRow("SELECT COUNT(*) FROM files WHERE status = 'deleted'").Scan(&deletedFiles)

	stats["total_files"] = totalFiles
	stats["active_files"] = activeFiles
	stats["deleted_files"] = deletedFiles

	// Total TV shows and episodes
	var totalShows, totalEpisodes int
	CatalogDB.QueryRow("SELECT COUNT(DISTINCT show_name) FROM files WHERE status = 'active' AND media_type = 'tv'").Scan(&totalShows)
	CatalogDB.QueryRow("SELECT COUNT(*) FROM files WHERE status = 'active' AND media_type = 'tv'").Scan(&totalEpisodes)
	stats["total_shows"] = totalShows
	stats["total_episodes"] = totalEpisodes

	// Total movies
	var totalMovies int
	CatalogDB.QueryRow("SELECT COUNT(*) FROM files WHERE status = 'active' AND media_type = 'movie'").Scan(&totalMovies)
	stats["total_movies"] = totalMovies

	// Total size
	var totalSize int64
	CatalogDB.QueryRow("SELECT COALESCE(SUM(file_size), 0) FROM files WHERE status = 'active'").Scan(&totalSize)
	stats["total_size_bytes"] = totalSize
	stats["total_size_gb"] = float64(totalSize) / (1024 * 1024 * 1024)

	// TV shows size
	var tvSize int64
	CatalogDB.QueryRow("SELECT COALESCE(SUM(file_size), 0) FROM files WHERE status = 'active' AND media_type = 'tv'").Scan(&tvSize)
	stats["tv_size_gb"] = float64(tvSize) / (1024 * 1024 * 1024)

	// Movies size
	var moviesSize int64
	CatalogDB.QueryRow("SELECT COALESCE(SUM(file_size), 0) FROM files WHERE status = 'active' AND media_type = 'movie'").Scan(&moviesSize)
	stats["movies_size_gb"] = float64(moviesSize) / (1024 * 1024 * 1024)

	return stats, nil
}
