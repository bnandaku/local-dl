package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

var musicAckMutex sync.Mutex

var musicAckTrigger = make(chan struct{}, 1)

func scheduleMusicAckRetry() {
	select {
	case musicAckTrigger <- struct{}{}:
	default:
	}
}

// Source deletion is triggered by the upstream completion endpoint. Keep a
// durable receipt until that endpoint accepts it; retries only use verified files.
func retryMusicAcknowledgements() {
	musicAckMutex.Lock()
	defer musicAckMutex.Unlock()
	statePath := musicEnv("MUSIC_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "music-ingest.json"))
	musicImportMutex.Lock()
	state, err := readMusicState(statePath)
	musicImportMutex.Unlock()
	if err != nil {
		logMessage(LogLevelWarn, "Music", "Cannot read pending receipts: %v", err)
		return
	}
	for key, record := range state.Receipts {
		if record.Acknowledged || record.FileID <= 0 {
			continue
		}
		digest, err := musicFileDigest(record.Path)
		if err != nil || digest != record.SHA256 {
			continue
		}
		kind := record.Type
		if kind == "" {
			kind = Music
		}
		item := Item{Name: record.Name, FileId: record.FileID, Type: kind, Completed: true, CompletedPercent: "100", Started: true, InQueue: true}
		if err := sendMusicAcknowledgement(item); err != nil {
			logMessage(LogLevelWarn, "Music", "Source receipt for file %d will retry: %v", record.FileID, err)
			continue
		}
		musicImportMutex.Lock()
		current, err := readMusicState(statePath)
		if err == nil {
			if r, ok := current.Receipts[key]; ok && r.SHA256 == record.SHA256 && r.FileID == record.FileID {
				r.Acknowledged = true
				current.Receipts[key] = r
				if f, ok := current.Files[r.Path]; ok && f.FileID == r.FileID {
					f.Acknowledged = true
					current.Files[r.Path] = f
				}
				err = saveMusicState(statePath, current)
			}
		}
		musicImportMutex.Unlock()
		if err != nil {
			logMessage(LogLevelWarn, "Music", "Cannot save source receipt for file %d: %v", record.FileID, err)
		}
	}
}

func sendMusicAcknowledgement(item Item) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodPost, RemoteServer+"/updateQueue", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid completion server URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("completion server unavailable")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("completion server returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func InitMusicIngest(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			retryMusicAcknowledgements()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-musicAckTrigger:
			}
		}
	}()
}
