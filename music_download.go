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
	"sync"
	"time"
)

var musicImportMutex sync.Mutex

type musicImportRecord struct {
	Type         ContentType   `json:"type,omitempty"`
	FileID       int64         `json:"file_id"`
	Name         string        `json:"name"`
	Acknowledged bool          `json:"acknowledged"`
	Path         string        `json:"path"`
	SHA256       string        `json:"sha256"`
	Metadata     MusicMetadata `json:"metadata"`
}
type musicImportState struct {
	Albums   map[string]string            `json:"albums"`
	Files    map[string]musicImportRecord `json:"files"`
	Receipts map[string]musicImportRecord `json:"receipts"`
}

func musicEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func (i *Item) downloadMusic() error {
	if record, ok := musicReceiptFor(i); ok {
		if digest, err := musicFileDigest(record.Path); err == nil && digest == record.SHA256 {
			i.Type, i.Completed, i.CompletedPercent = Music, true, "100"
			if err := removeMusicJob(i); err != nil {
				return err
			}
			TriggerMusicSync()
			scheduleMusicAckRetry()
			return nil
		}
	}
	root := musicEnv("MUSIC_PATH", "/mnt/music")
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("music destination unavailable; configure the music mount")
	}
	staging := musicEnv("MUSIC_STAGING_PATH", filepath.Join(root, ".incoming"))
	if err := musicMkdirAll(staging); err != nil {
		return err
	}
	f, err := os.CreateTemp(staging, "download-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.URL, nil)
	if err != nil {
		return fmt.Errorf("invalid music download URL")
	}
	// Error strings from net/http contain the URL, which may contain put.io credentials.
	client := &http.Client{Timeout: 6 * time.Hour}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("music download request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return failedDownloadError{"file_not_found"}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("music download returned HTTP %d", resp.StatusCode)
	}
	expected := i.FileSize
	if expected <= 0 {
		expected = resp.ContentLength
	}
	if expected <= 0 || (resp.ContentLength > 0 && resp.ContentLength != expected) {
		return fmt.Errorf("music source size unavailable or inconsistent")
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, expected+1))
	if err != nil {
		return fmt.Errorf("music download incomplete: %w", err)
	}
	if written != expected {
		return fmt.Errorf("music download length mismatch")
	}
	i.FileSize = expected
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err = detectDownloadFailure(f.Name()); err != nil {
		return err
	}
	metadata, err := readMusicMetadata(f.Name())
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	path, err := publishMusic(root, f.Name(), i, metadata, digest)
	if err != nil {
		return err
	}
	if err := removeMusicJob(i); err != nil {
		return fmt.Errorf("music queue cleanup failed: %w", err)
	}
	i.Type = Music
	i.CompletedPercent = "100"
	if err := queueLinkFeedback(i, "validated", ""); err != nil {
		return err
	}
	i.Completed = true
	logMessage(LogLevelInfo, "Music", "Verified music saved: %s", path)
	// Queue completion is the existing file_id based acknowledgement. The source
	// must retain failed downloads; indexing is retried separately after placement.
	TriggerMusicSync()
	scheduleMusicAckRetry()
	return nil
}

func publishMusic(root, staged string, item *Item, metadata MusicMetadata, digest string) (string, error) {
	musicImportMutex.Lock()
	defer musicImportMutex.Unlock()
	statePath := musicEnv("MUSIC_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "music-ingest.json"))
	state, err := readMusicState(statePath)
	if err != nil {
		return "", err
	}
	genre := primaryMusicGenre(metadata)
	albumKey := strings.ToLower(strings.TrimSpace(metadata.AlbumArtist)) + "\x00" + strings.ToLower(strings.TrimSpace(metadata.Album))
	if metadata.Album != "" && metadata.AlbumArtist != "" {
		if prior, ok := state.Albums[albumKey]; ok {
			genre = prior
		} else {
			state.Albums[albumKey] = genre
		}
	}
	path := filepath.Join(root, musicRelativePath(metadata, item.Name, genre))
	if err := musicMkdirAll(filepath.Dir(path)); err != nil {
		return "", err
	}
	path, err = publishMusicFile(staged, path, digest)
	if err != nil {
		return "", err
	}
	state.Files[path] = musicImportRecord{FileID: item.FileId, Name: item.Name, Path: path, SHA256: digest, Metadata: metadata}
	if item.FileId > 0 {
		state.Receipts[fmt.Sprint(item.FileId)] = musicImportRecord{FileID: item.FileId, Name: item.Name, Path: path, SHA256: digest, Metadata: metadata}
	}
	if err := saveMusicState(statePath, state); err != nil {
		return "", err
	}
	return path, nil
}

func readMusicState(path string) (musicImportState, error) {
	s := musicImportState{Albums: map[string]string{}, Files: map[string]musicImportRecord{}, Receipts: map[string]musicImportRecord{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("music state is invalid; preserve it for recovery: %w", err)
	}
	if s.Albums == nil {
		s.Albums = map[string]string{}
	}
	if s.Files == nil {
		s.Files = map[string]musicImportRecord{}
	}
	if s.Receipts == nil {
		s.Receipts = map[string]musicImportRecord{}
	}
	for path, record := range s.Files {
		if record.FileID > 0 {
			if _, ok := s.Receipts[fmt.Sprint(record.FileID)]; !ok {
				s.Receipts[fmt.Sprint(record.FileID)] = record
			}
		}
		if record.Path == "" {
			record.Path = path
			s.Files[path] = record
		}
	}
	return s, nil
}

func musicReceiptFor(item *Item) (musicImportRecord, bool) {
	statePath := musicEnv("MUSIC_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "music-ingest.json"))
	musicImportMutex.Lock()
	defer musicImportMutex.Unlock()
	state, err := readMusicState(statePath)
	if err != nil {
		return musicImportRecord{}, false
	}
	record, ok := state.Receipts[fmt.Sprint(item.FileId)]
	return record, ok
}

func saveMusicState(path string, state musicImportState) error {
	if err := musicMkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".music-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncMusicDirectory(filepath.Dir(path))
}

// Walk existing parents without following symlinks, including the final directory.
func musicMkdirAll(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = nativeStoragePath(abs)
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0755); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("music path contains a symlink or non-directory: %s", current)
		}
	}
	return nil
}

func musicFileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("music destination is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func publishMusicFile(staged, target, digest string) (string, error) {
	// Copy into a temporary file on the target filesystem, then link atomically
	// without replacing any existing destination (works across staging mounts).
	in, err := os.Open(staged)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(target), ".music-*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return "", fmt.Errorf("music staging file changed before publication")
	}
	if err := out.Chmod(0644); err != nil {
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := os.Link(out.Name(), target); err == nil {
			if err := syncMusicDirectory(filepath.Dir(target)); err != nil {
				return "", err
			}
			return target, nil
		} else if !os.IsExist(err) {
			return "", err
		}
		prior, err := musicFileDigest(target)
		if err != nil {
			return "", err
		}
		if prior == digest {
			return target, nil
		}
		ext := filepath.Ext(target)
		target = strings.TrimSuffix(target, ext) + " [" + digest[:12] + "]" + ext
	}
	return "", fmt.Errorf("music destination collision; existing audio retained")
}

func syncMusicDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
