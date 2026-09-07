package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type archiveReceipt struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	LibraryType string `json:"library_type"`
	LibraryKey  string `json:"library_key"`
}
type archiveJob struct {
	ID            string                    `json:"archive_id"`
	AttemptID     int64                     `json:"attempt_id"`
	RequestID     string                    `json:"request_id"`
	Receipts      map[string]archiveReceipt `json:"receipts"`
	Pending       *archiveReport            `json:"pending_report,omitempty"`
	PendingOutput string                    `json:"pending_output,omitempty"`
	Done          bool                      `json:"done"`
	LastError     string                    `json:"last_error,omitempty"`
	Updated       time.Time                 `json:"updated"`
}
type archiveWorkerState struct {
	Jobs        map[string]*archiveJob `json:"jobs"`
	LastError   string                 `json:"last_error,omitempty"`
	LastChecked time.Time              `json:"last_checked"`
}

var archiveStateMu sync.Mutex

func archiveStatePath() string {
	return musicEnv("ARCHIVE_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "archive-jobs.json"))
}
func archiveStagingRoot() string {
	return musicEnv("ARCHIVE_STAGING_PATH", filepath.Join(filepath.Dir(archiveStatePath()), "archive-staging"))
}
func newArchiveState() archiveWorkerState { return archiveWorkerState{Jobs: map[string]*archiveJob{}} }
func loadArchiveState() (archiveWorkerState, error) {
	archiveStateMu.Lock()
	defer archiveStateMu.Unlock()
	s := newArchiveState()
	b, e := os.ReadFile(archiveStatePath())
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil || s.Jobs == nil {
		return s, fmt.Errorf("archive state unreadable; preserve for repair")
	}
	for k, j := range s.Jobs {
		if j == nil || j.ID != k || !safeArchiveID(k) {
			return s, fmt.Errorf("invalid saved archive job")
		}
		if j.Receipts == nil {
			j.Receipts = map[string]archiveReceipt{}
		}
	}
	return s, nil
}
func saveArchiveState(s archiveWorkerState) error {
	archiveStateMu.Lock()
	defer archiveStateMu.Unlock()
	p := archiveStatePath()
	if e := musicMkdirAll(filepath.Dir(p)); e != nil {
		return e
	}
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".archive-state-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), p); e != nil {
		return e
	}
	return syncMusicDirectory(filepath.Dir(p))
}
