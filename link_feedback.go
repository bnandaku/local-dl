package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("bot feedback returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return resp.StatusCode, e
}
func retryLinkFeedback() {
	if os.Getenv("BOT_SERVICE_TOKEN") == "" {
		return
	}
	linkFeedbackMu.Lock()
	m, e := readLinkFeedback()
	linkFeedbackMu.Unlock()
	if e != nil {
		return
	}
	for key, f := range m {
		var a struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		}
		status, e := botLinkCall("GET", "/api/v1/link-files/"+key, nil, &a)
		if status == 404 {
			deleteFeedback(key, f)
			continue
		}
		if e != nil {
			continue
		}
		_, e = botLinkCall("POST", fmt.Sprintf("/api/v1/links/%d/report", a.ID), f, &a)
		if e != nil {
			continue
		}
		if f.Status == "bad" {
			_, e = botLinkCall("POST", fmt.Sprintf("/api/v1/links/%d/retry", a.ID), map[string]string{}, nil)
			if e != nil {
				continue
			}
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
	if status == 404 {
		return nil
	}
	if e != nil {
		return e
	}
	if a.Status == "bad" {
		return errBlacklistedDownload
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
