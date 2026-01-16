package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var Jobs []*Item
var MoviesPath string
var TVShowPath string
var PORT string
var JobMap map[int64]*Item

// var Interval int64
var CurrentJobs map[string]*Item
var JobsMutex sync.Mutex
var CurrentJobsMutex sync.Mutex

// Regex patterns to match season and episode
// Pattern 1: S01E02, s01e02, S1E1
var tvShowPattern = regexp.MustCompile(`(?i)s(\d{1,2})e(\d{1,2})`)

// Pattern 2: Season 02 - 01, season 2 - 1
var tvShowPatternSpelled = regexp.MustCompile(`(?i)season\s+(\d{1,2})\s*-\s*(\d{1,3})`)

// Pattern 3: Just " - 01" (implies Season 01)
var tvShowPatternSimple = regexp.MustCompile(`\s-\s+(\d{1,3})(?:\D|$)`)

// Pattern to match square brackets and their content (e.g., [Fansub], [1080p])
var squareBracketsPattern = regexp.MustCompile(`\[[^\]]*\]`)

// Pattern to match year in parentheses (e.g., (2024), (2022))
var yearPattern = regexp.MustCompile(`\s*\((?:19|20)\d{2}\)`)

// Logger levels
const (
	LogLevelInfo  = "INFO"
	LogLevelWarn  = "WARN"
	LogLevelError = "ERROR"
	LogLevelDebug = "DEBUG"
)

// TVShowInfo holds parsed information about a TV show episode
type TVShowInfo struct {
	ShowName       string
	Season         string
	Episode        string
	OriginalName   string
	QualityInfo    string // Everything after the episode number (resolution, codec, etc.)
	Extension      string
	HasSeasonInfo  bool
	StandardName   string // Standardized filename: ShowName.SXXEXX.quality.ext
}

// MovieInfo holds parsed information about a movie
type MovieInfo struct {
	Title        string
	Year         string
	Quality      string // Resolution, codec, etc. (1080p, BluRay, x264, etc.)
	OriginalName string
	Extension    string
	HasMovieInfo bool
}

func logMessage(level, component, message string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	formattedMsg := fmt.Sprintf(message, args...)
	log.Printf("[%s] [%s] [%s] %s", timestamp, level, component, formattedMsg)
}

// toTitleCase converts underscore_separated_string to Title_Case
// Example: "the_last_of_us" -> "The_Last_Of_Us"
func toTitleCase(s string) string {
	words := strings.Split(s, "_")
	for i, word := range words {
		if len(word) > 0 {
			// Capitalize first letter, lowercase the rest
			words[i] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
		}
	}
	return strings.Join(words, "_")
}

// parseTVShowInfo extracts show name, season, and episode from filename
// Supports multiple formats:
//   - S01E02, s01e02 -> Season 01, Episode 02
//   - Season 02 - 01 -> Season 02, Episode 01
//   - Show Name - 01 -> Season 01, Episode 01
func parseTVShowInfo(filename string) TVShowInfo {
	info := TVShowInfo{
		OriginalName:  filename,
		HasSeasonInfo: false,
	}

	var matches []int
	var showNameRaw string
	var seasonNum, episodeNum string

	// Try Pattern 1: S01E02
	matches = tvShowPattern.FindStringSubmatchIndex(filename)
	if matches != nil {
		showNameRaw = filename[:matches[0]]
		seasonNum = filename[matches[2]:matches[3]]
		episodeNum = filename[matches[4]:matches[5]]
	} else {
		// Try Pattern 2: Season 02 - 01
		matches = tvShowPatternSpelled.FindStringSubmatchIndex(filename)
		if matches != nil {
			showNameRaw = filename[:matches[0]]
			seasonNum = filename[matches[2]:matches[3]]
			episodeNum = filename[matches[4]:matches[5]]
		} else {
			// Try Pattern 3: - 01 (implies Season 01)
			matches = tvShowPatternSimple.FindStringSubmatchIndex(filename)
			if matches != nil {
				showNameRaw = filename[:matches[0]]
				seasonNum = "01"
				episodeNum = filename[matches[2]:matches[3]]
			} else {
				// No season info found
				return info
			}
		}
	}

	// Remove square brackets and their content (common in anime releases: [Fansub] Show Name)
	showName := squareBracketsPattern.ReplaceAllString(showNameRaw, "")
	// Remove year in parentheses (e.g., "Show Name (2024)" -> "Show Name")
	showName = yearPattern.ReplaceAllString(showName, "")
	// Clean up the show name: replace dots and spaces with underscores, trim
	showName = strings.ReplaceAll(showName, ".", "_")
	showName = strings.ReplaceAll(showName, " ", "_")
	showName = strings.Trim(showName, "_")
	// Remove trailing hyphens
	showName = strings.Trim(showName, "-")
	showName = strings.Trim(showName, "_")
	// Remove duplicate underscores
	for strings.Contains(showName, "__") {
		showName = strings.ReplaceAll(showName, "__", "_")
	}
	// Normalize to Title Case to prevent duplicates like "the_bear" vs "The_Bear"
	showName = toTitleCase(showName)

	// Pad season number to 2 digits if needed
	if len(seasonNum) == 1 {
		seasonNum = "0" + seasonNum
	}

	// Pad episode number to 2 digits if needed
	if len(episodeNum) == 1 {
		episodeNum = "0" + episodeNum
	}

	// Extract quality info (everything after the episode pattern)
	qualityInfo := ""
	extension := filepath.Ext(filename)
	filenameWithoutExt := strings.TrimSuffix(filename, extension)

	// Find where the episode pattern ends
	if matches != nil && len(matches) > 1 {
		afterPattern := filenameWithoutExt[matches[1]:]
		// Clean up quality info
		afterPattern = strings.TrimPrefix(afterPattern, ".")
		afterPattern = strings.TrimPrefix(afterPattern, "-")
		afterPattern = strings.TrimPrefix(afterPattern, " ")
		afterPattern = strings.Trim(afterPattern, ".")
		qualityInfo = afterPattern
	}

	// Build standardized filename: ShowName.SXXEXX[.quality].ext
	standardName := showName + ".S" + seasonNum + "E" + episodeNum
	if qualityInfo != "" {
		standardName += "." + qualityInfo
	}
	standardName += extension

	info.ShowName = showName
	info.Season = seasonNum
	info.Episode = episodeNum
	info.QualityInfo = qualityInfo
	info.Extension = extension
	info.StandardName = standardName
	info.HasSeasonInfo = true

	return info
}

// buildTVShowPath creates the proper directory structure for a TV show
// Returns the full path where the file should be saved
func buildTVShowPath(basePath string, info TVShowInfo) (string, error) {
	if !info.HasSeasonInfo {
		// No season info, just put it in the base TV show path
		return basePath, nil
	}

	// Create path: basePath/ShowName/Season_XX/
	showDir := filepath.Join(basePath, info.ShowName)
	seasonDir := filepath.Join(showDir, fmt.Sprintf("Season_%s", info.Season))

	// Create directories if they don't exist
	if err := os.MkdirAll(seasonDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory structure: %w", err)
	}

	logMessage(LogLevelInfo, "FileOrg", "Created directory structure: %s", seasonDir)
	return seasonDir, nil
}

// parseMovieInfo extracts title, year, and quality from movie filename
// Expected format: Title.Year.Quality.ext (e.g., Inception.2010.1080p.mkv)
// Also handles: Title (Year) Quality.ext, Title.Year.ext, etc.
func parseMovieInfo(filename string) MovieInfo {
	info := MovieInfo{
		OriginalName:  filename,
		HasMovieInfo:  false,
	}

	extension := filepath.Ext(filename)
	info.Extension = extension
	filenameWithoutExt := strings.TrimSuffix(filename, extension)

	// Pattern to match year (1900-2099)
	yearRegex := regexp.MustCompile(`(?:^|\.)(\d{4})(?:\.|$)`)
	yearMatches := yearRegex.FindStringSubmatch(filenameWithoutExt)

	var year string
	var titlePart string
	var qualityPart string

	if len(yearMatches) >= 2 {
		year = yearMatches[1]
		// Validate year range
		yearInt, _ := strconv.Atoi(year)
		if yearInt >= 1900 && yearInt <= 2099 {
			info.Year = year

			// Split filename into parts using the year as delimiter
			parts := strings.Split(filenameWithoutExt, year)
			if len(parts) >= 1 {
				// Everything before year is the title
				titlePart = strings.TrimSuffix(parts[0], ".")
				titlePart = strings.TrimPrefix(titlePart, ".")

				// Everything after year is quality info
				if len(parts) > 1 {
					qualityPart = strings.TrimPrefix(parts[1], ".")
					qualityPart = strings.TrimSuffix(qualityPart, ".")
				}
			}
		}
	}

	// If no year found, try to extract quality markers from the end
	if year == "" {
		// Common quality patterns: 720p, 1080p, 2160p, BluRay, WEB-DL, etc.
		qualityRegex := regexp.MustCompile(`(?i)\.(720p|1080p|2160p|4K|BluRay|BrRip|WEB-DL|WEBRip|HDTV|x264|x265|HEVC|10bit).*$`)
		if qualityMatch := qualityRegex.FindStringIndex(filenameWithoutExt); qualityMatch != nil {
			titlePart = filenameWithoutExt[:qualityMatch[0]]
			qualityPart = filenameWithoutExt[qualityMatch[0]+1:] // Skip the dot
		} else {
			// No quality markers found, entire filename is title
			titlePart = filenameWithoutExt
		}
	}

	// Clean up title: replace dots/underscores with spaces, remove brackets
	title := titlePart
	title = strings.ReplaceAll(title, ".", " ")
	title = strings.ReplaceAll(title, "_", " ")
	title = squareBracketsPattern.ReplaceAllString(title, "")
	title = strings.TrimSpace(title)

	// Clean up quality info
	quality := qualityPart
	if quality != "" {
		quality = strings.ReplaceAll(quality, ".", " ")
		quality = strings.TrimSpace(quality)
	}

	info.Title = title
	info.Quality = quality
	info.HasMovieInfo = (title != "")

	return info
}

func main() {
	logMessage(LogLevelInfo, "Main", "=== Local Plex Download Manager Starting ===")

	CurrentJobs = make(map[string]*Item)
	MoviesPath = os.Getenv("MOVIES_PATH")
	TVShowPath = os.Getenv("TVSHOW_PATH")
	PORT = os.Getenv("PORT")
	Jobs = make([]*Item, 0)
	JobMap = make(map[int64]*Item)

	logMessage(LogLevelInfo, "Main", "Configuration:")
	logMessage(LogLevelInfo, "Main", "  Movies Path: %s", MoviesPath)
	logMessage(LogLevelInfo, "Main", "  TV Shows Path: %s", TVShowPath)
	logMessage(LogLevelInfo, "Main", "  Server Port: %s", PORT)

	// Initialize catalog database
	if err := InitCatalog(); err != nil {
		logMessage(LogLevelError, "Main", "Failed to initialize catalog: %v", err)
	} else {
		logMessage(LogLevelInfo, "Main", "Catalog database initialized")
		// Run initial scan in background
		go func() {
			time.Sleep(5 * time.Second) // Wait for startup
			logMessage(LogLevelInfo, "Main", "Running initial catalog scan...")
			if err := ScanAndUpdateCatalog(); err != nil {
				logMessage(LogLevelError, "Main", "Initial catalog scan failed: %v", err)
			} else {
				logMessage(LogLevelInfo, "Main", "Initial catalog scan completed")
			}
			// Mark initial scan as complete and send catalog update
			FinishInitialScan()
		}()
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logMessage(LogLevelInfo, "Main", "Starting background workers...")
	go Dequeue(ctx)
	go GetQueue(ctx)

	r := gin.Default()
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})
	r.POST("/download", HandleDownload)
	r.GET("/queue", Queue)
	r.GET("/catalog/stats", CatalogStats)
	r.POST("/catalog/scan", CatalogScan)
	r.GET("/catalog/search", CatalogSearch)

	logMessage(LogLevelInfo, "Main", "HTTP server listening on port %s", PORT)
	logMessage(LogLevelInfo, "Main", "Endpoints: GET /ping, POST /download, GET /queue, GET /catalog/stats, POST /catalog/scan, GET /catalog/search")

	if err := r.Run(":" + PORT); err != nil {
		logMessage(LogLevelError, "Main", "Server failed to start: %v", err)
	}

}

func (i *Item) StartDownload() error {
	defer func() {
		CurrentJobsMutex.Lock()
		delete(CurrentJobs, i.URL)
		CurrentJobsMutex.Unlock()
	}()

	logMessage(LogLevelInfo, "Download", "Starting download for: %s (Type: %s)", i.Name, i.Type)
	i.Started = true

	// Determine base destination
	destination := MoviesPath
	filename := i.Name // Default to original name

	if i.Type == TVShow {
		destination = TVShowPath

		// Parse TV show info and create proper folder structure
		tvInfo := parseTVShowInfo(i.Name)
		if tvInfo.HasSeasonInfo {
			logMessage(LogLevelInfo, "Download", "Parsed TV show: %s - Season %s Episode %s",
				tvInfo.ShowName, tvInfo.Season, tvInfo.Episode)

			// Use standardized filename
			filename = tvInfo.StandardName
			logMessage(LogLevelInfo, "Download", "Standardizing filename: %s -> %s", i.Name, filename)

			// Build the proper path with show name and season folders
			seasonPath, err := buildTVShowPath(destination, tvInfo)
			if err != nil {
				logMessage(LogLevelError, "Download", "Failed to create directory structure: %v", err)
				return err
			}
			destination = seasonPath
		} else {
			logMessage(LogLevelWarn, "Download", "No season/episode info found in filename: %s", i.Name)
		}
	}

	// Build full file path with standardized filename
	fullPath := filepath.Join(destination, filename)
	logMessage(LogLevelDebug, "Download", "Destination path: %s", fullPath)

	start := time.Now()

	// Create the file
	out, err := os.Create(fullPath)
	if err != nil {
		logMessage(LogLevelError, "Download", "Failed to create file %s: %v", fullPath, err)
		return err
	}
	defer out.Close()

	// Get file size
	headResp, err := http.Head(i.URL)
	if err != nil {
		logMessage(LogLevelError, "Download", "Failed to get file size for %s: %v", i.Name, err)
		return err
	}
	defer headResp.Body.Close()

	size, err := strconv.Atoi(headResp.Header.Get("Content-Length"))
	if err != nil {
		logMessage(LogLevelError, "Download", "Invalid Content-Length for %s: %v", i.Name, err)
		return err
	}

	sizeMB := float64(size) / 1024 / 1024
	logMessage(LogLevelInfo, "Download", "File size: %.2f MB", sizeMB)

	// Start progress tracking
	done := make(chan int64)
	go i.UpdateDownloadPercent(done, fullPath, int64(size))

	// Download the file
	resp, err := http.Get(i.URL)
	if err != nil {
		logMessage(LogLevelError, "Download", "Failed to download %s: %v", i.Name, err)
		return err
	}
	defer resp.Body.Close()

	n, err := io.Copy(out, resp.Body)
	if err != nil {
		logMessage(LogLevelError, "Download", "Failed to write file %s: %v", i.Name, err)
		return err
	}

	done <- n

	elapsed := time.Since(start)
	speedMBps := sizeMB / elapsed.Seconds()
	logMessage(LogLevelInfo, "Download", "✓ Completed: %s | Size: %.2f MB | Time: %s | Speed: %.2f MB/s",
		filename, sizeMB, elapsed.Round(time.Second), speedMBps)

	update(filename)
	i.Completed = true
	UpdateQueue(i)

	// Add file to catalog
	if err := AddFileToCatalog(fullPath); err != nil {
		logMessage(LogLevelWarn, "Catalog", "Failed to add file to catalog: %v", err)
	}

	return nil
}

func update(name string) {
	url := "https://putio.bramsoft.com/update"
	method := "POST"

	payload := strings.NewReader(fmt.Sprintf("{\"name\": \"%s\"}", name))

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		logMessage(LogLevelError, "Webhook", "Failed to create update request for %s: %v", name, err)
		return
	}
	req.Header.Add("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		logMessage(LogLevelError, "Webhook", "Failed to send update webhook for %s: %v", name, err)
		return
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		logMessage(LogLevelError, "Webhook", "Failed to read update response for %s: %v", name, err)
		return
	}
	logMessage(LogLevelInfo, "Webhook", "Update sent for %s: %s", name, string(body))
}

func HandleDownload(c *gin.Context) {
	var json Item
	if err := c.BindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "unable to bind JSON",
		})
		// fmt.Println(err)
		return
	}

	strings.ReplaceAll(json.Name, " ", ".")

	JobsMutex.Lock()
	Jobs = append(Jobs, &json)
	queueCount := len(Jobs)
	JobsMutex.Unlock()

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Added to Jobs. Current Queue Count %d", queueCount)})
}

func Dequeue(ctx context.Context) {
	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			logMessage(LogLevelError, "Dequeue", "Panicked: %v. Restarting...", r)
			go Dequeue(ctx)
		}
	}()

	const (
		dequeueInterval   = 30 * time.Second
		maxConcurrentJobs = 3
	)

	logMessage(LogLevelInfo, "Dequeue", "Worker started (checking every %s, max %d concurrent jobs)", dequeueInterval, maxConcurrentJobs)

	ticker := time.NewTicker(dequeueInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logMessage(LogLevelInfo, "Dequeue", "Shutting down gracefully")
			return
		case <-ticker.C:
			// Process jobs
		}

		JobsMutex.Lock()
		jobsAvailable := len(Jobs) > 0
		queueSize := len(Jobs)
		JobsMutex.Unlock()

		CurrentJobsMutex.Lock()
		activeJobs := len(CurrentJobs)
		canProcessMore := activeJobs < maxConcurrentJobs
		CurrentJobsMutex.Unlock()

		if !jobsAvailable || !canProcessMore {
			if jobsAvailable && !canProcessMore {
				logMessage(LogLevelDebug, "Dequeue", "Max concurrent jobs reached (%d/%d), queue size: %d",
					activeJobs, maxConcurrentJobs, queueSize)
			}
			continue
		}

		// Dequeue job
		JobsMutex.Lock()
		if len(Jobs) == 0 {
			JobsMutex.Unlock()
			continue
		}
		job := Jobs[0]
		Jobs = Jobs[1:]
		remainingJobs := len(Jobs)
		JobsMutex.Unlock()

		// Add to current jobs
		CurrentJobsMutex.Lock()
		CurrentJobs[job.URL] = job
		CurrentJobsMutex.Unlock()

		logMessage(LogLevelInfo, "Dequeue", "Dequeued: %s (Remaining in queue: %d)", job.Name, remainingJobs)

		// Start download in goroutine
		go func(j *Item) {
			if err := j.StartDownload(); err != nil {
				logMessage(LogLevelError, "Dequeue", "Download failed for %s, re-queuing: %v", j.Name, err)
				// Re-queue on failure
				JobsMutex.Lock()
				Jobs = append(Jobs, j)
				JobsMutex.Unlock()
			}
		}(job)
	}
}

func Queue(c *gin.Context) {
	JobsMutex.Lock()
	jobsCount := len(Jobs)
	var JobQueue []string
	for _, job := range Jobs {
		JobQueue = append(JobQueue, job.Name)
	}
	JobsMutex.Unlock()

	CurrentJobsMutex.Lock()
	currentJobsCount := len(CurrentJobs)
	var CurrentJobQueue []string
	for _, job := range CurrentJobs {
		CurrentJobQueue = append(CurrentJobQueue, job.Name+" - "+job.CompletedPercent+"% completed")
	}
	CurrentJobsMutex.Unlock()

	totalJobs := jobsCount + currentJobsCount

	if totalJobs == 0 {
		c.JSON(http.StatusOK, gin.H{
			"message": "no jobs in queue",
		})
		return
	}

	resp := gin.H{
		"totalJobs": totalJobs,
		"queue":     JobQueue,
		"working":   CurrentJobQueue,
		"message":   fmt.Sprintf("%d", totalJobs),
	}
	c.JSON(http.StatusOK, resp)
}

func GetQueue(ctx context.Context) {
	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			logMessage(LogLevelError, "QueuePoller", "Panicked: %v. Restarting...", r)
			go GetQueue(ctx)
		}
	}()

	const (
		pollInterval   = 5 * time.Minute
		initialBackoff = 30 * time.Second
		maxBackoff     = 10 * time.Minute
		maxQueueSize   = 1000
	)

	logMessage(LogLevelInfo, "QueuePoller", "Started (polling every %s)", pollInterval)

	retryDelay := initialBackoff
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Run immediately on start, then use ticker
	for {
		logMessage(LogLevelInfo, "QueuePoller", "Fetching remote queue...")

		url := "https://putio.bramsoft.com/queue"
		method := "POST"

		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			logMessage(LogLevelError, "QueuePoller", "Failed to create request: %v (retry in %s)", err, retryDelay)
			time.Sleep(retryDelay)
			retryDelay = min(retryDelay*2, maxBackoff)
			continue
		}

		res, err := client.Do(req)
		if err != nil {
			logMessage(LogLevelError, "QueuePoller", "Failed to fetch queue: %v (retry in %s)", err, retryDelay)
			time.Sleep(retryDelay)
			retryDelay = min(retryDelay*2, maxBackoff)
			continue
		}

		body, err := io.ReadAll(res.Body)
		res.Body.Close()

		if err != nil {
			logMessage(LogLevelError, "QueuePoller", "Failed to read response: %v (retry in %s)", err, retryDelay)
			time.Sleep(retryDelay)
			retryDelay = min(retryDelay*2, maxBackoff)
			continue
		}

		// Parse response with catalog status
		var response QueueResponse
		if err := json.Unmarshal(body, &response); err != nil {
			logMessage(LogLevelError, "QueuePoller", "Failed to parse queue response: %v (retry in %s)", err, retryDelay)
			time.Sleep(retryDelay)
			retryDelay = min(retryDelay*2, maxBackoff)
			continue
		}

		items := response.Items

		// Check if catalog resync is requested
		if response.CatalogStatus.NeedsResync {
			logMessage(LogLevelInfo, "CatalogSync", "Resync requested by server: %s", response.CatalogStatus.ResyncReason)
			go func() {
				if err := SendCatalogUpdate(); err != nil {
					logMessage(LogLevelWarn, "CatalogSync", "Failed to send catalog update: %v", err)
				} else {
					logMessage(LogLevelInfo, "CatalogSync", "Catalog resync completed successfully")
				}
			}()
		}

		// Check queue size limit
		JobsMutex.Lock()
		currentQueueSize := len(Jobs)
		if currentQueueSize+len(items) > maxQueueSize {
			logMessage(LogLevelWarn, "QueuePoller", "Queue size limit reached (%d/%d). Skipping %d new items.",
				currentQueueSize, maxQueueSize, len(items))
			JobsMutex.Unlock()
		} else {
			Jobs = append(Jobs, items...)
			newQueueSize := len(Jobs)
			JobsMutex.Unlock()

			if len(items) > 0 {
				logMessage(LogLevelInfo, "QueuePoller", "Added %d new items to queue (total: %d)", len(items), newQueueSize)
				for _, item := range items {
					logMessage(LogLevelDebug, "QueuePoller", "  - %s (%s)", item.Name, item.Type)
					item.InQueue = true
					UpdateQueue(item)
				}
			} else {
				logMessage(LogLevelDebug, "QueuePoller", "No new items in queue")
			}
		}

		// Reset backoff on success
		retryDelay = initialBackoff

		// Send catalog update (every 5 minutes with queue poll)
		if CatalogDB != nil {
			go func() {
				if err := SendCatalogUpdate(); err != nil {
					logMessage(LogLevelWarn, "CatalogSync", "Failed to send catalog update: %v", err)
				}
			}()
		}

		// Wait for next poll or context cancellation
		select {
		case <-ctx.Done():
			logMessage(LogLevelInfo, "QueuePoller", "Shutting down gracefully")
			return
		case <-ticker.C:
			// Continue to next iteration
		}
	}
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (i *Item) UpdateDownloadPercent(done chan int64, path string, total int64) {
	var stop bool = false
	lastReportedPercent := -1

	for {
		select {
		case <-done:
			stop = true
		default:
			file, err := os.Open(path)
			if err != nil {
				logMessage(LogLevelError, "Progress", "Failed to open file for progress tracking %s: %v", i.Name, err)
				return
			}

			fi, err := file.Stat()
			if err != nil {
				file.Close()
				logMessage(LogLevelError, "Progress", "Failed to stat file %s: %v", i.Name, err)
				return
			}

			size := fi.Size()
			file.Close()

			if size == 0 {
				size = 1
			}

			percent := float64(size) / float64(total) * 100
			i.CompletedPercent = fmt.Sprintf("%.0f", percent)

			// Report progress every 5%
			currentPercent := int(percent)
			if currentPercent%5 == 0 && currentPercent != lastReportedPercent {
				sizeMB := float64(size) / 1024 / 1024
				totalMB := float64(total) / 1024 / 1024
				logMessage(LogLevelInfo, "Progress", "%s: %.0f%% (%.2f MB / %.2f MB)",
					i.Name, percent, sizeMB, totalMB)
				UpdateQueue(i)
				lastReportedPercent = currentPercent
			}
		}

		if stop {
			break
		}

		time.Sleep(time.Second)
	}
}

func UpdateQueue(item *Item) {
	url := "https://putio.bramsoft.com/updateQueue"
	method := "POST"

	arr, err := json.Marshal(item)
	if err != nil {
		logMessage(LogLevelError, "QueueUpdate", "Failed to marshal item %s: %v", item.Name, err)
		return
	}

	payload := bytes.NewReader(arr)
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		logMessage(LogLevelError, "QueueUpdate", "Failed to create queue update request: %v", err)
		return
	}
	req.Header.Add("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		logMessage(LogLevelError, "QueueUpdate", "Failed to send queue update: %v", err)
		return
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		logMessage(LogLevelWarn, "QueueUpdate", "Failed to read queue update response: %v", err)
		return
	}
	logMessage(LogLevelDebug, "QueueUpdate", "Queue updated: %s", string(body))
}

type Item struct {
	URL              string      `json:"url"`
	Type             ContentType `json:"type"`
	Name             string      `json:"name"`
	FileId           int64       `json:"file_id"`
	Started          bool        `json:"started"`
	CompletedPercent string      `json:"completed_percent"`
	Completed        bool        `json:"completed"`
	InQueue          bool        `json:"in_queue"`
}

// QueueResponse represents the response from /queue endpoint with catalog status
type QueueResponse struct {
	Items         []*Item       `json:"items"`
	CatalogStatus CatalogStatus `json:"catalog_status"`
}

// CatalogStatus holds catalog sync status from putio-go-server
type CatalogStatus struct {
	NeedsResync  bool   `json:"needs_resync"`
	ResyncReason string `json:"resync_reason,omitempty"`
	LastUpdated  string `json:"last_updated"`
	ShowCount    int    `json:"show_count"`
	EpisodeCount int    `json:"episode_count"`
}

type ContentType string

const (
	Movies ContentType = "movie"
	Anime  ContentType = "anime"
	TVShow ContentType = "tvshow"
)
