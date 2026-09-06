package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Detects audio being misrouted by legacy upstream movie/TV classification.
func TestMusicLegacyLabelRoutesTaggedAudio(t *testing.T) {
	audio, err := os.ReadFile("testdata/tagged.flac")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio" {
			w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
			w.Write(audio)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "music"), 0755)
	t.Setenv("MUSIC_STATE_PATH", filepath.Join(root, "music-state.json"))
	t.Setenv("MUSIC_PATH", filepath.Join(root, "music"))
	t.Setenv("MUSIC_STAGING_PATH", filepath.Join(root, "staging"))
	oldMovies, oldRemote, oldJobs := MoviesPath, RemoteServer, CurrentJobs
	MoviesPath, RemoteServer, CurrentJobs = root, server.URL, map[string]*Item{}
	t.Cleanup(func() { MoviesPath, RemoteServer, CurrentJobs = oldMovies, oldRemote, oldJobs })
	item := &Item{Name: "02 - wrong movie label.flac", URL: server.URL + "/audio", Type: Movies, FileId: 1}
	if err := item.StartDownload(); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "music", "Rock", "Various Artists", "Test Album", "Disc 01", "02 - Test Song.flac")
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("music not organized at %s: %v", want, err)
	}
	if string(got) != string(audio) {
		t.Fatal("downloaded audio differs")
	}
	if _, err := os.Stat(filepath.Join(root, item.Name)); !os.IsNotExist(err) {
		t.Fatal("audio leaked into movie directory")
	}
	if !item.Completed {
		t.Fatal("verified music not marked completed")
	}
}

func TestMusicDownloadFailuresNeverPublishOrAcknowledge(t *testing.T) {
	for _, mode := range []string{"status", "truncated", "not-audio"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("MUSIC_PATH", root)
			t.Setenv("MUSIC_STATE_PATH", filepath.Join(root, "state.json"))
			ack := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/audio" {
					ack++
					return
				}
				switch mode {
				case "status":
					w.WriteHeader(http.StatusForbidden)
				case "truncated":
					w.Header().Set("Content-Length", "999999")
					w.Write([]byte("short"))
				case "not-audio":
					w.Write([]byte("<html>not music</html>"))
				}
			}))
			defer server.Close()
			old := RemoteServer
			RemoteServer = server.URL
			defer func() { RemoteServer = old }()
			i := &Item{Name: "song.mp3", URL: server.URL + "/audio"}
			if err := i.downloadMusic(); err == nil {
				t.Fatal("bad audio accepted")
			}
			if i.Completed || ack != 0 {
				t.Fatal("failed music acknowledged")
			}
			files, _ := filepath.Glob(filepath.Join(root, ".incoming", "*"))
			if len(files) != 0 {
				t.Fatal("partial download retained")
			}
			if _, err := os.Stat(filepath.Join(root, "state.json")); !os.IsNotExist(err) {
				t.Fatal("failed audio cataloged")
			}
		})
	}
}

func TestMusicPublishKeepsAlbumTogetherAndNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MUSIC_STATE_PATH", filepath.Join(root, "state.json"))
	m := MusicMetadata{Title: "Song", Artist: "Artist", AlbumArtist: "Artist", Album: "Album", Genres: []string{"Rock"}, Track: 1}
	stage := filepath.Join(root, "stage")
	put := func(content string, metadata MusicMetadata) string {
		t.Helper()
		if err := os.WriteFile(stage, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(content))
		path, err := publishMusic(root, stage, &Item{Name: "song.flac", FileId: 10}, metadata, hex.EncodeToString(h[:]))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := put("original audio", m)
	if repeat := put("original audio", m); repeat != first {
		t.Fatal("retry created duplicate")
	}
	second := put("different recording", m)
	if second == first {
		t.Fatal("collision overwrote audio")
	}
	if repeat := put("different recording", m); repeat != second {
		t.Fatal("collision retry created duplicate")
	}
	got, _ := os.ReadFile(first)
	if string(got) != "original audio" {
		t.Fatal("existing audio changed")
	}
	m.Title = "Another"
	m.Track = 2
	m.Genres = []string{"Blues"}
	third := put("second song", m)
	if filepath.Dir(first) != filepath.Dir(third) {
		t.Fatal("mixed-genre album split across directories")
	}
	data, _ := os.ReadFile(filepath.Join(root, "state.json"))
	var state musicImportState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Files) != 3 {
		t.Fatalf("expected three durable files, got %d", len(state.Files))
	}
}

func TestMusicMetadataAndSafeFallback(t *testing.T) {
	m, err := readMusicMetadata("testdata/tagged.flac")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Test Song" || m.AlbumArtist != "Various Artists" || m.Track != 2 || m.Disc != 1 || len(m.Genres) != 2 {
		t.Fatalf("bad tags: %+v", m)
	}
	path := musicRelativePath(MusicMetadata{}, "plain.mp3", "Rock")
	if path != "Unsorted/Unknown Artist/plain/plain.mp3" {
		t.Fatalf("unsafe fallback: %s", path)
	}
	path = musicRelativePath(MusicMetadata{Title: "../../escape", AlbumArtist: "/../../evil", Album: "..", Genres: []string{"/root"}}, "song.flac", "/root")
	if filepath.IsAbs(path) || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
		t.Fatalf("path escapes root: %q", path)
	}
}

func TestMusicRejectsSymlinkDestinationAndCorruptState(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "Rock")); err != nil {
		t.Fatal(err)
	}
	if err := musicMkdirAll(filepath.Join(root, "Rock", "Artist")); err == nil {
		t.Fatal("followed genre symlink")
	}
	files, _ := os.ReadDir(outside)
	if len(files) != 0 {
		t.Fatal("wrote outside music root")
	}
	state := filepath.Join(root, "state.json")
	os.WriteFile(state, []byte("corrupt"), 0600)
	if _, err := readMusicState(state); err == nil {
		t.Fatal("discarded corrupt state")
	}
}

func TestAudioExtensionsOverrideVideoReleasePatterns(t *testing.T) {
	for _, name := range []string{"01 - Song.MP3", "Album.S01E01.FLAC", "song.m4a", "song.opus"} {
		if !isAudioFilename(name) {
			t.Fatalf("audio not identified: %s", name)
		}
	}
	for _, name := range []string{"movie.mkv", "cover.jpg", "music.zip", "song.mp3.exe"} {
		if isAudioFilename(name) {
			t.Fatalf("non-audio identified: %s", name)
		}
	}
}

func TestMusicAcknowledgementRetriesDurablyWithoutDownloadingAgain(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	t.Setenv("MUSIC_STATE_PATH", statePath)
	stage := filepath.Join(root, "stage")
	os.WriteFile(stage, []byte("verified audio"), 0600)
	h := sha256.Sum256([]byte("verified audio"))
	item := &Item{Name: "song.flac", FileId: 77}
	path, err := publishMusic(root, stage, item, MusicMetadata{}, hex.EncodeToString(h[:]))
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/updateQueue" {
			t.Errorf("unexpected redownload: %s", r.URL.Path)
		}
		attempts++
		var ack Item
		if err := json.NewDecoder(r.Body).Decode(&ack); err != nil {
			t.Error(err)
		}
		if ack.FileId != 77 || !ack.Completed {
			t.Errorf("bad acknowledgement: %+v", ack)
		}
		if fail {
			w.WriteHeader(503)
		} else {
			w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer server.Close()
	old := RemoteServer
	RemoteServer = server.URL
	defer func() { RemoteServer = old }()
	retryMusicAcknowledgements()
	state, err := readMusicState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.Files[path].Acknowledged {
		t.Fatal("failed acknowledgement recorded as accepted")
	}
	fail = false
	retryMusicAcknowledgements()
	retryMusicAcknowledgements()
	state, err = readMusicState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Files[path].Acknowledged || attempts != 2 {
		t.Fatalf("acknowledgement not persisted/idempotent: %+v attempts=%d", state.Files[path], attempts)
	}
}

func TestMusicDownloadRejectsUnknownOrMismatchedSourceSize(t *testing.T) {
	for _, tc := range []struct {
		name       string
		itemSize   int64
		headerSize string
	}{
		{name: "unknown", itemSize: 0, headerSize: ""},
		{name: "item mismatch", itemSize: 4, headerSize: "3"},
		{name: "header mismatch", itemSize: 3, headerSize: "4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("MUSIC_PATH", root)
			t.Setenv("MUSIC_STATE_PATH", filepath.Join(root, "state.json"))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.headerSize != "" {
					w.Header().Set("Content-Length", tc.headerSize)
				}
				_, _ = w.Write([]byte("abc"))
			}))
			defer srv.Close()
			i := &Item{Name: "song.mp3", URL: srv.URL, FileSize: tc.itemSize}
			if err := i.downloadMusic(); err == nil {
				t.Fatal("accepted invalid source size")
			}
		})
	}
}

func TestMusicReceiptsKeepDuplicateFileIDsWithSamePath(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	t.Setenv("MUSIC_STATE_PATH", statePath)
	stage := filepath.Join(root, "stage")
	if err := os.WriteFile(stage, []byte("same audio"), 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("same audio"))
	m := MusicMetadata{Title: "Same", AlbumArtist: "Artist", Album: "Album", Genres: []string{"Rock"}}
	if _, err := publishMusic(root, stage, &Item{Name: "a.flac", FileId: 1}, m, hex.EncodeToString(h[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := publishMusic(root, stage, &Item{Name: "b.flac", FileId: 2}, m, hex.EncodeToString(h[:])); err != nil {
		t.Fatal(err)
	}
	state, err := readMusicState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Receipts) != 2 {
		t.Fatalf("receipts=%d want 2", len(state.Receipts))
	}
}
