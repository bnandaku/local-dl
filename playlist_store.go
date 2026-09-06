package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type PlaylistTrack struct {
	Unavailable bool   `json:"unavailable,omitempty"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	ISRC        string `json:"isrc,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
}

type PlaylistManifest struct {
	Source            string          `json:"source"`
	SourceID          string          `json:"source_id"`
	Name              string          `json:"name"`
	SourceUnavailable bool            `json:"source_unavailable,omitempty"`
	Tracks            []PlaylistTrack `json:"tracks"`
}

type playlistState struct {
	InstanceID string                    `json:"instance_id"`
	ServerID   string                    `json:"server_id"`
	LastError  string                    `json:"last_error,omitempty"`
	Manifests  []PlaylistManifest        `json:"manifests"`
	Owned      map[string]int64          `json:"owned_playlists"`
	Status     map[string]PlaylistStatus `json:"status"`
}

type PlaylistStatus struct {
	Source    string            `json:"source"`
	SourceID  string            `json:"source_id"`
	Name      string            `json:"name"`
	Matched   int               `json:"matched"`
	Missing   int               `json:"missing"`
	Ambiguous int               `json:"ambiguous"`
	Pending   []PlaylistPending `json:"pending,omitempty"`
	Error     string            `json:"error,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type PlaylistPending struct {
	Index int           `json:"index"`
	Track PlaylistTrack `json:"track"`
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
		return s, s.ensureIdentity()
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
	return s, s.ensureIdentity()
}

func (s *playlistStore) ensureIdentity() error {
	if s.state.InstanceID != "" {
		return nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	s.state.InstanceID = hex.EncodeToString(id[:])
	return s.saveLocked()
}

func (s *playlistStore) saveLocked() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".playlist-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, "playlists.json")); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *playlistStore) upsert(m PlaylistManifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Manifests {
		if s.state.Manifests[i].Source == m.Source && s.state.Manifests[i].SourceID == m.SourceID {
			old := s.state.Manifests[i]
			s.state.Manifests[i] = m
			if err := s.saveLocked(); err != nil {
				s.state.Manifests[i] = old
				return err
			}
			return nil
		}
	}
	s.state.Manifests = append(s.state.Manifests, m)
	if err := s.saveLocked(); err != nil {
		s.state.Manifests = s.state.Manifests[:len(s.state.Manifests)-1]
		return err
	}
	return nil
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
