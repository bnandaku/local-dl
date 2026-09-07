package main

import "time"

// CatalogSyncData represents the complete catalog data to send to putio-go-server
type CatalogSyncData struct {
	Timestamp  time.Time         `json:"timestamp"`
	Statistics CatalogStatistics `json:"statistics"`
	Shows      []ShowData        `json:"shows"`
	Movies     []MovieData       `json:"movies"`
	Recent     []RecentItem      `json:"recent"`
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
