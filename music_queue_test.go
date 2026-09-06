package main

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMusicQueueSurvivesRestartAndFailedPublication(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MUSIC_PATH", root)
	t.Setenv("MUSIC_QUEUE_PATH", filepath.Join(root, "queue.json"))
	// An unreadable catalog forces a publication failure after transfer validation.
	t.Setenv("MUSIC_STATE_PATH", root)
	audio, _ := os.ReadFile("testdata/tagged.flac")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
		w.Write(audio)
	}))
	defer srv.Close()
	item := &Item{Name: "song.flac", URL: srv.URL + "?oauth_token=private", FileId: 88, Type: Music}
	if err := persistMusicJob(item); err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(musicQueuePath())
	if stat.Mode().Perm() != 0600 {
		t.Fatal("queue containing source token not private")
	}
	if err := item.downloadMusic(); err == nil {
		t.Fatal("publication should fail")
	}
	old := Jobs
	Jobs = nil
	defer func() { Jobs = old }()
	if err := restoreMusicQueue(); err != nil {
		t.Fatal(err)
	}
	if len(Jobs) != 1 || Jobs[0].FileId != 88 {
		t.Fatal("unverified claimed source lost across restart")
	}
}

func TestMusicUnknownLengthIsRejectedButAuthoritativeSizeAllowsChunkedAudio(t *testing.T) {
	audio, _ := os.ReadFile("testdata/tagged.flac")
	root := t.TempDir()
	t.Setenv("MUSIC_PATH", root)
	t.Setenv("MUSIC_STATE_PATH", filepath.Join(root, "state.json"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); w.Write(audio) }))
	defer srv.Close()
	item := &Item{Name: "song.flac", URL: srv.URL}
	if err := item.downloadMusic(); err == nil || !strings.Contains(err.Error(), "source size") {
		t.Fatalf("unverified size accepted: %v", err)
	}
	item.FileSize = int64(len(audio))
	if err := item.downloadMusic(); err != nil {
		t.Fatalf("authoritative size rejected: %v", err)
	}
}

func TestMusicDownloadEndpointRequiresAuthorizationAndSanitizesState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MUSIC_QUEUE_PATH", filepath.Join(root, "queue.json"))
	t.Setenv("MUSIC_API_TOKEN", "secret")
	old := Jobs
	Jobs = nil
	defer func() { Jobs = old }()
	r := gin.New()
	r.POST("/download", HandleDownload)
	body := `{"name":"song.mp3","type":"movie","url":"https://example.test/song","file_id":5,"completed":true,"started":true,"in_queue":true}`
	for _, auth := range []string{"", "Bearer wrong", "Bearer secret"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/download", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		r.ServeHTTP(w, req)
		if auth != "Bearer secret" && w.Code != 401 {
			t.Fatalf("unauthenticated music accepted: %d", w.Code)
		}
		if auth == "Bearer secret" && w.Code != 200 {
			t.Fatalf("authorized music rejected: %s", w.Body.String())
		}
	}
	if len(Jobs) != 1 || Jobs[0].Completed || Jobs[0].Started || Jobs[0].InQueue {
		t.Fatal("untrusted completion state accepted")
	}
}

func TestMusicRestartAndRequestReplayDoNotDuplicateQueuedOrActiveJob(t *testing.T) {
	t.Setenv("MUSIC_QUEUE_PATH", filepath.Join(t.TempDir(), "queue.json"))
	oldJobs, oldKnown := Jobs, musicKnownJobs
	Jobs = nil
	musicKnownJobs = map[string]bool{}
	defer func() { Jobs = oldJobs; musicKnownJobs = oldKnown }()
	item := &Item{Name: "song.mp3", URL: "https://example.test/audio", Type: Music, FileId: 555}
	if err := persistMusicJob(item); err != nil {
		t.Fatal(err)
	}
	if err := restoreMusicQueue(); err != nil {
		t.Fatal(err)
	}
	added, err := enqueueMusicJob(item)
	if err != nil || added || len(Jobs) != 1 {
		t.Fatalf("restored replay duplicated: added=%v jobs=%d err=%v", added, len(Jobs), err)
	}
	Jobs = nil // Simulate the dequeue-to-active transition.
	added, err = enqueueMusicJob(item)
	if err != nil || added || len(Jobs) != 0 {
		t.Fatal("active job replay duplicated")
	}
	forgetMusicJob(item)
}
