package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// repairLibraryFile is an offline maintenance command. A recovery copy is
// published before removing the misplaced source; no Put.io receipt is sent.
func repairLibraryFile(source string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	var from ContentType
	var relative string
	for root, kind := range map[string]ContentType{MoviesPath: Movies, TVShowPath: TVShow, musicEnv("MUSIC_PATH", "/mnt/music"): Music} {
		rel, e := filepath.Rel(root, source)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			from = kind
			relative = rel
			break
		}
	}
	if from == "" {
		return fmt.Errorf("source must be inside a configured library")
	}
	if err = musicMkdirAll(filepath.Dir(source)); err != nil {
		return err
	}
	digest, err := musicFileDigest(source)
	if err != nil {
		return err
	}
	item := Item{Name: filepath.Base(source), Type: from, RelativePath: relative}
	if err = routeMedia(&item); err != nil {
		return err
	}
	if item.Type == from {
		return fmt.Errorf("file is already in the correct library")
	}
	backupRoot := musicEnv("MEDIA_QUARANTINE_PATH", "/data/media-quarantine")
	backup := filepath.Join(backupRoot, time.Now().UTC().Format("20060102T150405.000000000"), string(from), item.Name)
	if err = musicMkdirAll(filepath.Dir(backup)); err != nil {
		return err
	}
	backup, err = publishMusicFile(source, backup, digest)
	if err != nil {
		return err
	}
	var target string
	if item.Type == Music {
		metadata, e := readMusicMetadata(source)
		if e != nil {
			return e
		}
		target, err = publishMusic(musicEnv("MUSIC_PATH", "/mnt/music"), source, &item, metadata, digest)
	} else {
		if err = probeVideo(source); err != nil {
			return err
		}
		target, err = mediaDestination(&item)
		if err == nil {
			err = musicMkdirAll(filepath.Dir(target))
		}
		if err == nil {
			target, err = publishMusicFile(source, target, digest)
		}
	}
	if err != nil {
		return err
	}
	manifest := map[string]string{"source": source, "destination": target, "recovery": backup, "sha256": digest}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	record := filepath.Join(filepath.Dir(backup), "repair.json")
	f, e := os.OpenFile(record, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	if e != nil {
		return e
	}
	if e = syncMusicDirectory(filepath.Dir(backup)); e != nil {
		return e
	}
	current, e := musicFileDigest(source)
	if e != nil || current != digest {
		return fmt.Errorf("source changed during repair; preserved")
	}
	if e = os.Remove(source); e != nil {
		return e
	}
	if e = syncMusicDirectory(filepath.Dir(source)); e != nil {
		return e
	}
	fmt.Println(string(data))
	return nil
}
