package main

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

var (
	catalogSyncMutex     sync.Mutex
	lastCatalogSyncTime  time.Time
	catalogSyncDebounce  = 10 * time.Second // Wait 10 seconds before syncing
	pendingSyncTimer     *time.Timer
	isInitialScan        = true // Flag to prevent spam during initial scan
)

// CatalogSyncData represents the complete catalog data to send to putio-go-server
type CatalogSyncData struct {
	Timestamp  time.Time              `json:"timestamp"`
	Statistics CatalogStatistics      `json:"statistics"`
	Shows      []ShowData             `json:"shows"`
	Movies     []MovieData            `json:"movies"`
	Recent     []RecentItem           `json:"recent"`
}

// CatalogStatistics holds overall catalog statistics
type CatalogStatistics struct {
	TotalShows     int     `json:"total_shows"`
	TotalEpisodes  int     `json:"total_episodes"`
	TotalMovies    int     `json:"total_movies"`
	TotalSizeGB    float64 `json:"total_size_gb"`
	TotalSizeBytes int64   `json:"total_size_bytes"`
	TVSizeGB       float64 `json:"tv_size_gb"`
	MoviesSizeGB   float64 `json:"movies_size_gb"`
}

// ShowData represents a TV show with all its seasons and episodes
type ShowData struct {
	ShowName string       `json:"show_name"`
	Seasons  []SeasonData `json:"seasons"`
}

// SeasonData represents a season with all its episodes
type SeasonData struct {
	Season   string        `json:"season"`
	Episodes []EpisodeData `json:"episodes"`
}

// EpisodeData represents a single episode
type EpisodeData struct {
	Episode    string `json:"episode"`
	Filename   string `json:"filename"`
	FilePath   string `json:"file_path"`
	FileSize   int64  `json:"file_size"`
	ModifiedAt string `json:"modified_at"`
}

// MovieData represents a movie
type MovieData struct {
	Title      string `json:"title"`
	Year       string `json:"year"`
	Quality    string `json:"quality"`
	Filename   string `json:"filename"`
	FilePath   string `json:"file_path"`
	FileSize   int64  `json:"file_size"`
	ModifiedAt string `json:"modified_at"`
}

// RecentItem represents a recently added TV episode or movie
type RecentItem struct {
	MediaType      string `json:"media_type"` // "tv" or "movie"
	ShowName       string `json:"show_name,omitempty"`
	Season         string `json:"season,omitempty"`
	Episode        string `json:"episode,omitempty"`
	Title          string `json:"title,omitempty"`
	Year           string `json:"year,omitempty"`
	Filename       string `json:"filename"`
	AddedToCatalog string `json:"added_to_catalog"`
}

// GetCatalogSyncData retrieves all catalog data for syncing
func GetCatalogSyncData() (*CatalogSyncData, error) {
	if CatalogDB == nil {
		return nil, fmt.Errorf("catalog database not initialized")
	}

	data := &CatalogSyncData{
		Timestamp: time.Now(),
	}

	// Get statistics
	stats, err := GetCatalogStats()
	if err != nil {
		return nil, fmt.Errorf("failed to get statistics: %w", err)
	}

	data.Statistics = CatalogStatistics{
		TotalShows:     stats["total_shows"].(int),
		TotalEpisodes:  stats["total_episodes"].(int),
		TotalMovies:    stats["total_movies"].(int),
		TotalSizeGB:    stats["total_size_gb"].(float64),
		TotalSizeBytes: stats["total_size_bytes"].(int64),
		TVSizeGB:       stats["tv_size_gb"].(float64),
		MoviesSizeGB:   stats["movies_size_gb"].(float64),
	}

	// Get all TV shows with seasons and episodes
	shows := make(map[string]map[string][]EpisodeData)

	tvRows, err := CatalogDB.Query(`
		SELECT show_name, season, episode, filename, file_path, file_size, modified_at
		FROM files
		WHERE status = 'active' AND media_type = 'tv'
		ORDER BY show_name, season, episode
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query TV shows: %w", err)
	}
	defer tvRows.Close()

	for tvRows.Next() {
		var showName, season, episode, filename, filePath, modifiedAt string
		var fileSize int64

		if err := tvRows.Scan(&showName, &season, &episode, &filename, &filePath, &fileSize, &modifiedAt); err != nil {
			continue
		}

		if shows[showName] == nil {
			shows[showName] = make(map[string][]EpisodeData)
		}

		shows[showName][season] = append(shows[showName][season], EpisodeData{
			Episode:    episode,
			Filename:   filename,
			FilePath:   filePath,
			FileSize:   fileSize,
			ModifiedAt: modifiedAt,
		})
	}

	// Convert to slice structure for TV shows
	for showName, seasons := range shows {
		showData := ShowData{
			ShowName: showName,
			Seasons:  []SeasonData{},
		}

		for season, episodes := range seasons {
			showData.Seasons = append(showData.Seasons, SeasonData{
				Season:   season,
				Episodes: episodes,
			})
		}

		data.Shows = append(data.Shows, showData)
	}

	// Get all movies
	movieRows, err := CatalogDB.Query(`
		SELECT title, year, quality, filename, file_path, file_size, modified_at
		FROM files
		WHERE status = 'active' AND media_type = 'movie'
		ORDER BY title, year
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query movies: %w", err)
	}
	defer movieRows.Close()

	for movieRows.Next() {
		var title, year, quality, filename, filePath, modifiedAt string
		var fileSize int64

		if err := movieRows.Scan(&title, &year, &quality, &filename, &filePath, &fileSize, &modifiedAt); err != nil {
			continue
		}

		data.Movies = append(data.Movies, MovieData{
			Title:      title,
			Year:       year,
			Quality:    quality,
			Filename:   filename,
			FilePath:   filePath,
			FileSize:   fileSize,
			ModifiedAt: modifiedAt,
		})
	}

	// Get recent additions (last 50, both TV and movies)
	recentRows, err := CatalogDB.Query(`
		SELECT media_type, show_name, season, episode, title, year, filename, added_to_catalog
		FROM files
		WHERE status = 'active'
		ORDER BY added_to_catalog DESC
		LIMIT 50
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent: %w", err)
	}
	defer recentRows.Close()

	for recentRows.Next() {
		var recent RecentItem
		var showName, season, episode, title, year sql.NullString

		if err := recentRows.Scan(&recent.MediaType, &showName, &season, &episode, &title, &year, &recent.Filename, &recent.AddedToCatalog); err != nil {
			continue
		}

		if showName.Valid {
			recent.ShowName = showName.String
		}
		if season.Valid {
			recent.Season = season.String
		}
		if episode.Valid {
			recent.Episode = episode.String
		}
		if title.Valid {
			recent.Title = title.String
		}
		if year.Valid {
			recent.Year = year.String
		}

		data.Recent = append(data.Recent, recent)
	}

	return data, nil
}

// SendCatalogUpdate sends the catalog data to putio-go-server (with mutex protection)
func SendCatalogUpdate() error {
	// Prevent concurrent syncs
	catalogSyncMutex.Lock()
	defer catalogSyncMutex.Unlock()

	url := RemoteServer + "/catalogUpdate"

	// Get catalog data
	data, err := GetCatalogSyncData()
	if err != nil {
		return fmt.Errorf("failed to get catalog data: %w", err)
	}

	// Convert to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// Compress with gzip
	var compressedBuffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressedBuffer)
	if _, err := gzipWriter.Write(jsonData); err != nil {
		return fmt.Errorf("failed to compress data: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("failed to close gzip writer: %w", err)
	}

	originalSize := len(jsonData)
	compressedSize := compressedBuffer.Len()
	compressionRatio := float64(compressedSize) / float64(originalSize) * 100

	// Send POST request with longer timeout
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest("POST", url, &compressedBuffer)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status: %d", resp.StatusCode)
	}

	lastCatalogSyncTime = time.Now()
	logMessage(LogLevelInfo, "CatalogSync", "Catalog update sent successfully (%d shows, %d episodes, %d movies, %d KB → %d KB, %.1f%% compression)",
		len(data.Shows), data.Statistics.TotalEpisodes, len(data.Movies), originalSize/1024, compressedSize/1024, compressionRatio)

	return nil
}

// DebouncedCatalogSync schedules a catalog sync after a delay (batches multiple changes)
func DebouncedCatalogSync() {
	// Skip immediate sync during initial scan
	if isInitialScan {
		return
	}

	// Cancel pending timer if exists
	if pendingSyncTimer != nil {
		pendingSyncTimer.Stop()
	}

	// Schedule new sync after debounce period
	pendingSyncTimer = time.AfterFunc(catalogSyncDebounce, func() {
		if err := SendCatalogUpdate(); err != nil {
			logMessage(LogLevelWarn, "CatalogSync", "Failed to send catalog update: %v", err)
		}
	})
}

// FinishInitialScan marks the initial scan as complete and triggers a sync
func FinishInitialScan() {
	if !isInitialScan {
		return
	}

	isInitialScan = false
	logMessage(LogLevelInfo, "CatalogSync", "Initial scan complete, sending catalog update")

	// Send immediate sync after initial scan
	go func() {
		if err := SendCatalogUpdate(); err != nil {
			logMessage(LogLevelWarn, "CatalogSync", "Failed to send catalog update: %v", err)
		}
	}()
}
