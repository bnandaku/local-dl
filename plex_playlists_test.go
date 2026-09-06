package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPlexClientReadsTracksAndReconcilesWithoutDuplicateAdds(t *testing.T) {
	var puts, deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/library/sections/1/all" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<MediaContainer size="1"><Metadata ratingKey="42" title="Song" grandparentTitle="Artist" parentTitle="Album" duration="120000"><Genre tag="Rock"/></Metadata></MediaContainer>`))
			return
		}
		if r.URL.Path == "/playlists/7/items" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`<MediaContainer size="1"><Metadata ratingKey="42" title="Song"/></MediaContainer>`))
			return
		}
		if r.URL.Path == "/playlists/7/items" && r.Method == http.MethodPut {
			puts++
			return
		}
		if strings.HasPrefix(r.URL.Path, "/playlists/7/items/") && r.Method == http.MethodDelete {
			deletes++
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p := &plexClient{base: srv.URL, token: "secret", section: "1", http: srv.Client()}
	tracks, err := p.tracks(context.Background())
	if err != nil || len(tracks) != 1 || tracks[0].Genres[0] != "Rock" {
		t.Fatalf("tracks=%#v err=%v", tracks, err)
	}
	if err := p.reconcilePlaylist(context.Background(), 7, tracks); err != nil {
		t.Fatal(err)
	}
	if puts != 0 || deletes != 0 {
		t.Fatalf("idempotent reconcile mutated playlist: puts=%d deletes=%d", puts, deletes)
	}
}

func TestMusicAuthRejectsMissingOrWrongBearer(t *testing.T) {
	h := musicAuth("token")
	for _, tc := range []struct {
		name, auth string
		code       int
	}{{"missing", "", http.StatusUnauthorized}, {"wrong", "Bearer nope", http.StatusUnauthorized}, {"valid", "Bearer token", http.StatusOK}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tc.auth)
			c := ginTestContext(r, req)
			h(c)
			if r.Code != tc.code {
				t.Fatalf("status=%d want=%d", r.Code, tc.code)
			}
		})
	}
}

// Tiny adapter keeps this test independent of a running Gin engine.
func ginTestContext(w *httptest.ResponseRecorder, req *http.Request) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c
}

func TestPlexLibraryPaginatesTracksAndBatchesAlbumGenres(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/library/sections/3/all" {
			t.Errorf("unexpected per-album lookup %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("type") == "9" {
			fmt.Fprint(w, `<MediaContainer size="1"><Directory ratingKey="album"><Genre tag="Rock"/></Directory></MediaContainer>`)
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		end := start + 500
		if end > 501 {
			end = 501
		}
		fmt.Fprintf(w, `<MediaContainer size="%d" totalSize="501">`, end-start)
		for i := start; i < end; i++ {
			fmt.Fprintf(w, `<Track ratingKey="%d" parentRatingKey="album" grandparentTitle="Various Artists" originalTitle="Track Artist" title="Song"/>`, i)
		}
		fmt.Fprint(w, `</MediaContainer>`)
	}))
	defer srv.Close()
	p := &plexClient{base: srv.URL, section: "3", http: srv.Client()}
	tracks, err := p.tracks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 501 || calls != 3 || tracks[500].Genres[0] != "Rock" || tracks[0].Artist != "Track Artist" {
		t.Fatalf("incomplete library: tracks=%d calls=%d", len(tracks), calls)
	}
}

func TestPlexDoesNotForwardTokenOnRedirect(t *testing.T) {
	received := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	p := &plexClient{base: origin.URL, token: "secret", http: origin.Client()}
	res, err := p.request(context.Background(), "GET", "/identity", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if received || res.StatusCode != 307 {
		t.Fatal("Plex credential redirected")
	}
}

func TestPlexPlaylistEnumerationIncludesOwnedIDsBeyondFirstPage(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		end := start + 500
		if end > 501 {
			end = 501
		}
		fmt.Fprintf(w, `<MediaContainer size="%d" totalSize="501">`, end-start)
		for i := start; i < end; i++ {
			fmt.Fprintf(w, `<Playlist ratingKey="%d" title="Playlist" playlistType="audio"/>`, i+1)
		}
		fmt.Fprint(w, `</MediaContainer>`)
	}))
	defer srv.Close()
	p := &plexClient{base: srv.URL, http: srv.Client()}
	lists, err := p.playlists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 501 || lists[500].ID != 501 || requests != 2 {
		t.Fatalf("owned IDs omitted: count=%d requests=%d", len(lists), requests)
	}
}
