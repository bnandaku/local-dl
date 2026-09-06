package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type recoveryMonitorState struct {
	Transfers   map[int64]string `json:"reported_transfers"`
	Unknown     map[int64]string `json:"unidentified_transfers"`
	LastError   string           `json:"last_error,omitempty"`
	LastChecked time.Time        `json:"last_checked"`
}

var recoveryMonitorMu sync.Mutex

func recoveryMonitorPath() string {
	return musicEnv("RECOVERY_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "recovery-state.json"))
}
func loadRecoveryMonitor() (recoveryMonitorState, error) {
	s := recoveryMonitorState{Transfers: map[int64]string{}, Unknown: map[int64]string{}}
	data, e := os.ReadFile(recoveryMonitorPath())
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(data, &s)
	if s.Transfers == nil {
		s.Transfers = map[int64]string{}
	}
	if s.Unknown == nil {
		s.Unknown = map[int64]string{}
	}
	return s, e
}
func saveRecoveryMonitor(s recoveryMonitorState) error {
	p := recoveryMonitorPath()
	if e := musicMkdirAll(filepath.Dir(p)); e != nil {
		return e
	}
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".recovery-*")
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

type failedRecoveryTransfer struct {
	ID      int64         `json:"transfer_id"`
	Title   string        `json:"title"`
	Status  string        `json:"status"`
	Reason  string        `json:"reason"`
	Tracked bool          `json:"tracked"`
	Attempt *recoveryLink `json:"attempt"`
}

func pollFailedTransfers(s *recoveryMonitorState) error {
	after := int64(0)
	var failures []error
	for pageNumber := 0; pageNumber < 1000; pageNumber++ {
		var page struct {
			Items []failedRecoveryTransfer `json:"items"`
			Next  int64                    `json:"next_after"`
		}
		if _, e := botLinkCall("GET", fmt.Sprintf("/api/v1/transfers/failed?after=%d&limit=100", after), nil, &page); e != nil {
			return e
		}
		if page.Items == nil {
			return fmt.Errorf("invalid failed-transfer page")
		}
		for _, transfer := range page.Items {
			if transfer.ID <= after || transfer.Status != "ERROR" {
				return fmt.Errorf("invalid failed-transfer identity or state")
			}
			if !transfer.Tracked {
				if _, known := s.Unknown[transfer.ID]; known {
					continue
				}
				var out struct {
					Transfer failedRecoveryTransfer `json:"transfer"`
					Action   string                 `json:"next_action"`
				}
				_, e := botLinkCall("POST", fmt.Sprintf("/api/v1/transfers/%d/report", transfer.ID), map[string]string{"event_id": fmt.Sprintf("local-dl:transfer-failed:%d", transfer.ID)}, &out)
				if e != nil {
					failures = append(failures, e)
					continue
				}
				if out.Transfer.ID != transfer.ID || out.Transfer.Tracked || out.Action != "create_legacy_recovery" {
					failures = append(failures, fmt.Errorf("invalid unknown-transfer receipt"))
					continue
				}
				s.Unknown[transfer.ID] = transfer.Title
				if e := saveRecoveryMonitor(*s); e != nil {
					return e
				}
				logMessage(LogLevelWarn, "Recovery", "Failed transfer %d has no source history; identify it through /recovery/status before requesting replacement", transfer.ID)
				continue
			}
			if transfer.Attempt == nil || transfer.Attempt.ID <= 0 || transfer.Attempt.Group == "" {
				return fmt.Errorf("invalid tracked transfer mapping")
			}
			marker := fmt.Sprintf("%d:%s", transfer.Attempt.ID, transfer.Reason)
			if s.Transfers[transfer.ID] != marker {
				var out struct {
					Transfer failedRecoveryTransfer `json:"transfer"`
					Action   string                 `json:"next_action"`
				}
				_, e := botLinkCall("POST", fmt.Sprintf("/api/v1/transfers/%d/report", transfer.ID), map[string]string{"event_id": fmt.Sprintf("local-dl:transfer-failed:%d", transfer.ID)}, &out)
				if e != nil {
					failures = append(failures, e)
					continue
				}
				if out.Transfer.ID != transfer.ID || out.Action != "retry_request" {
					return fmt.Errorf("invalid transfer failure receipt")
				}
				s.Transfers[transfer.ID] = marker
				if e = saveRecoveryMonitor(*s); e != nil {
					return e
				}
			}
			// Authentication, quota, network and storage failures remain available for repair.
			if transfer.Reason == "transfer_failed" {
				if e := discardFailedTransfer(*transfer.Attempt, transfer.ID); e != nil {
					failures = append(failures, e)
					continue
				}
			}
			// The server retains operational failures and enforces candidate reservations.
			if e := retryRecoveryRequest(transfer.Attempt.Group); e != nil {
				failures = append(failures, e)
			}
		}
		if page.Next == 0 {
			return errors.Join(failures...)
		}
		if page.Next <= after {
			return fmt.Errorf("failed-transfer pagination did not advance")
		}
		after = page.Next
	}
	return fmt.Errorf("failed-transfer pagination exceeds limit")
}
func reconcileRecoveryGroups() error {
	after := int64(0)
	var failures []error
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < 1000; pageNumber++ {
		var page struct {
			Items []recoveryLink `json:"items"`
			Next  int64          `json:"next_after"`
		}
		if _, e := botLinkCall("GET", fmt.Sprintf("/api/v1/links?after=%d&limit=100", after), nil, &page); e != nil {
			return e
		}
		if page.Items == nil {
			return fmt.Errorf("invalid link history page")
		}
		for _, a := range page.Items {
			if a.ID <= after || a.Group == "" {
				return fmt.Errorf("invalid link history identity")
			}
			if e := cleanupUnwantedFiles(a); e != nil {
				failures = append(failures, e)
				continue
			}
			if e := confirmRecovery(a); e != nil {
				failures = append(failures, e)
			}
			if (a.Status == "bad" || a.Status == "failed") && !seen[a.Group] {
				seen[a.Group] = true
				if e := retryRecoveryRequest(a.Group); e != nil {
					failures = append(failures, e)
				}
			}
		}
		if page.Next == 0 {
			return errors.Join(failures...)
		}
		if page.Next <= after {
			return fmt.Errorf("history pagination did not advance")
		}
		after = page.Next
	}
	return fmt.Errorf("link history pagination exceeds limit")
}
func recoveryMonitorCycle() {
	recoveryMonitorMu.Lock()
	s, e := loadRecoveryMonitor()
	recoveryMonitorMu.Unlock()
	if e != nil {
		logMessage(LogLevelError, "Recovery", "Cannot read recovery state; preserving it: %v", e)
		return
	}
	e = requireRecoveryContract()
	if e == nil {
		e = errors.Join(pollFailedTransfers(&s), reconcileRecoveryGroups())
	}
	s.LastChecked = time.Now().UTC()
	s.LastError = ""
	if e != nil {
		s.LastError = e.Error()
	}
	recoveryMonitorMu.Lock()
	defer recoveryMonitorMu.Unlock()
	if e := saveRecoveryMonitor(s); e != nil {
		logMessage(LogLevelError, "Recovery", "Cannot persist recovery monitor state: %v", e)
	}
}
func runRecoveryMonitor(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		recoveryMonitorCycle()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
