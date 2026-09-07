package main

import (
	"crypto/subtle"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// Titles and filenames are private operational data; require the shared service token.
func recoveryStatus(c *gin.Context) {
	token := os.Getenv("BOT_SERVICE_TOKEN")
	if token == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+token)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "service authorization required"})
		return
	}
	recoveryMonitorMu.Lock()
	state, err := loadRecoveryMonitor()
	recoveryMonitorMu.Unlock()
	if err != nil {
		c.JSON(503, gin.H{"error": "recovery state unreadable; preserved for repair"})
		return
	}
	linkFeedbackMu.Lock()
	feedback, err := readLinkFeedback()
	linkFeedbackMu.Unlock()
	if err != nil {
		c.JSON(503, gin.H{"error": "recovery feedback unreadable; preserved for repair"})
		return
	}
	archives, archiveErr := loadArchiveState()
	archiveError := ""
	if archiveErr != nil {
		archiveError = "Archive state unreadable; preserved for repair"
	}
	catalog, catalogErr := readCatalogSyncState()
	catalogError := ""
	if catalogErr != nil {
		catalogError = "catalog state unavailable"
	}
	c.JSON(200, gin.H{"catalog_sync": catalog, "catalog_sync_error": catalogError, "archives": archives, "archive_error": archiveError, "monitor": state, "pending_file_feedback": feedback, "unidentified_action": "Identify the title and media type, check the library, then create a legacy recovery request. No source URL is inferred."})
}
