package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type fakePlaylist struct {
	title string
	items []PlexTrack
}
type fakePlex struct {
	library    []PlexTrack
	lists      map[int64]*fakePlaylist
	nextID     int64
	nextItem   int
	creates    int
	failAdd    bool
	noGenre    bool
	failRename bool
}

func (f *fakePlex) item(key string) PlexTrack {
	f.nextItem++
	return PlexTrack{RatingKey: key, PlaylistItemID: strconv.Itoa(f.nextItem)}
}
func (f *fakePlex) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	if r.Header.Get("X-Plex-Token") != "secret" || r.URL.Query().Get("X-Plex-Token") != "" {
		http.Error(w, "token contract", 401)
		return
	}
	switch r.URL.Path {
	case "/identity":
		fmt.Fprint(w, `<MediaContainer machineIdentifier="machine-123"/>`)
		return
	case "/library/sections/3/all":
		if r.URL.Query().Get("type") == "9" {
			if f.noGenre {
				fmt.Fprint(w, `<MediaContainer size="1"><Directory ratingKey="a"/></MediaContainer>`)
				return
			}
			fmt.Fprint(w, `<MediaContainer size="1"><Directory ratingKey="a" title="Album"><Genre tag="Rock"/></Directory></MediaContainer>`)
			return
		}
		fmt.Fprintf(w, `<MediaContainer size="%d">`, len(f.library))
		for _, track := range f.library {
			fmt.Fprintf(w, `<Track ratingKey="%s" parentRatingKey="a" title="%s" grandparentTitle="Band" parentTitle="Album" duration="120000"/>`, track.RatingKey, track.Title)
		}
		fmt.Fprint(w, `</MediaContainer>`)
		return
	case "/playlists":
		if r.Method == "POST" {
			if !strings.Contains(r.URL.Query().Get("uri"), "server://machine-123/") {
				http.Error(w, "bad machine", 400)
				return
			}
			f.nextID++
			f.creates++
			id := f.nextID
			keys := strings.Split(r.URL.Query().Get("uri"), "/")
			key := keys[len(keys)-1]
			if strings.Contains(key, ",") {
				http.Error(w, "creation must be bounded", 400)
				return
			}
			f.lists[id] = &fakePlaylist{title: r.URL.Query().Get("title"), items: []PlexTrack{f.item(key)}}
			fmt.Fprintf(w, `<MediaContainer><Playlist ratingKey="%d" playlistType="audio"/></MediaContainer>`, id)
			return
		}
		fmt.Fprint(w, `<MediaContainer>`)
		for id, list := range f.lists {
			fmt.Fprintf(w, `<Playlist ratingKey="%d" title="%s" playlistType="audio"/>`, id, list.title)
		}
		fmt.Fprint(w, `</MediaContainer>`)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "playlists" {
		http.NotFound(w, r)
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	list := f.lists[id]
	if list == nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && r.Method == "PUT" {
		if f.failRename {
			http.Error(w, "rename failed", 503)
			return
		}
		list.title = r.URL.Query().Get("title")
		return
	}
	if len(parts) == 3 && parts[2] == "items" {
		if r.Method == "GET" {
			fmt.Fprintf(w, `<MediaContainer size="%d">`, len(list.items))
			for _, item := range list.items {
				fmt.Fprintf(w, `<Track ratingKey="%s" playlistItemID="%s"/>`, item.RatingKey, item.PlaylistItemID)
			}
			fmt.Fprint(w, `</MediaContainer>`)
			return
		}
		if r.Method == "PUT" {
			if f.failAdd {
				http.Error(w, "failed", 503)
				return
			}
			uri := r.URL.Query().Get("uri")
			keys := strings.Split(uri, "/")
			for _, key := range strings.Split(keys[len(keys)-1], ",") {
				list.items = append(list.items, f.item(key))
			}
			return
		}
	}
	if len(parts) >= 4 {
		at := -1
		for i, item := range list.items {
			if item.PlaylistItemID == parts[3] {
				at = i
				break
			}
		}
		if at < 0 {
			http.NotFound(w, r)
			return
		}
		item := list.items[at]
		if r.Method == "DELETE" {
			list.items = append(list.items[:at], list.items[at+1:]...)
			return
		}
		if len(parts) == 5 && parts[4] == "move" {
			list.items = append(list.items[:at], list.items[at+1:]...)
			index := 0
			after := r.URL.Query().Get("after")
			if after != "" {
				for i, p := range list.items {
					if p.PlaylistItemID == after {
						index = i + 1
						break
					}
				}
			}
			next := append([]PlexTrack{}, list.items[:index]...)
			next = append(next, item)
			next = append(next, list.items[index:]...)
			list.items = next
			return
		}
	}
	http.NotFound(w, r)
}
func fakeKeys(p *fakePlaylist) []string {
	var keys []string
	for _, t := range p.items {
		keys = append(keys, t.RatingKey)
	}
	return keys
}

func TestPlaylistRecreationPreservesOrderGapsDuplicatesAndPersonalLists(t *testing.T) {
	t.Setenv("MUSIC_STATE_PATH", filepath.Join(t.TempDir(), "none.json"))
	fake := &fakePlex{library: []PlexTrack{{RatingKey: "1", Title: "First"}, {RatingKey: "3", Title: "Last"}}, lists: map[int64]*fakePlaylist{99: {title: "Mix", items: []PlexTrack{{RatingKey: "private", PlaylistItemID: "99"}}}}, nextID: 99}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	defer srv.Close()
	dir := t.TempDir()
	store, err := newPlaylistStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest := PlaylistManifest{Source: "tidal", SourceID: "mix", Name: "Mix", Tracks: []PlaylistTrack{{Title: "First", Artist: "Band", Album: "Album"}, {Title: "Middle", Artist: "Band", Album: "Album"}, {Title: "Last", Artist: "Band", Album: "Album"}}}
	if err := store.upsert(manifest); err != nil {
		t.Fatal(err)
	}
	m := &playlistManager{client: &plexClient{base: srv.URL, token: "secret", section: "3", http: srv.Client()}, store: store, ctx: context.Background()}
	if err := m.reconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := store.snapshot().Owned["tidal:mix"]
	if !reflect.DeepEqual(fakeKeys(fake.lists[id]), []string{"1", "3"}) {
		t.Fatal("wrong initial playlist")
	}
	status := store.snapshot().Status["tidal:mix"]
	if len(status.Pending) != 1 || status.Pending[0].Index != 1 {
		t.Fatalf("wrong pending position: %+v", status)
	}
	fake.library = append(fake.library, PlexTrack{RatingKey: "2", Title: "Middle"})
	if err := m.reconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fakeKeys(fake.lists[id]), []string{"1", "2", "3"}) {
		t.Fatalf("gap not filled in order: %v", fakeKeys(fake.lists[id]))
	}
	manifest.Tracks = []PlaylistTrack{manifest.Tracks[2], manifest.Tracks[0], manifest.Tracks[1], manifest.Tracks[0]}
	if err := store.upsert(manifest); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fakeKeys(fake.lists[id]), []string{"3", "1", "2", "1"}) {
		t.Fatalf("duplicates/reorder lost: %v", fakeKeys(fake.lists[id]))
	}
	store, err = newPlaylistStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.store = store
	creates := fake.creates
	if err := m.reconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.creates != creates || !reflect.DeepEqual(fakeKeys(fake.lists[99]), []string{"private"}) {
		t.Fatal("restart duplicated playlist or touched personal playlist")
	}
}

func TestPlaylistAddFailurePreservesExistingItems(t *testing.T) {
	f := &fakePlex{lists: map[int64]*fakePlaylist{7: {items: []PlexTrack{{RatingKey: "1", PlaylistItemID: "1"}, {RatingKey: "2", PlaylistItemID: "2"}}}}, failAdd: true}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	defer srv.Close()
	p := &plexClient{base: srv.URL, token: "secret", http: srv.Client()}
	if err := p.reconcilePlaylist(context.Background(), 7, []PlexTrack{{RatingKey: "3"}}); err == nil {
		t.Fatal("failed addition reported success")
	}
	if !reflect.DeepEqual(fakeKeys(f.lists[7]), []string{"1", "2"}) {
		t.Fatal("old playlist removed after failed add")
	}
}

func TestPlaylistStoreRejectsFailedPersistenceTransaction(t *testing.T) {
	s, err := newPlaylistStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := PlaylistManifest{Source: "tidal", SourceID: "id", Name: "old"}
	if err := s.upsert(original); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "file")
	os.WriteFile(bad, []byte("x"), 0600)
	s.dir = bad
	changed := original
	changed.Name = "rejected"
	if err := s.upsert(changed); err == nil {
		t.Fatal("write should fail")
	}
	if s.snapshot().Manifests[0].Name != "old" {
		t.Fatal("rejected manifest became active")
	}
}

func TestPlaylistExplicitEmptyAndRemovedGenreClearOnlyOwnedLists(t *testing.T) {
	t.Setenv("MUSIC_STATE_PATH", filepath.Join(t.TempDir(), "none.json"))
	fake := &fakePlex{library: []PlexTrack{{RatingKey: "1", Title: "First"}}, lists: map[int64]*fakePlaylist{}, nextID: 99}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	defer srv.Close()
	store, err := newPlaylistStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifest := PlaylistManifest{Source: "tidal", SourceID: "mix", Name: "Mix", Tracks: []PlaylistTrack{{Title: "First", Artist: "Band", Album: "Album"}}}
	store.upsert(manifest)
	m := &playlistManager{client: &plexClient{base: srv.URL, token: "secret", section: "3", http: srv.Client()}, store: store, ctx: context.Background()}
	if err := m.reconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids := store.snapshot().Owned
	manifest.Tracks = []PlaylistTrack{}
	if err := store.upsert(manifest); err != nil {
		t.Fatal(err)
	}
	fake.noGenre = true
	if err := m.reconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.lists[ids["tidal:mix"]].items) != 0 || len(fake.lists[ids["genre:rock"]].items) != 0 {
		t.Fatal("explicit empty source/removed genre kept stale items")
	}
}

func TestPlaylistRecoversUncertainCreationWithoutAdoptingPersonalName(t *testing.T) {
	fake := &fakePlex{lists: map[int64]*fakePlaylist{99: {title: "Mix"}}, nextID: 99, failRename: true}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	defer srv.Close()
	store, err := newPlaylistStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := &playlistManager{client: &plexClient{base: srv.URL, token: "secret", http: srv.Client()}, store: store, ctx: context.Background()}
	_, err = m.ensureOwnedPlaylist(context.Background(), "tidal:mix", "Mix", []PlexTrack{{RatingKey: "1"}}, nil)
	if err == nil {
		t.Fatal("rename failure not reported")
	}
	delete(store.state.Owned, "tidal:mix")
	if err := store.saveLocked(); err != nil {
		t.Fatal(err)
	}
	available, err := m.client.playlists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fake.failRename = false
	id, err := m.ensureOwnedPlaylist(context.Background(), "tidal:mix", "Mix", []PlexTrack{{RatingKey: "1"}}, available)
	if err != nil {
		t.Fatal(err)
	}
	if id != 100 || fake.creates != 1 || fake.lists[99].title != "Mix" {
		t.Fatal("uncertain create duplicated or adopted personal playlist")
	}
}
