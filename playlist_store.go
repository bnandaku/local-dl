package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type PlaylistTrack struct {
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	ISRC       string `json:"isrc,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type PlaylistManifest struct {
	Source   string          `json:"source"`
	SourceID string          `json:"source_id"`
	Name     string          `json:"name"`
	Tracks   []PlaylistTrack `json:"tracks"`
}

type playlistState struct {
	Manifests []PlaylistManifest        `json:"manifests"`
	Owned     map[string]int64          `json:"owned_playlists"`
	Status    map[string]PlaylistStatus `json:"status"`
}

type PlaylistStatus struct {
	Source    string    `json:"source"`
	SourceID  string    `json:"source_id"`
	Name      string    `json:"name"`
	Matched   int       `json:"matched"`
	Missing   int       `json:"missing"`
	Ambiguous int       `json:"ambiguous"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type playlistStore struct {
	mu    sync.Mutex
	dir   string
	state playlistState
}

func newPlaylistStore(dir string) (*playlistStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	s := &playlistStore{dir: dir, state: playlistState{Owned: map[string]int64{}, Status: map[string]PlaylistStatus{}}}
	b, err := os.ReadFile(filepath.Join(dir, "playlists.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.state); err != nil {
		return nil, fmt.Errorf("read playlist state: %w", err)
	}
	if s.state.Owned == nil {
		s.state.Owned = map[string]int64{}
	}
	if s.state.Status == nil {
		s.state.Status = map[string]PlaylistStatus{}
	}
	return s, nil
}

func (s *playlistStore) saveLocked() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "playlists.json.tmp")
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "playlists.json"))
}

func (s *playlistStore) upsert(m PlaylistManifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Manifests {
		if s.state.Manifests[i].Source == m.Source && s.state.Manifests[i].SourceID == m.SourceID {
			s.state.Manifests[i] = m
			return s.saveLocked()
		}
	}
	s.state.Manifests = append(s.state.Manifests, m)
	return s.saveLocked()
}

func (s *playlistStore) snapshot() playlistState {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.state)
	var out playlistState
	_ = json.Unmarshal(b, &out)
	return out
}

func playlistKey(m PlaylistManifest) string { return m.Source + ":" + m.SourceID }
