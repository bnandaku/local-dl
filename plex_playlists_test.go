package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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
