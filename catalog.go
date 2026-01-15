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
	ShowName        string
	Season          string
	Episode         string
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

	// Create tables
	schema := `
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

	CREATE INDEX IF NOT EXISTS idx_show_name ON files(show_name);
	CREATE INDEX IF NOT EXISTS idx_season ON files(season);
	CREATE INDEX IF NOT EXISTS idx_status ON files(status);
	CREATE INDEX IF NOT EXISTS idx_file_path ON files(file_path);

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

	_, err = CatalogDB.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to create catalog schema: %w", err)
	}

	logMessage(LogLevelInfo, "Catalog", "Catalog database initialized at %s", CatalogPath)
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

	// Parse TV show info
	tvInfo := parseTVShowInfo(filename)

	// Check if file already exists in catalog
	var existingID int64
	err = CatalogDB.QueryRow("SELECT id FROM files WHERE file_path = ?", filePath).Scan(&existingID)

	if err == sql.ErrNoRows {
		// New file - insert
		result, err := CatalogDB.Exec(`
			INSERT INTO files (file_path, filename, show_name, season, episode, file_size, created_at, modified_at, last_seen_at, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active')
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
			return fmt.Errorf("failed to insert file: %w", err)
		}

		_, _ = result.LastInsertId()

		// Log event
		logCatalogEvent("added", filePath, tvInfo.ShowName, tvInfo.Season, tvInfo.Episode, "")

		logMessage(LogLevelInfo, "Catalog", "Added: %s (Show: %s S%sE%s)", filename, tvInfo.ShowName, tvInfo.Season, tvInfo.Episode)

		// Send catalog update when new file is added
		go func() {
			if err := SendCatalogUpdate(); err != nil {
				logMessage(LogLevelWarn, "CatalogSync", "Failed to send catalog update: %v", err)
			}
		}()

		return nil
	} else if err != nil {
		return fmt.Errorf("failed to check existing file: %w", err)
	}

	// File exists - update
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
		return fmt.Errorf("failed to update file: %w", err)
	}

	logCatalogEvent("updated", filePath, tvInfo.ShowName, tvInfo.Season, tvInfo.Episode, "")
	logMessage(LogLevelInfo, "Catalog", "Updated: %s", filename)

	return nil
}

// RemoveFileFromCatalog marks a file as deleted
func RemoveFileFromCatalog(filePath string) error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database not initialized")
	}

	// Get file info before marking as deleted
	var showName, season, episode string
	err := CatalogDB.QueryRow("SELECT show_name, season, episode FROM files WHERE file_path = ?", filePath).Scan(&showName, &season, &episode)
	if err != nil {
		return err
	}

	_, err = CatalogDB.Exec("UPDATE files SET status = 'deleted', last_seen_at = ? WHERE file_path = ?", time.Now(), filePath)
	if err != nil {
		return fmt.Errorf("failed to mark file as deleted: %w", err)
	}

	logCatalogEvent("deleted", filePath, showName, season, episode, "")
	logMessage(LogLevelInfo, "Catalog", "Marked as deleted: %s", filePath)

	return nil
}

// ScanAndUpdateCatalog scans the TV shows directory and updates the catalog
func ScanAndUpdateCatalog() error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database not initialized")
	}

	if TVShowPath == "" {
		return fmt.Errorf("TVSHOW_PATH not set")
	}

	logMessage(LogLevelInfo, "Catalog", "Starting catalog scan of %s", TVShowPath)

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

	err = filepath.Walk(TVShowPath, func(path string, info os.FileInfo, err error) error {
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

	if err != nil {
		return fmt.Errorf("failed to walk directory: %w", err)
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
func logCatalogEvent(eventType, filePath, showName, season, episode, details string) {
	if CatalogDB == nil {
		return
	}

	_, err := CatalogDB.Exec(`
		INSERT INTO catalog_events (event_type, file_path, show_name, season, episode, details)
		VALUES (?, ?, ?, ?, ?, ?)
	`, eventType, filePath, showName, season, episode, details)

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

	// Total shows
	var totalShows int
	CatalogDB.QueryRow("SELECT COUNT(DISTINCT show_name) FROM files WHERE status = 'active'").Scan(&totalShows)
	stats["total_shows"] = totalShows

	// Total size
	var totalSize int64
	CatalogDB.QueryRow("SELECT COALESCE(SUM(file_size), 0) FROM files WHERE status = 'active'").Scan(&totalSize)
	stats["total_size_bytes"] = totalSize
	stats["total_size_gb"] = float64(totalSize) / (1024 * 1024 * 1024)

	return stats, nil
}
