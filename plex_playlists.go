package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type PlexTrack struct {
	RatingKey       string
	ParentRatingKey string
	PlaylistItemID  string
	Title           string
	Artist          string
	Album           string
	Duration        int64
	Genres          []string
}
type plexPlaylist struct {
	ID    int64
	Title string
}
type plexClient struct {
	base    string
	token   string
	section string
	http    *http.Client
}

type plexXMLContainer struct {
	Size      int               `xml:"size,attr"`
	Metadata  []plexXMLMetadata `xml:"Metadata"`
	Track     []plexXMLMetadata `xml:"Track"`
	Directory []plexXMLMetadata `xml:"Directory"`
	Playlist  []plexXMLMetadata `xml:"Playlist"`
}
type plexXMLMetadata struct {
	RatingKey        string `xml:"ratingKey,attr"`
	Title            string `xml:"title,attr"`
	ParentTitle      string `xml:"parentTitle,attr"`
	ParentRatingKey  string `xml:"parentRatingKey,attr"`
	PlaylistItemID   string `xml:"playlistItemID,attr"`
	GrandparentTitle string `xml:"grandparentTitle,attr"`
	Duration         int64  `xml:"duration,attr"`
	Genre            []struct {
		Tag string `xml:"tag,attr"`
	} `xml:"Genre"`
	PlaylistType string `xml:"playlistType,attr"`
}

func (p *plexClient) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	u, err := url.Parse(strings.TrimRight(p.base, "/") + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("X-Plex-Token", p.token)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", p.token)
	req.Header.Set("Accept", "application/xml, application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.ContentLength > 8<<20 {
		res.Body.Close()
		return nil, fmt.Errorf("plex response too large")
	}
	return res, nil
}

func (p *plexClient) tracks(ctx context.Context) ([]PlexTrack, error) {
	var all []PlexTrack
	for start := 0; ; start += 500 {
		path := fmt.Sprintf("/library/sections/%s/all?type=10&X-Plex-Container-Start=%d&X-Plex-Container-Size=500", url.PathEscape(p.section), start)
		res, err := p.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if res.StatusCode/100 != 2 {
			res.Body.Close()
			return nil, fmt.Errorf("plex library returned %s", res.Status)
		}
		var c plexXMLContainer
		err = xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&c)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		entries := append(c.Metadata, c.Track...)
		for _, x := range entries {
			t := PlexTrack{RatingKey: x.RatingKey, ParentRatingKey: x.ParentRatingKey, Title: x.Title, Artist: x.GrandparentTitle, Album: x.ParentTitle, Duration: x.Duration}
			for _, g := range x.Genre {
				t.Genres = append(t.Genres, g.Tag)
			}
			all = append(all, t)
		}
		if len(entries) == 0 || len(all) >= c.Size && c.Size > 0 || len(entries) < 500 {
			break
		}
	}
	albumGenres := make(map[string][]string)
	for _, track := range all {
		if track.ParentRatingKey == "" || len(track.Genres) != 0 || albumGenres[track.ParentRatingKey] != nil {
			continue
		}
		res, e := p.request(ctx, http.MethodGet, "/library/metadata/"+url.PathEscape(track.ParentRatingKey), nil)
		if e != nil {
			continue
		}
		var c plexXMLContainer
		e = xml.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&c)
		res.Body.Close()
		if e != nil {
			continue
		}
		albumEntries := append(c.Metadata, c.Directory...)
		for _, x := range albumEntries {
			for _, g := range x.Genre {
				albumGenres[track.ParentRatingKey] = append(albumGenres[track.ParentRatingKey], g.Tag)
			}
		}
	}
	for i := range all {
		if len(all[i].Genres) == 0 {
			all[i].Genres = albumGenres[all[i].ParentRatingKey]
		}
	}
	return all, nil
}

func (p *plexClient) playlists(ctx context.Context) ([]plexPlaylist, error) {
	res, err := p.request(ctx, http.MethodGet, "/playlists", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("plex playlists returned %s", res.Status)
	}
	var c plexXMLContainer
	if err := xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&c); err != nil {
		return nil, err
	}
	out := make([]plexPlaylist, 0, len(c.Metadata))
	entries := append(c.Metadata, c.Playlist...)
	for _, x := range entries {
		if x.PlaylistType == "audio" || x.PlaylistType == "" {
			id, _ := strconv.ParseInt(x.RatingKey, 10, 64)
			out = append(out, plexPlaylist{ID: id, Title: x.Title})
		}
	}
	return out, nil
}

func (p *plexClient) createPlaylist(ctx context.Context, title string, tracks []PlexTrack) (int64, error) {
	if len(tracks) == 0 {
		return 0, fmt.Errorf("cannot create empty playlist")
	}
	keys := make([]string, 0, len(tracks))
	for _, t := range tracks {
		keys = append(keys, t.RatingKey)
	}
	machine := os.Getenv("PLEX_MACHINE_IDENTIFIER")
	if machine == "" {
		machine = "local"
	}
	uri := "server://" + machine + "/com.plexapp.plugins.library/library/metadata/" + strings.Join(keys, ",")
	form := url.Values{"title": {title}, "type": {"audio"}, "smart": {"0"}, "uri": {uri}}
	res, err := p.request(ctx, http.MethodPost, "/playlists?"+form.Encode(), nil)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return 0, fmt.Errorf("create playlist returned %s", res.Status)
	}
	var c plexXMLContainer
	if err := xml.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&c); err != nil {
		return 0, err
	}
	entries := append(c.Metadata, c.Playlist...)
	if len(entries) == 0 {
		return 0, fmt.Errorf("plex create returned no playlist")
	}
	return strconv.ParseInt(entries[0].RatingKey, 10, 64)
}

func (p *plexClient) playlistItems(ctx context.Context, id int64) ([]PlexTrack, error) {
	res, err := p.request(ctx, http.MethodGet, fmt.Sprintf("/playlists/%d/items", id), nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("playlist items returned %s", res.Status)
	}
	var c plexXMLContainer
	if err := xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&c); err != nil {
		return nil, err
	}
	entries := append(c.Metadata, c.Track...)
	out := make([]PlexTrack, 0, len(entries))
	for _, x := range entries {
		out = append(out, PlexTrack{RatingKey: x.RatingKey, PlaylistItemID: x.PlaylistItemID, Title: x.Title, Artist: x.GrandparentTitle, Album: x.ParentTitle, Duration: x.Duration})
	}
	return out, nil
}

func (p *plexClient) addTracks(ctx context.Context, id int64, tracks []PlexTrack) error {
	machine := os.Getenv("PLEX_MACHINE_IDENTIFIER")
	if machine == "" {
		machine = "local"
	}
	for _, t := range tracks {
		path := fmt.Sprintf("/playlists/%d/items?uri=%s", id, url.QueryEscape("server://"+machine+"/com.plexapp.plugins.library/library/metadata/"+t.RatingKey))
		res, err := p.request(ctx, http.MethodPut, path, nil)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode/100 != 2 {
			return fmt.Errorf("add playlist item returned %s", res.Status)
		}
	}
	return nil
}

func (p *plexClient) removeTrack(ctx context.Context, id int64, item PlexTrack) error {
	itemID := item.PlaylistItemID
	if itemID == "" {
		itemID = item.RatingKey
	}
	res, err := p.request(ctx, http.MethodDelete, fmt.Sprintf("/playlists/%d/items/%s", id, url.PathEscape(itemID)), nil)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("remove playlist item returned %s", res.Status)
	}
	return nil
}

func (p *plexClient) moveTrack(ctx context.Context, id int64, item PlexTrack, after *PlexTrack) error {
	itemID := item.PlaylistItemID
	if itemID == "" {
		itemID = item.RatingKey
	}
	path := fmt.Sprintf("/playlists/%d/items/%s/move", id, url.PathEscape(itemID))
	if after != nil {
		afterID := after.PlaylistItemID
		if afterID == "" {
			afterID = after.RatingKey
		}
		path += "?after=" + url.QueryEscape(afterID)
	}
	res, err := p.request(ctx, http.MethodPut, path, nil)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("move playlist item returned %s", res.Status)
	}
	return nil
}

func (p *plexClient) reconcilePlaylist(ctx context.Context, id int64, desired []PlexTrack) error {
	current, err := p.playlistItems(ctx, id)
	if err != nil {
		return err
	}
	if len(current) == len(desired) {
		equal := true
		for i := range desired {
			if current[i].RatingKey != desired[i].RatingKey {
				equal = false
				break
			}
		}
		if equal {
			return nil
		}
	}
	present := make(map[string]int, len(current))
	for _, item := range current {
		present[item.RatingKey]++
	}
	// Add before deleting so an add failure leaves the prior playlist intact.
	for _, item := range desired {
		if present[item.RatingKey] == 0 {
			if err := p.addTracks(ctx, id, []PlexTrack{item}); err != nil {
				return err
			}
		} else {
			present[item.RatingKey]--
		}
	}
	wanted := make(map[string]int, len(desired))
	for _, item := range desired {
		wanted[item.RatingKey]++
	}
	for _, item := range current {
		if wanted[item.RatingKey] == 0 {
			if err := p.removeTrack(ctx, id, item); err != nil {
				return err
			}
		} else {
			wanted[item.RatingKey]--
		}
	}
	current, err = p.playlistItems(ctx, id)
	if err != nil {
		return err
	}
	for i, want := range desired {
		if i < len(current) && current[i].RatingKey == want.RatingKey {
			continue
		}
		at := -1
		for j := i + 1; j < len(current); j++ {
			if current[j].RatingKey == want.RatingKey {
				at = j
				break
			}
		}
		if at < 0 {
			continue
		}
		var after *PlexTrack
		if i > 0 {
			after = &current[i-1]
		}
		if err := p.moveTrack(ctx, id, current[at], after); err != nil {
			return err
		}
		item := current[at]
		current = append(current[:at], current[at+1:]...)
		if i >= len(current) {
			current = append(current, item)
		} else {
			current = append(current[:i], append([]PlexTrack{item}, current[i:]...)...)
		}
	}
	return nil
}

var musicPlaylists *playlistManager

type playlistManager struct {
	client  *plexClient
	store   *playlistStore
	ctx     context.Context
	trigger chan struct{}
	once    sync.Once
}

func InitMusicPlaylists(ctx context.Context, r *gin.Engine) error {
	base, token, section := os.Getenv("PLEX_URL"), os.Getenv("PLEX_TOKEN"), os.Getenv("PLEX_MUSIC_SECTION_ID")
	if base == "" || token == "" || section == "" {
		return nil
	}
	dir := os.Getenv("MUSIC_PLAYLIST_DIR")
	if dir == "" {
		dir = "/data/music-playlists"
	}
	store, err := newPlaylistStore(dir)
	if err != nil {
		return err
	}
	m := &playlistManager{client: &plexClient{base: base, token: token, section: section, http: &http.Client{Timeout: 15 * time.Second}}, store: store, ctx: ctx, trigger: make(chan struct{}, 1)}
	musicPlaylists = m
	if api := os.Getenv("MUSIC_API_TOKEN"); api != "" {
		r.POST("/music/playlists", musicAuth(api), m.importManifest)
		r.GET("/music/playlists", musicAuth(api), m.status)
		r.POST("/music/sync", musicAuth(api), m.syncNow)
	}
	m.once.Do(func() { go m.loop() })
	return nil
}

func musicAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}
func (m *playlistManager) importManifest(c *gin.Context) {
	var in PlaylistManifest
	if err := c.ShouldBindJSON(&in); err != nil || (in.Source != "spotify" && in.Source != "tidal" && in.Source != "manual") || in.SourceID == "" || in.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playlist manifest"})
		return
	}
	if err := m.store.upsert(in); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	m.requestSync()
	c.JSON(http.StatusAccepted, gin.H{"status": "queued"})
}
func (m *playlistManager) status(c *gin.Context) { s := m.store.snapshot(); c.JSON(http.StatusOK, s) }
func (m *playlistManager) syncNow(c *gin.Context) {
	m.requestSync()
	c.JSON(http.StatusAccepted, gin.H{"status": "queued"})
}
func (m *playlistManager) requestSync() {
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}
func (m *playlistManager) loop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.reconcile()
		case <-m.trigger:
			m.reconcile()
		}
	}
}
func TriggerMusicSync() {
	if musicPlaylists != nil {
		musicPlaylists.requestSync()
	}
}

func (m *playlistManager) reconcile() {
	ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
	defer cancel()
	library, err := m.client.tracks(ctx)
	if err != nil {
		return
	}
	state := m.store.snapshot()
	for _, manifest := range state.Manifests {
		matched, pending, amb := matchPlaylistTracks(manifest.Tracks, library)
		key := playlistKey(manifest)
		st := PlaylistStatus{Source: manifest.Source, SourceID: manifest.SourceID, Name: manifest.Name, Matched: len(matched), Missing: len(pending), Ambiguous: amb, UpdatedAt: time.Now()}
		if len(matched) > 0 {
			id := m.store.snapshot().Owned[key]
			if id == 0 {
				id, err = m.client.createPlaylist(ctx, manifest.Name, matched)
				if err == nil {
					m.store.mu.Lock()
					m.store.state.Owned[key] = id
					_ = m.store.saveLocked()
					m.store.mu.Unlock()
				}
			}
			if err == nil {
				err = m.client.reconcilePlaylist(ctx, id, matched)
			}
		}
		if err != nil {
			st.Error = err.Error()
		}
		m.store.mu.Lock()
		m.store.state.Status[key] = st
		_ = m.store.saveLocked()
		m.store.mu.Unlock()
	}
	// Genre playlists are derived from Plex metadata and use a separate ownership namespace.
	genres := make(map[string][]PlexTrack)
	for _, track := range library {
		for _, genre := range track.Genres {
			genre = strings.TrimSpace(genre)
			if genre != "" {
				genres[genre] = append(genres[genre], track)
			}
		}
	}
	for genre, tracks := range genres {
		key := "genre:" + normalizePlaylistText(genre)
		id := state.Owned[key]
		var e error
		if id == 0 {
			id, e = m.client.createPlaylist(ctx, "Genre — "+genre, tracks)
			if e == nil {
				m.store.mu.Lock()
				m.store.state.Owned[key] = id
				_ = m.store.saveLocked()
				m.store.mu.Unlock()
			}
		}
		if e == nil {
			e = m.client.reconcilePlaylist(ctx, id, tracks)
		}
		if e != nil {
			logMessage(LogLevelWarn, "MusicPlaylists", "genre %s sync failed: %v", genre, e)
		}
	}
}
