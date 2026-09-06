package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func (m *playlistManager) reconcile() {
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
	defer cancel()
	if err := m.reconcileAll(ctx); err != nil {
		logMessage(LogLevelWarn, "MusicPlaylists", "Sync failed: %v", err)
		m.store.mu.Lock()
		m.store.state.LastError = err.Error()
		_ = m.store.saveLocked()
		m.store.mu.Unlock()
	}
}

func (m *playlistManager) reconcileAll(ctx context.Context) error {
	server, err := m.client.serverID(ctx)
	if err != nil {
		return err
	}
	m.store.mu.Lock()
	if m.store.state.ServerID != "" && m.store.state.ServerID != server {
		m.store.mu.Unlock()
		return fmt.Errorf("Plex server identity changed; existing playlist ownership is preserved")
	}
	if m.store.state.ServerID == "" {
		m.store.state.ServerID = server
		if err := m.store.saveLocked(); err != nil {
			m.store.state.ServerID = ""
			m.store.mu.Unlock()
			return err
		}
	}
	m.store.mu.Unlock()
	if m.scan.Swap(false) {
		if err := m.client.refresh(ctx); err != nil {
			m.scan.Store(true)
			return err
		}
	}
	library, err := m.client.tracks(ctx)
	if err != nil {
		return err
	}
	if err := applyIngestedMusicMetadata(library); err != nil {
		return err
	}
	available, err := m.client.playlists(ctx)
	if err != nil {
		return err
	}
	state := m.store.snapshot()
	for _, manifest := range state.Manifests {
		if manifest.SourceUnavailable {
			continue
		}
		matched, pending, ambiguous := matchPlaylistDetails(manifest.Tracks, library)
		status := PlaylistStatus{Source: manifest.Source, SourceID: manifest.SourceID, Name: manifest.Name, Matched: len(matched), Missing: len(pending), Ambiguous: ambiguous, UpdatedAt: time.Now()}
		status.Pending = pending
		key := playlistKey(manifest)
		if len(matched) > 0 {
			id, e := m.ensureOwnedPlaylist(ctx, key, manifest.Name, matched, available)
			if e == nil {
				e = m.client.reconcilePlaylist(ctx, id, matched)
			}
			if e != nil {
				status.Error = e.Error()
			}
		}
		if len(matched) == 0 && state.Owned[key] > 0 {
			if len(manifest.Tracks) == 0 && len(library) > 0 {
				if e := m.client.reconcilePlaylist(ctx, state.Owned[key], nil); e != nil {
					status.Error = e.Error()
				}
			} else {
				status.Error = "No tracks currently match; existing playlist retained while entries are pending"
			}
		}
		m.store.mu.Lock()
		m.store.state.Status[key] = status
		err = m.store.saveLocked()
		m.store.mu.Unlock()
		if err != nil {
			return err
		}
	}
	// Merge genre spelling variants and repeated tags without duplicate tracks.
	genres := map[string][]PlexTrack{}
	names := map[string]string{}
	seen := map[string]map[string]bool{}
	for _, track := range library {
		for _, genre := range track.Genres {
			key := normalizePlaylistText(genre)
			if key == "" {
				continue
			}
			if seen[key] == nil {
				seen[key] = map[string]bool{}
				names[key] = genre
			}
			if !seen[key][track.RatingKey] {
				genres[key] = append(genres[key], track)
				seen[key][track.RatingKey] = true
			}
		}
	}
	keys := make([]string, 0, len(genres))
	for key := range genres {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		tracks := genres[key]
		id, e := m.ensureOwnedPlaylist(ctx, "genre:"+key, "Genre — "+names[key], tracks, available)
		if e == nil {
			e = m.client.reconcilePlaylist(ctx, id, tracks)
		}
		if e != nil {
			return e
		}
	}
	if len(library) > 0 {
		present := map[int64]bool{}
		for _, p := range available {
			present[p.ID] = true
		}
		for key, id := range state.Owned {
			if strings.HasPrefix(key, "genre:") && len(genres[strings.TrimPrefix(key, "genre:")]) == 0 && present[id] {
				if e := m.client.reconcilePlaylist(ctx, id, nil); e != nil {
					return e
				}
			}
		}
	}
	m.store.mu.Lock()
	m.store.state.LastError = ""
	err = m.store.saveLocked()
	m.store.mu.Unlock()
	return err
}

func (m *playlistManager) ensureOwnedPlaylist(ctx context.Context, key, title string, tracks []PlexTrack, available []plexPlaylist) (int64, error) {
	state := m.store.snapshot()
	id := state.Owned[key]
	if id != 0 {
		found := false
		for _, playlist := range available {
			if playlist.ID == id {
				found = true
				break
			}
		}
		if !found {
			id = 0
		}
	}
	if id == 0 {
		// A stable unguessable temporary name lets us recover a create that succeeded
		// before its response or the ownership file was persisted. Personal names
		// are never used to establish ownership.
		hash := sha256.Sum256([]byte(key))
		marker := fmt.Sprintf("local-dl pending %s %x", state.InstanceID, hash[:12])
		for _, playlist := range available {
			if playlist.Title == marker {
				if id != 0 {
					return 0, fmt.Errorf("multiple pending managed playlists; refusing ambiguous ownership")
				}
				id = playlist.ID
			}
		}
		if id == 0 {
			var err error
			id, err = m.client.createPlaylist(ctx, marker, tracks[:1])
			if err != nil {
				return 0, err
			}
		}
		m.store.mu.Lock()
		old := m.store.state.Owned[key]
		m.store.state.Owned[key] = id
		err := m.store.saveLocked()
		if err != nil {
			if old == 0 {
				delete(m.store.state.Owned, key)
			} else {
				m.store.state.Owned[key] = old
			}
		}
		m.store.mu.Unlock()
		if err != nil {
			return 0, err
		}
	}
	if err := m.client.renamePlaylist(ctx, id, title); err != nil {
		return 0, err
	}
	return id, nil
}

func (p *plexClient) renamePlaylist(ctx context.Context, id int64, title string) error {
	res, err := p.request(ctx, http.MethodPut, fmt.Sprintf("/playlists/%d?title=%s", id, url.QueryEscape(title)), nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("rename playlist returned HTTP %d", res.StatusCode)
	}
	return nil
}
