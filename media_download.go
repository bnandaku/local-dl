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
	"os/exec"
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

func mediaDestination(i *Item) (string, error) {
	kind := mediaKind(i.Name)
	if kind == "video" {
		if i.MediaName != "" && safeMediaName(i.MediaName) && mediaKind(i.MediaName) == "video" && !tvShowPattern.MatchString(i.Name) {
			primary := Item{Name: i.MediaName, Type: i.Type, RelativePath: i.RelativePath}
			if err := routeMedia(&primary); err != nil {
				return "", err
			}
			path, err := videoDestination(primary.Name, primary.Type, primary.RelativePath)
			if err != nil {
				return "", err
			}
			return filepath.Join(filepath.Dir(path), "Trailers", i.Name), nil
		}
		return videoDestination(i.Name, i.Type, i.RelativePath)
	}
	if i.Type == Music {
		musicImportMutex.Lock()
		defer musicImportMutex.Unlock()
		statePath := musicEnv("MUSIC_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "music-ingest.json"))
		state, err := readMusicState(statePath)
		if err != nil {
			return "", err
		}
		primary, ok := state.Receipts[fmt.Sprint(i.MediaFileID)]
		if !ok || !isAudioFilename(primary.Name) {
			return "", fmt.Errorf("waiting for associated album audio")
		}
		root := musicEnv("MUSIC_PATH", "/mnt/music")
		rel, err := filepath.Rel(root, primary.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("associated album outside music root")
		}
		dir := filepath.Dir(primary.Path)
		if strings.HasPrefix(filepath.Base(dir), "Disc ") {
			dir = filepath.Dir(dir)
		}
		return filepath.Join(dir, i.Name), nil
	}
	primary, err := videoDestination(i.MediaName, i.Type, i.RelativePath)
	if err != nil {
		return "", err
	}
	name := i.Name
	if kind == "subtitle" {
		stem := strings.TrimSuffix(i.MediaName, filepath.Ext(i.MediaName))
		suffix := strings.TrimPrefix(i.Name, stem)
		if suffix == i.Name {
			suffix = "." + i.Name
		}
		name = strings.TrimSuffix(filepath.Base(primary), filepath.Ext(primary)) + suffix
	}
	return filepath.Join(filepath.Dir(primary), name), nil
}

func probeVideo(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-show_entries", "stream=codec_type:stream_disposition=attached_pic", "-of", "json", path)
	out := &limitedMusicOutput{max: 1 << 20}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("video validation failed")
	}
	var p struct {
		Streams []struct {
			Type        string `json:"codec_type"`
			Disposition struct {
				Attached int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out.data, &p); err != nil {
		return err
	}
	for _, s := range p.Streams {
		if s.Type == "video" && s.Disposition.Attached == 0 {
			return nil
		}
	}
	return fmt.Errorf("file contains no video stream")
}

// Publish only complete, validated downloads; never truncate an existing movie
// or expose an in-progress file to Plex's scanner.
func (i *Item) downloadMedia() error {
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
	if i.Type == Music {
		musicImportMutex.Lock()
		statePath := musicEnv("MUSIC_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "music-ingest.json"))
		state, e := readMusicState(statePath)
		if e == nil && i.FileId > 0 {
			state.Receipts[fmt.Sprint(i.FileId)] = musicImportRecord{FileID: i.FileId, Name: i.Name, Path: target, SHA256: digest}
			e = saveMusicState(statePath, state)
		}
		musicImportMutex.Unlock()
		if e != nil {
			return e
		}
		if err = removeMusicJob(i); err != nil {
			return err
		}
		scheduleMusicAckRetry()
		TriggerMusicSync()
	} else {
		i.Completed = true
		if mediaKind(i.Name) == "video" {
			update(filepath.Base(target))
		}
		i.CompletedPercent = "100"
		UpdateQueue(i)
		if mediaKind(i.Name) == "video" {
			if err := AddFileToCatalog(target); err != nil {
				logMessage(LogLevelWarn, "Catalog", "Cannot catalog media: %v", err)
			}
		}
	}
	logMessage(LogLevelInfo, "Media", "Verified media saved: %s", target)
	return nil
}
