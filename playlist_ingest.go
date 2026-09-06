package main

import (
	"path/filepath"
	"strings"
)

// Plex and the downloader use different container paths for the same files.
// Prefer the original tags for music we imported, preserving track artists and
// multiple genres even if the library's online metadata provider disagrees.
func applyIngestedMusicMetadata(tracks []PlexTrack) error {
	statePath := musicEnv("MUSIC_STATE_PATH", filepath.Join(filepath.Dir(musicEnv("CATALOG_DB", "./tvshows_catalog.db")), "music-ingest.json"))
	musicImportMutex.Lock()
	state, err := readMusicState(statePath)
	musicImportMutex.Unlock()
	if err != nil {
		return err
	}
	plexRoot := musicEnv("PLEX_MUSIC_PATH", "/media/music")
	localRoot := musicEnv("MUSIC_PATH", "/mnt/music")
	for i := range tracks {
		for _, file := range tracks[i].Files {
			rel, err := filepath.Rel(plexRoot, file)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				continue
			}
			record, ok := state.Files[filepath.Join(localRoot, rel)]
			if !ok {
				continue
			}
			m := record.Metadata
			if len(m.Genres) > 0 {
				tracks[i].Genres = m.Genres
			}
			if m.Artist != "" {
				tracks[i].Artist = m.Artist
			}
			tracks[i].ISRC = m.ISRC
			break
		}
	}
	return nil
}
