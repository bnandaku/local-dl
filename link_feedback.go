package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Content errors are raised only after a complete transfer and a successful
// media probe. Missing tooling, probe timeouts and network failures are not bad media.
type badMediaError struct{ reason string }
type failedDownloadError struct{ reason string }

func (e failedDownloadError) Error() string { return "download failed: " + e.reason }

func (e badMediaError) Error() string { return "content validation failed: " + e.reason }

type linkFeedback struct {
	FileID int64  `json:"file_id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

var linkFeedbackMu sync.Mutex

func feedbackPath() string {
	return musicEnv("LINK_FEEDBACK_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "link-feedback.json"))
}
func readLinkFeedback() (map[string]linkFeedback, error) {
	m := map[string]linkFeedback{}
	b, e := os.ReadFile(feedbackPath())
	if os.IsNotExist(e) {
		return m, nil
	}
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal(b, &m)
	return m, e
}
func saveLinkFeedback(m map[string]linkFeedback) error {
	p := feedbackPath()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".link-feedback-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(f.Name(), p)
	}
	if e == nil {
		e = syncMusicDirectory(filepath.Dir(p))
	}
	return e
}
func queueLinkFeedback(i *Item, status, reason string) error {
	if os.Getenv("BOT_SERVICE_TOKEN") == "" {
		return nil
	}
	if i.FileId <= 0 {
		return nil
	}
	linkFeedbackMu.Lock()
	defer linkFeedbackMu.Unlock()
	m, e := readLinkFeedback()
	if e != nil {
		return e
	}
	key := strconv.FormatInt(i.FileId, 10)
	if old, ok := m[key]; ok && old.Status == "bad" {
		return nil
	}
	m[key] = linkFeedback{i.FileId, status, reason}
	return saveLinkFeedback(m)
}
func botLinkCall(method, path string, body interface{}, out interface{}) (int, error) {
	b, e := json.Marshal(body)
	if e != nil {
		return 0, e
	}
	req, e := http.NewRequest(method, RemoteServer+path, bytes.NewReader(b))
	if e != nil {
		return 0, e
	}
	req.Header.Set("Content-Type", "application/json")
	authorizeBotRequest(req)
	client := &http.Client{Timeout: 4 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return 0, fmt.Errorf("bot feedback connection failed")
	}
	defer resp.Body.Close()
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return resp.StatusCode, fmt.Errorf("bot did not return a JSON API response")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return resp.StatusCode, fmt.Errorf("bot response exceeds limit or is incomplete")
	}
	if resp.StatusCode/100 != 2 {
		var failure struct {
			Code      string `json:"code"`
			Message   string `json:"error"`
			Retryable *bool  `json:"retryable"`
		}
		if json.Unmarshal(data, &failure) != nil || failure.Code == "" || failure.Message == "" || failure.Retryable == nil {
			return resp.StatusCode, fmt.Errorf("invalid bot API error response (HTTP %d)", resp.StatusCode)
		}
		return resp.StatusCode, &botAPIError{Status: resp.StatusCode, Code: failure.Code, Retryable: *failure.Retryable}
	}
	if out != nil {
		e = json.Unmarshal(data, out)
	}

	return resp.StatusCode, e
}
func retryLinkFeedback() {
	if requireRecoveryContract() != nil {
		return
	}
	linkFeedbackMu.Lock()
	pending, e := readLinkFeedback()
	linkFeedbackMu.Unlock()
	if e != nil {
		return
	}
	for key, f := range pending {
		if f.Status == "validated" {
			// Successful publication receipts drive validation, scoped cleanup and confirmation.
			record, ok := musicReceiptFor(&Item{FileId: f.FileID})
			if !ok {
				continue
			}
			if _, e := recoverPublishedFile(record); e == nil {
				deleteFeedback(key, f)
			}
			continue
		}
		var a recoveryLink
		code, e := botLinkCall("GET", "/api/v1/link-files/"+key, nil, &a)
		if code == 404 {
			continue
		} // Preserve untracked failures for explicit legacy recovery.
		if e != nil || a.ID <= 0 || a.Group == "" {
			continue
		}
		a, e = reportRecoveryFile(a, f)
		if e != nil {
			continue
		}
		if e = cleanupRecoveryFile(a, f.FileID, false, f.Reason); e != nil {
			continue
		}
		if e = retryRecoveryRequest(a.Group); e != nil {
			continue
		}
		deleteFeedback(key, f)
	}
}

func deleteFeedback(key string, f linkFeedback) {
	linkFeedbackMu.Lock()
	defer linkFeedbackMu.Unlock()
	m, e := readLinkFeedback()
	if e != nil {
		return
	}
	if m[key] == f {
		delete(m, key)
		_ = saveLinkFeedback(m)
	}
}
func reportBadMedia(i *Item, err error) bool {
	if os.Getenv("BOT_SERVICE_TOKEN") == "" {
		return false
	}
	var failed failedDownloadError
	if errors.As(err, &failed) {
		return queueLinkFeedback(i, "failed", failed.reason) == nil
	}
	var bad badMediaError
	if !errors.As(err, &bad) {
		return false
	}
	if e := queueLinkFeedback(i, "bad", bad.reason); e != nil {
		logMessage(LogLevelError, "Validation", "Could not persist bad-media feedback: %v", e)
		return false
	}
	return true
}

// Only explicit parser diagnostics classify a failed ffprobe invocation as
// corrupt input; missing tools, permissions and timeouts remain operational errors.
func invalidProbeInput(stderr string) bool {
	text := strings.ToLower(stderr)
	for _, pattern := range []string{"invalid data found when processing input", "moov atom not found"} {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

var errBlacklistedDownload = errors.New("release has been blacklisted")

func checkDownloadBlacklist(i *Item) error {
	if os.Getenv("BOT_SERVICE_TOKEN") == "" || i.FileId <= 0 {
		return nil
	}
	var a struct {
		Status string `json:"status"`
	}
	status, e := botLinkCall("GET", fmt.Sprintf("/api/v1/link-files/%d", i.FileId), nil, &a)
	if status == 404 && isBotNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if a.Status == "bad" {
		var link recoveryLink
		if _, e := botLinkCall("GET", fmt.Sprintf("/api/v1/link-files/%d", i.FileId), nil, &link); e != nil {
			return e
		}
		var page struct {
			Items []recoveryFile `json:"items"`
		}
		if _, e := botLinkCall("GET", fmt.Sprintf("/api/v1/links/%d/files", link.ID), nil, &page); e != nil {
			return e
		}
		for _, f := range page.Items {
			if f.ID == i.FileId && (f.Report == "invalid" || f.Cleanup == "discarded") {
				return errBlacklistedDownload
			}
		}
	}
	return nil
}
func runLinkFeedback(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		retryLinkFeedback()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Keep server messages out of logs; they can contain source URLs or credentials.
type botAPIError struct {
	Status    int
	Code      string
	Retryable bool
}

func (e *botAPIError) Error() string {
	return fmt.Sprintf("bot API returned HTTP %d (%s)", e.Status, e.Code)
}
func isBotNotFound(err error) bool {
	var e *botAPIError
	return errors.As(err, &e) && e.Status == 404 && e.Code == "not_found" && !e.Retryable
}
