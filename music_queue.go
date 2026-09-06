package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

var musicQueueMutex sync.Mutex

func musicQueuePath() string {
	return musicEnv("MUSIC_QUEUE_PATH", filepath.Join(filepath.Dir(musicEnv("MUSIC_STATE_PATH", musicEnv("CATALOG_DB", "./tvshows_catalog.db"))), "music-queue.json"))
}

func readMusicQueue() ([]*Item, error) {
	data, err := os.ReadFile(musicQueuePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []*Item
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("music queue is invalid: %w", err)
	}
	return jobs, nil
}

func saveMusicQueue(jobs []*Item) error {
	path := musicQueuePath()
	if err := musicMkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".music-queue-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	return syncMusicDirectory(filepath.Dir(path))
}

func persistMusicJob(item *Item) error {
	musicQueueMutex.Lock()
	defer musicQueueMutex.Unlock()
	jobs, err := readMusicQueue()
	if err != nil {
		return err
	}
	for _, prior := range jobs {
		if prior != nil && ((item.FileId > 0 && prior.FileId == item.FileId) || (item.FileId <= 0 && prior.URL == item.URL)) {
			return nil
		}
	}
	jobs = append(jobs, item)
	return saveMusicQueue(jobs)
}

func removeMusicJob(item *Item) error {
	musicQueueMutex.Lock()
	defer musicQueueMutex.Unlock()
	jobs, err := readMusicQueue()
	if err != nil {
		return err
	}
	kept := jobs[:0]
	for _, prior := range jobs {
		if prior == nil || (item.FileId > 0 && prior.FileId == item.FileId) || (item.FileId <= 0 && prior.URL == item.URL) {
			continue
		}
		kept = append(kept, prior)
	}
	return saveMusicQueue(kept)
}

func restoreMusicQueue() error {
	musicEnqueueMutex.Lock()
	defer musicEnqueueMutex.Unlock()
	musicQueueMutex.Lock()
	defer musicQueueMutex.Unlock()
	jobs, err := readMusicQueue()
	if err != nil {
		return err
	}
	JobsMutex.Lock()
	defer JobsMutex.Unlock()
	for _, job := range jobs {
		if job == nil {
			continue
		}
		if isAudioFilename(job.Name) {
			job.Type = Music
		}
		musicKnownJobs[musicJobKey(job)] = true
		duplicate := false
		for _, existing := range Jobs {
			if existing != nil && ((job.FileId > 0 && existing.FileId == job.FileId) || (job.FileId <= 0 && existing.URL == job.URL)) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			Jobs = append(Jobs, job)
		}
	}
	return nil
}

func musicJobKey(item *Item) string {
	if item == nil {
		return ""
	}
	if item.FileId > 0 {
		return strconv.FormatInt(item.FileId, 10)
	}
	return item.URL
}

var musicEnqueueMutex sync.Mutex
var musicKnownJobs = make(map[string]bool)

// Membership lasts from durable acceptance through retries until completion,
// including the dequeue-to-active transition where the legacy queue has a gap.
func enqueueMusicJob(item *Item) (bool, error) {
	musicEnqueueMutex.Lock()
	defer musicEnqueueMutex.Unlock()
	key := musicJobKey(item)
	if musicKnownJobs[key] {
		return false, nil
	}
	JobsMutex.Lock()
	full := len(Jobs) >= 1000
	JobsMutex.Unlock()
	if full {
		return false, fmt.Errorf("download queue is full")
	}
	if err := persistMusicJob(item); err != nil {
		return false, err
	}
	musicKnownJobs[key] = true
	JobsMutex.Lock()
	Jobs = append(Jobs, item)
	JobsMutex.Unlock()
	return true, nil
}
func forgetMusicJob(item *Item) {
	musicEnqueueMutex.Lock()
	delete(musicKnownJobs, musicJobKey(item))
	musicEnqueueMutex.Unlock()
}
