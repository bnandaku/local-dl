package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func videoDestination(name string, kind ContentType, relative string) (string, error) {
	if kind == TVShow {
		info := parseTVShowInfo(name)
		if !info.HasSeasonInfo {
			parts := strings.Split(relative, "/")
			for j := len(parts) - 2; j >= 0; j-- {
				if tvShowPattern.MatchString(parts[j]) {
					info = parseTVShowInfo(parts[j] + "." + name)
					break
				}
			}
		}
		if info.HasSeasonInfo {
			return filepath.Join(TVShowPath, musicComponent(info.ShowName, "Unknown Show"), "Season_"+info.Season, info.StandardName), nil
		}
		// Preserve TV context even when an individual filename omits episode numbers.
		dir := filepath.Dir(relative)
		if dir == "." {
			dir = "Unsorted"
		}
		return filepath.Join(TVShowPath, dir, name), nil
	}
	info := parseMovieInfo(name)
	folder := info.Title
	if info.Year != "" {
		folder += " (" + info.Year + ")"
	}
	return filepath.Join(MoviesPath, musicComponent(folder, strings.TrimSuffix(name, filepath.Ext(name))), name), nil
}

func verifiedPrimary(i *Item) (musicImportRecord, error) {
	musicImportMutex.Lock()
	statePath := musicEnv("MUSIC_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "music-ingest.json"))
	state, err := readMusicState(statePath)
	musicImportMutex.Unlock()
	if err != nil {
		return musicImportRecord{}, err
	}
	p, ok := state.Receipts[fmt.Sprint(i.MediaFileID)]
	expected := "video"
	root := MoviesPath
	if i.Type == Music {
		expected = "audio"
		root = musicEnv("MUSIC_PATH", "/mnt/music")
	} else if i.Type == TVShow {
		root = TVShowPath
	}
	if !ok || i.MediaFileID <= 0 || p.Name != i.MediaName || mediaKind(p.Name) != expected {
		return p, fmt.Errorf("waiting for verified primary media")
	}
	rel, e := filepath.Rel(root, p.Path)
	if e != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return p, fmt.Errorf("primary is outside expected library")
	}
	digest, e := musicFileDigest(p.Path)
	if e != nil || digest != p.SHA256 {
		return p, fmt.Errorf("primary media missing or changed")
	}
	return p, nil
}

func mediaDestination(i *Item) (string, error) {
	kind := mediaKind(i.Name)
	if kind == "video" && i.MediaName == "" {
		return videoDestination(i.Name, i.Type, i.RelativePath)
	}
	p, err := verifiedPrimary(i)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(p.Path)
	if i.Type == Music {
		if strings.HasPrefix(filepath.Base(dir), "Disc ") {
			dir = filepath.Dir(dir)
		}
		return filepath.Join(dir, i.Name), nil
	}
	if kind == "video" {
		return filepath.Join(dir, "Trailers", i.Name), nil
	}
	name := i.Name
	if kind == "subtitle" {
		stem := strings.TrimSuffix(i.MediaName, filepath.Ext(i.MediaName))
		suffix := i.Name
		if strings.HasPrefix(strings.ToLower(i.Name), strings.ToLower(stem)) {
			suffix = i.Name[len(stem):]
		} else {
			suffix = "." + i.Name
		}
		name = strings.TrimSuffix(filepath.Base(p.Path), filepath.Ext(p.Path)) + suffix
	}
	return filepath.Join(dir, name), nil
}

// Publish only complete, validated downloads; never truncate an existing movie
// or expose an in-progress file to Plex's scanner.
func (i *Item) downloadMedia() error {
	if record, ok := musicReceiptFor(i); ok && record.Name == i.Name && record.Type == i.Type {
		if digest, err := musicFileDigest(record.Path); err == nil && digest == record.SHA256 {
			if err := removeMusicJob(i); err != nil {
				return err
			}
			i.Completed, i.CompletedPercent = true, "100"
			scheduleMusicAckRetry()
			return nil
		}
	}

	target, err := mediaDestination(i)
	if err != nil {
		return err
	}
	if err = musicMkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".media-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.URL, nil)
	if err != nil {
		return fmt.Errorf("invalid media URL")
	}
	resp, err := (&http.Client{Timeout: 6 * time.Hour}).Do(req)
	if err != nil {
		return fmt.Errorf("media request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return failedDownloadError{"file_not_found"}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("media HTTP %d", resp.StatusCode)
	}
	expected := i.FileSize
	if expected <= 0 {
		expected = resp.ContentLength
	}
	if expected <= 0 || resp.ContentLength > 0 && resp.ContentLength != expected {
		return fmt.Errorf("media size unavailable or inconsistent")
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, expected+1))
	if err != nil || n != expected {
		return fmt.Errorf("media download incomplete")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = detectDownloadFailure(f.Name()); err != nil {
		return err
	}
	if mediaKind(i.Name) == "subtitle" {
		input, e := os.Open(f.Name())
		if e != nil {
			return e
		}
		data, e := io.ReadAll(io.LimitReader(input, 65537))
		input.Close()
		if e != nil {
			return e
		}
		text := strings.TrimSpace(string(data))
		lower := strings.ToLower(text)
		var payload map[string]json.RawMessage
		jsonError := json.Unmarshal(data, &payload) == nil && (payload["error_type"] != nil || payload["error_message"] != nil)
		if text == "" || strings.HasPrefix(text, "MZ") || strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") || jsonError {
			return fmt.Errorf("subtitle contains a non-media error payload")
		}
	}
	if mediaKind(i.Name) == "video" {
		if err = probeVideo(f.Name()); err != nil {
			return err
		}
	}
	if mediaKind(i.Name) == "art" {
		b := make([]byte, 512)
		in, e := os.Open(f.Name())
		if e != nil {
			return e
		}
		n, _ := in.Read(b)
		in.Close()
		if !strings.HasPrefix(http.DetectContentType(b[:n]), "image/") {
			return fmt.Errorf("artwork is not an image")
		}
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	target, err = publishMusicFile(f.Name(), target, digest)
	if err != nil {
		return err
	}
	musicImportMutex.Lock()
	statePath := musicEnv("MUSIC_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "music-ingest.json"))
	state, e := readMusicState(statePath)
	if e == nil && i.FileId > 0 {
		state.Receipts[fmt.Sprint(i.FileId)] = musicImportRecord{Type: i.Type, FileID: i.FileId, Name: i.Name, Path: target, SHA256: digest}
		e = saveMusicState(statePath, state)
	}
	musicImportMutex.Unlock()
	if e != nil {
		return e
	}
	if err = removeMusicJob(i); err != nil {
		return err
	}
	if i.Type == Music {
		TriggerMusicSync()
	}
	if mediaKind(i.Name) == "video" {
		if err := queueLinkFeedback(i, "validated", ""); err != nil {
			return err
		}
	}
	i.Completed = true
	i.CompletedPercent = "100"
	scheduleMusicAckRetry()
	if mediaKind(i.Name) == "video" {
		update(filepath.Base(target))
		if err := AddPublishedFileToCatalog(target); err != nil {
			logMessage(LogLevelWarn, "Catalog", "Cannot catalog media: %v", err)
		}
	}
	logMessage(LogLevelInfo, "Media", "Verified media saved: %s", target)
	return nil
}
