package main

import (
	"context"
	"crypto/subtle"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	Files           []string
	ISRC            string
}
type plexPlaylist struct {
	ID    int64
	Title string
}
type plexClient struct {
	base      string
	token     string
	section   string
	http      *http.Client
	machineID string
	machineMu sync.Mutex
}

type plexXMLContainer struct {
	Size      int               `xml:"size,attr"`
	TotalSize int               `xml:"totalSize,attr"`
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
	OriginalTitle    string `xml:"originalTitle,attr"`
	Media            []struct {
		Parts []struct {
			File string `xml:"file,attr"`
		} `xml:"Part"`
	} `xml:"Media"`
	Duration int64 `xml:"duration,attr"`
	Genre    []struct {
		Tag string `xml:"tag,attr"`
	} `xml:"Genre"`
	PlaylistType string `xml:"playlistType,attr"`
}

func (p *plexClient) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	u, err := url.Parse(strings.TrimRight(p.base, "/") + path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", p.token)
	req.Header.Set("Accept", "application/xml, application/json")
	client := *p.http
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Plex request failed")
	}
	if res.ContentLength > 8<<20 {
		res.Body.Close()
		return nil, fmt.Errorf("plex response too large")
	}
	return res, nil
}

func (p *plexClient) tracks(ctx context.Context) ([]PlexTrack, error) {
	entries, err := p.libraryEntries(ctx, 10)
	if err != nil {
		return nil, err
	}
	albums, err := p.libraryEntries(ctx, 9)
	if err != nil {
		return nil, err
	}
	genres := map[string][]string{}
	for _, album := range albums {
		for _, g := range album.Genre {
			genres[album.RatingKey] = append(genres[album.RatingKey], g.Tag)
		}
	}
	all := make([]PlexTrack, 0, len(entries))
	for _, x := range entries {
		artist := x.OriginalTitle
		if artist == "" {
			artist = x.GrandparentTitle
		}
		t := PlexTrack{RatingKey: x.RatingKey, ParentRatingKey: x.ParentRatingKey, Title: x.Title, Artist: artist, Album: x.ParentTitle, Duration: x.Duration}
		for _, g := range x.Genre {
			t.Genres = append(t.Genres, g.Tag)
		}
		if len(t.Genres) == 0 {
			t.Genres = genres[x.ParentRatingKey]
		}
		for _, media := range x.Media {
			for _, part := range media.Parts {
				t.Files = append(t.Files, part.File)
			}
		}
		all = append(all, t)
	}
	return all, nil
}

func (p *plexClient) libraryEntries(ctx context.Context, kind int) ([]plexXMLMetadata, error) {
	var all []plexXMLMetadata
	seen := map[string]bool{}
	for page := 0; page < 2000; page++ {
		path := fmt.Sprintf("/library/sections/%s/all?type=%d&X-Plex-Container-Start=%d&X-Plex-Container-Size=500", url.PathEscape(p.section), kind, len(all))
		res, err := p.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if res.StatusCode/100 != 2 {
			res.Body.Close()
			return nil, fmt.Errorf("Plex library returned HTTP %d", res.StatusCode)
		}
		var c plexXMLContainer
		err = xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&c)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		entries := append(append(c.Metadata, c.Track...), c.Directory...)
		for _, entry := range entries {
			if entry.RatingKey == "" || seen[entry.RatingKey] {
				return nil, fmt.Errorf("Plex library pagination repeated or omitted track identity")
			}
			seen[entry.RatingKey] = true
		}
		all = append(all, entries...)
		if c.TotalSize > 0 {
			if len(all) >= c.TotalSize {
				return all, nil
			}
			if len(entries) == 0 {
				return nil, fmt.Errorf("Plex library pagination ended early")
			}
		} else if len(entries) < 500 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("Plex library pagination exceeds limit")
}

func (p *plexClient) playlists(ctx context.Context) ([]plexPlaylist, error) {
	var out []plexPlaylist
	consumed := 0
	seen := map[int64]bool{}
	for page := 0; page < 2000; page++ {
		path := fmt.Sprintf("/playlists?X-Plex-Container-Start=%d&X-Plex-Container-Size=500", consumed)
		res, err := p.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if res.StatusCode/100 != 2 {
			res.Body.Close()
			return nil, fmt.Errorf("Plex playlists returned HTTP %d", res.StatusCode)
		}
		var c plexXMLContainer
		err = xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&c)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		entries := append(c.Metadata, c.Playlist...)
		for _, x := range entries {
			id, err := strconv.ParseInt(x.RatingKey, 10, 64)
			if err != nil || id <= 0 || seen[id] {
				return nil, fmt.Errorf("Plex playlists pagination repeated or omitted identity")
			}
			seen[id] = true
			if x.PlaylistType == "audio" || x.PlaylistType == "" {
				out = append(out, plexPlaylist{ID: id, Title: x.Title})
			}
		}
		consumed += len(entries)
		if c.TotalSize > 0 {
			if consumed >= c.TotalSize {
				return out, nil
			}
			if len(entries) == 0 {
				return nil, fmt.Errorf("Plex playlists pagination ended early")
			}
		} else if len(entries) < 500 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("Plex playlists pagination exceeds limit")
}

func (p *plexClient) createPlaylist(ctx context.Context, title string, tracks []PlexTrack) (int64, error) {
	if len(tracks) == 0 {
		return 0, fmt.Errorf("cannot create empty playlist")
	}
	keys := make([]string, 0, len(tracks))
	for _, t := range tracks {
		keys = append(keys, t.RatingKey)
	}
	machine, err := p.serverID(ctx)
	if err != nil {
		return 0, err
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
	var out []PlexTrack
	for page := 0; page < 2000; page++ {
		res, err := p.request(ctx, http.MethodGet, fmt.Sprintf("/playlists/%d/items?X-Plex-Container-Start=%d&X-Plex-Container-Size=500", id, len(out)), nil)
		if err != nil {
			return nil, err
		}
		if res.StatusCode/100 != 2 {
			res.Body.Close()
			return nil, fmt.Errorf("plex playlist items returned %s", res.Status)
		}
		var c plexXMLContainer
		err = xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&c)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		entries := append(c.Metadata, c.Track...)
		if page > 0 && len(entries) > 0 && entries[0].PlaylistItemID != "" && entries[0].PlaylistItemID == out[0].PlaylistItemID {
			return nil, fmt.Errorf("Plex playlist pagination repeated a page")
		}
		for _, x := range entries {
			out = append(out, PlexTrack{RatingKey: x.RatingKey, PlaylistItemID: x.PlaylistItemID, Title: x.Title, Artist: x.GrandparentTitle, Album: x.ParentTitle, Duration: x.Duration})
		}
		if c.TotalSize > 0 {
			if len(out) >= c.TotalSize {
				return out, nil
			}
			if len(entries) == 0 {
				return nil, fmt.Errorf("Plex playlist pagination ended early")
			}
		} else if len(entries) < 500 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("Plex playlist pagination exceeds limit")
}

func (p *plexClient) machineIdentifier(ctx context.Context) (string, error) {
	res, err := p.request(ctx, http.MethodGet, "/identity", nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("plex identity returned %s", res.Status)
	}
	var c struct {
		Machine string `xml:"machineIdentifier,attr"`
	}
	if err := xml.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&c); err != nil {
		return "", err
	}
	if c.Machine == "" {
		return "", fmt.Errorf("plex identity missing machine identifier")
	}
	return c.Machine, nil
}

func (p *plexClient) serverID(ctx context.Context) (string, error) {
	p.machineMu.Lock()
	defer p.machineMu.Unlock()
	if p.machineID != "" {
		return p.machineID, nil
	}
	id, err := p.machineIdentifier(ctx)
	if err != nil {
		return "", err
	}
	p.machineID = id
	return id, nil
}

func (p *plexClient) refresh(ctx context.Context) error {
	res, err := p.request(ctx, http.MethodGet, "/library/sections/"+url.PathEscape(p.section)+"/refresh", nil)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("plex refresh returned %s", res.Status)
	}
	return nil
}

func (p *plexClient) addTracks(ctx context.Context, id int64, tracks []PlexTrack) error {
	machine, err := p.serverID(ctx)
	if err != nil {
		return err
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
		return fmt.Errorf("Plex playlist item ID is missing")
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
		return fmt.Errorf("Plex playlist item ID is missing")
	}
	path := fmt.Sprintf("/playlists/%d/items/%s/move", id, url.PathEscape(itemID))
	if after != nil {
		afterID := after.PlaylistItemID
		if afterID == "" {
			return fmt.Errorf("Plex predecessor item ID is missing")
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
	afterAdd, err := p.playlistItems(ctx, id)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, item := range afterAdd {
		counts[item.RatingKey]++
	}
	for _, item := range desired {
		if counts[item.RatingKey] <= 0 {
			return fmt.Errorf("Plex did not retain requested playlist occurrences")
		}
		counts[item.RatingKey]--
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
			return fmt.Errorf("Plex did not retain all requested playlist occurrences")
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
	final, err := p.playlistItems(ctx, id)
	if err != nil {
		return err
	}
	if len(final) != len(desired) {
		return fmt.Errorf("Plex playlist length did not converge")
	}
	for i := range final {
		if final[i].RatingKey != desired[i].RatingKey {
			return fmt.Errorf("Plex playlist order did not converge")
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
	scan    atomic.Bool
}

func InitMusicPlaylists(ctx context.Context, r *gin.Engine) error {
	base, token, section := os.Getenv("PLEX_URL"), os.Getenv("PLEX_TOKEN"), os.Getenv("PLEX_MUSIC_SECTION_ID")
	if base == "" || token == "" || section == "" {
		return nil
	}
	parsed, e := url.Parse(base)
	if e != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("PLEX_URL must be an HTTP(S) server URL without credentials or query")
	}
	if _, e := strconv.ParseUint(section, 10, 64); e != nil {
		return fmt.Errorf("PLEX_MUSIC_SECTION_ID must be numeric")
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
	m.requestSync()
	return nil
}

func musicAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}
func (m *playlistManager) importManifest(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<20)
	var in PlaylistManifest
	if err := c.ShouldBindJSON(&in); err != nil || (in.Source != "spotify" && in.Source != "tidal" && in.Source != "manual") || strings.TrimSpace(in.SourceID) == "" || strings.TrimSpace(in.Name) == "" || in.Tracks == nil {
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
		musicPlaylists.scan.Store(true)
		musicPlaylists.requestSync()
	}
}
