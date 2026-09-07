package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
var RemoteServer string
var JobMap map[int64]*Item

// var Interval int64
var CurrentJobs map[string]*Item
var JobsMutex sync.Mutex
var CurrentJobsMutex sync.Mutex

// Regex patterns to match season and episode
// Pattern 1: S01E02, s01e02, S1E1
var tvShowPattern = regexp.MustCompile(`(?i)s(\d{1,2})[\s._-]*e(\d{1,3})`)

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
	ShowName      string
	Season        string
	Episode       string
	OriginalName  string
	QualityInfo   string // Everything after the episode number (resolution, codec, etc.)
	Extension     string
	HasSeasonInfo bool
	StandardName  string // Standardized filename: ShowName.SXXEXX.quality.ext
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
		OriginalName: filename,
		HasMovieInfo: false,
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

func configureLibraryPaths() {
	MoviesPath = musicEnv("MOVIES_PATH", "/mnt/movies")
	TVShowPath = musicEnv("TVSHOW_PATH", "/mnt/tvshows")
}

func main() {
	configureLibraryPaths()
	if len(os.Args) == 3 && os.Args[1] == "repair-file" {
		if err := InitCatalog(); err != nil {
			log.Fatal(err)
		}
		defer CatalogDB.Close()
		if err := repairLibraryFile(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}
	logMessage(LogLevelInfo, "Main", "=== Local Plex Download Manager Starting ===")

	CurrentJobs = make(map[string]*Item)
	PORT = "8080"
	RemoteServer = os.Getenv("REMOTE_SERVER")
	if RemoteServer == "" {
		RemoteServer = "https://putio.bramsoft.com"
	}
	Jobs = make([]*Item, 0)
	JobMap = make(map[int64]*Item)
	if err := restoreMusicQueue(); err != nil {
		logMessage(LogLevelWarn, "Music", "Cannot restore durable music queue: %v", err)
	}

	logMessage(LogLevelInfo, "Main", "Configuration:")
	logMessage(LogLevelInfo, "Main", "  Movies Path: %s", MoviesPath)
	logMessage(LogLevelInfo, "Main", "  TV Shows Path: %s", TVShowPath)
	logMessage(LogLevelInfo, "Main", "  Server Port: %s", PORT)
	logMessage(LogLevelInfo, "Main", "  Remote Server: %s", RemoteServer)

	// Initialize catalog database
	if err := InitCatalog(); err != nil {
		logMessage(LogLevelError, "Main", "Failed to initialize catalog: %v", err)
	} else {
		logMessage(LogLevelInfo, "Main", "Catalog database initialized")

	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logMessage(LogLevelInfo, "Main", "Starting background workers...")
	go Dequeue(ctx)
	go GetQueue(ctx)
	go runCatalogSync(ctx)

	r := gin.Default()
	if err := InitMusicPlaylists(ctx, r); err != nil {
		logMessage(LogLevelError, "MusicPlaylists", "Playlist initialization failed: %v", err)
		return
	}
	InitMusicIngest(ctx)
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})
	r.POST("/download", HandleDownload)
	r.GET("/queue", Queue)
	r.GET("/recovery/status", recoveryStatus)
	r.GET("/catalog/stats", requireCatalogServiceAuth, CatalogStats)
	r.POST("/catalog/scan", requireCatalogServiceAuth, CatalogScan)
	r.GET("/catalog/search", requireCatalogServiceAuth, CatalogSearch)

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

	if e := checkDownloadBlacklist(i); e != nil {
		return e
	}
	logMessage(LogLevelInfo, "Download", "Starting download for: %s (Type: %s)", i.Name, i.Type)
	i.Started = true

	if err := routeMedia(i); err != nil {
		return err
	}
	if mediaKind(i.Name) == "audio" {
		return i.downloadMusic()
	}
	return i.downloadMedia()
}

func update(name string) {
	url := RemoteServer + "/update"
	method := "POST"

	payload := strings.NewReader(fmt.Sprintf("{\"name\": \"%s\"}", name))

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		logMessage(LogLevelError, "Webhook", "Failed to create update request for %s: %v", name, err)
		return
	}
	req.Header.Add("Content-Type", "application/json")

	authorizeBotRequest(req)
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
	if err := routeMedia(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	json.Started, json.Completed, json.InQueue = false, false, false
	isMusic := json.Type == Music || isAudioFilename(json.Name)
	if isMusic {
		token := os.Getenv("MUSIC_API_TOKEN")
		if token == "" || c.GetHeader("Authorization") != "Bearer "+token {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "music authorization required"})
			return
		}
		json.Type = Music
		json.Started, json.Completed, json.InQueue = false, false, false
		json.CompletedPercent = ""
		added, err := enqueueMusicJob(&json)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "music queue unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Music queued", "already_queued": !added})
		return
	}

	added, err := enqueueMusicJob(&json)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "download queue unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Media queued", "already_queued": !added})

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
				if errors.Is(err, errBlacklistedDownload) || reportBadMedia(j, err) {
					if e := removeMusicJob(j); e != nil {
						logMessage(LogLevelError, "Queue", "Cannot retire rejected file %d: %v", j.FileId, e)
					} else {
						forgetMusicJob(j)
					}
					logMessage(LogLevelWarn, "Validation", "Rejected invalid media; replacement feedback saved for file %d", j.FileId)
					return
				}
				logMessage(LogLevelError, "Dequeue", "Download failed for %s, re-queuing: %v", j.Name, err)
				// Re-queue on failure
				JobsMutex.Lock()
				Jobs = append(Jobs, j)
				JobsMutex.Unlock()
			} else {
				forgetMusicJob(j)
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

		url := RemoteServer + "/queue"
		method := "POST"

		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			logMessage(LogLevelError, "QueuePoller", "Failed to create request: %v (retry in %s)", err, retryDelay)
			time.Sleep(retryDelay)
			retryDelay = min(retryDelay*2, maxBackoff)
			continue
		}

		authorizeBotRequest(req)
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
		for _, item := range items {
			if item == nil {
				continue
			}
			if err := routeMedia(item); err != nil {
				logMessage(LogLevelWarn, "MediaPolicy", "Excluded file %d: %v", item.FileId, err)
				continue
			}
			// Persist every media type before acknowledging the queue claim.
			_, err := enqueueMusicJob(item)
			if err != nil {
				logMessage(LogLevelWarn, "QueuePoller", "Cannot durably queue file %d; leaving unclaimed: %v", item.FileId, err)
				continue
			}
			item.InQueue = true
			UpdateQueue(item)
		}

		if response.CatalogStatus.NeedsResync && CatalogDB != nil {
			if err := RequestCatalogReconciliation(); err != nil {
				logMessage(LogLevelWarn, "CatalogSync", "Cannot persist server resync request")
			}
		}
		// Queue polling cadence is independent of catalog synchronization.
		retryDelay = initialBackoff

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
	url := RemoteServer + "/updateQueue"
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

	authorizeBotRequest(req)
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
	RelativePath     string      `json:"relative_path,omitempty"`
	MediaName        string      `json:"media_name,omitempty"`
	MediaFileID      int64       `json:"media_file_id,omitempty"`
	URL              string      `json:"url"`
	Type             ContentType `json:"type"`
	Name             string      `json:"name"`
	FileId           int64       `json:"file_id"`
	FileSize         int64       `json:"file_size"`
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
	Music  ContentType = "music"
	Movies ContentType = "movie"
	Anime  ContentType = "anime"
	TVShow ContentType = "tvshow"
)
