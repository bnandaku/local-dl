package main

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// CatalogStats returns statistics about the catalog
func CatalogStats(c *gin.Context) {
	if CatalogDB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Catalog not initialized"})
		return
	}

	stats, err := GetCatalogStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, stats)
}

// CatalogScan triggers a full catalog scan
func CatalogScan(c *gin.Context) {
	if CatalogDB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Catalog not initialized"})
		return
	}

	if err := RequestCatalogReconciliation(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cannot persist catalog reconciliation"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Catalog reconciliation queued"})
}

// CatalogSearch searches the catalog
func CatalogSearch(c *gin.Context) {
	if CatalogDB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Catalog not initialized"})
		return
	}

	// Get query parameters
	showName := c.Query("show")
	season := c.Query("season")
	episode := c.Query("episode")
	status := c.Query("status")

	if status == "" {
		status = "active"
	}

	// Build query
	query := "SELECT file_path, filename, show_name, season, episode, file_size, modified_at, status FROM files WHERE status = ?"
	args := []interface{}{status}

	if showName != "" {
		query += " AND show_name LIKE ?"
		args = append(args, "%"+showName+"%")
	}

	if season != "" {
		query += " AND season = ?"
		args = append(args, season)
	}

	if episode != "" {
		query += " AND episode = ?"
		args = append(args, episode)
	}

	query += " ORDER BY show_name, season, episode LIMIT 100"

	rows, err := CatalogDB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	results := []gin.H{}
	for rows.Next() {
		var filePath, filename, showNameResult, seasonResult, episodeResult, statusResult string
		var fileSize int64
		var modifiedAt string

		err := rows.Scan(&filePath, &filename, &showNameResult, &seasonResult, &episodeResult, &fileSize, &modifiedAt, &statusResult)
		if err != nil {
			continue
		}

		results = append(results, gin.H{
			"file_path":    filePath,
			"filename":     filename,
			"show_name":    showNameResult,
			"season":       seasonResult,
			"episode":      episodeResult,
			"file_size":    fileSize,
			"file_size_mb": fmt.Sprintf("%.2f", float64(fileSize)/(1024*1024)),
			"modified_at":  modifiedAt,
			"status":       statusResult,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"count":   len(results),
		"results": results,
	})
}

func requireCatalogServiceAuth(c *gin.Context) {
	token := os.Getenv("BOT_SERVICE_TOKEN")
	if token == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+token)) != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "service authorization required"})
		return
	}
	c.Next()
}
