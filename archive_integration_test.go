package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type archiveTestBot struct {
	set              archiveSet
	data             map[int64][]byte
	calls            []string
	failCleanupOnce  bool
	failFirstCleanup bool
	failDownload     bool
	cleanupCalls     int
	retries          int
	t                *testing.T
	root             string
}

func setupArchivePipeline(t *testing.T, fixture string) (*archiveTestBot, *archiveWorkerState, *archiveJob) {
	t.Helper()
	tool := realRARTool(t)
	oldTool := archiveTool
	archiveTool = tool
	t.Cleanup(func() { archiveTool = oldTool })
	root := t.TempDir()
	t.Setenv("ARCHIVE_STATE_PATH", filepath.Join(root, "archive.json"))
	t.Setenv("ARCHIVE_STAGING_PATH", filepath.Join(root, "stage"))
	t.Setenv("MUSIC_STATE_PATH", filepath.Join(root, "music.json"))
	t.Setenv("MUSIC_PATH", filepath.Join(root, "Music"))
	t.Setenv("BOT_SERVICE_TOKEN", "test")
	oldMovies, oldTV := MoviesPath, TVShowPath
	MoviesPath, TVShowPath = filepath.Join(root, "Movies"), filepath.Join(root, "TV")
	os.Mkdir(MoviesPath, 0700)
	os.Mkdir(TVShowPath, 0700)
	t.Cleanup(func() { MoviesPath, TVShowPath = oldMovies, oldTV })
	os.Mkdir(filepath.Join(root, "Music"), 0700)
	b := &archiveTestBot{t: t, root: root, data: map[int64][]byte{}, set: archiveSet{ID: "rar-test", AttemptID: 12, RequestID: "request-test", MediaType: "music", Name: "album.rar", State: "pending", Complete: true, Volumes: []archiveVolume{}, Outputs: []archiveOutput{}}}
	files, e := os.ReadDir(fixture)
	if e != nil {
		t.Fatal(e)
	}
	for i, f := range files {
		if f.IsDir() {
			continue
		}
		data, e := os.ReadFile(filepath.Join(fixture, f.Name()))
		if e != nil {
			t.Fatal(e)
		}
		id := int64(i + 1)
		b.data[id] = data
		b.set.Volumes = append(b.set.Volumes, archiveVolume{FileID: id, Filename: f.Name(), Size: int64(len(data)), Cleanup: "pending"})
	}
	srv := httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(srv.Close)
	t.Setenv("ARCHIVE_DOWNLOAD_ORIGINS", srv.URL)
	for i := range b.set.Volumes {
		v := &b.set.Volumes[i]
		v.URL = srv.URL + "/volume/" + strconv.FormatInt(v.FileID, 10)
	}
	old := RemoteServer
	RemoteServer = srv.URL
	t.Cleanup(func() { RemoteServer = old })
	state := newArchiveState()
	job := &archiveJob{ID: b.set.ID, AttemptID: b.set.AttemptID, RequestID: b.set.RequestID, Receipts: map[string]archiveReceipt{}}
	state.Jobs[job.ID] = job
	if e := saveArchiveState(state); e != nil {
		t.Fatal(e)
	}
	return b, &state, job
}
func (b *archiveTestBot) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/volume/") {
		if b.failDownload {
			w.WriteHeader(503)
			return
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/volume/"), 10, 64)
		w.Header().Set("Content-Length", strconv.Itoa(len(b.data[id])))
		w.Write(b.data[id])
		return
	}
	b.calls = append(b.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer test" {
		b.t.Error("archive API missing bearer")
	}
	encode := func() { json.NewEncoder(w).Encode(b.set) }
	switch r.URL.Path {
	case "/api/v1/archives":
		json.NewEncoder(w).Encode(map[string]interface{}{"items": []archiveSet{b.set}, "next_after": ""})
	case archiveURL(b.set.ID):
		encode()
	case archiveURL(b.set.ID) + "/outputs":
		var d struct {
			Complete bool                    `json:"complete"`
			Outputs  []archiveDeclaredOutput `json:"outputs"`
		}
		if e := json.NewDecoder(r.Body).Decode(&d); e != nil || !d.Complete {
			b.t.Errorf("invalid declaration %v", e)
		}
		if !b.set.OutputsDeclared {
			for i, o := range d.Outputs {
				b.set.Outputs = append(b.set.Outputs, archiveOutput{ID: fmt.Sprintf("out-%d", i), Path: o.Path, Size: o.Size, Required: isAudioFilename(o.Path) || mediaKind(o.Path) == "video" && !strings.Contains(strings.ToLower(o.Path), "trailer") && filepath.Base(o.Path) != "sample.mp4", Status: "pending"})
			}
			b.set.OutputsDeclared = true
			b.set.State = "extracting"
		}
		encode()
	case archiveURL(b.set.ID) + "/report":
		var report archiveReport
		json.NewDecoder(r.Body).Decode(&report)
		b.set.State = report.Status
		b.set.Reason = report.Reason
		encode()
	case "/api/v1/links/12/cleanup", "/api/v1/links/12/discard":
		success := strings.HasSuffix(r.URL.Path, "cleanup")
		if success && b.set.State != "validated" {
			b.t.Error("source cleanup before output validation")
		}
		if !success && b.set.State != "bad" && b.set.State != "failed" {
			b.t.Error("source discard before failure report")
		}
		b.cleanupCalls++
		if (b.failCleanupOnce && b.cleanupCalls == 2) || (b.failFirstCleanup && b.cleanupCalls == 1) {
			w.WriteHeader(503)
			w.Write([]byte(`{"code":"cleanup_uncertain","error":"retry","retryable":true}`))
			return
		}
		var body struct {
			Event string `json:"event_id"`
			File  int64  `json:"file_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for i := range b.set.Volumes {
			if b.set.Volumes[i].FileID == body.File {
				b.set.Volumes[i].Cleanup = "discarded"
				if success {
					b.set.Volumes[i].Cleanup = "purged"
				}
				b.set.Volumes[i].URL = ""
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"event_id": body.Event, "attempt_id": 12, "file_id": body.File, "status": "done"})
	case "/api/v1/links/12/confirm":
		for _, v := range b.set.Volumes {
			if v.Cleanup != "purged" {
				b.t.Error("confirmation before full purge")
			}
		}
		w.Write([]byte(`{"confirmed_id":12}`))
	case "/api/v1/recovery/requests/request-test":
		w.Write([]byte(`{"request":{"id":"request-test","state":"failed","attempts":1,"attempt_limit":5,"retryable":true}}`))
	case "/api/v1/recovery/requests/request-test/retry":
		b.retries++
		w.Write([]byte(`{"id":"request-test","state":"queued","attempts":2,"attempt_limit":5}`))
	default:
		if strings.HasPrefix(r.URL.Path, archiveURL(b.set.ID)+"/outputs/") && strings.HasSuffix(r.URL.Path, "/report") {
			var report archiveReport
			json.NewDecoder(r.Body).Decode(&report)
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, archiveURL(b.set.ID)+"/outputs/"), "/report")
			for i := range b.set.Outputs {
				if b.set.Outputs[i].ID != id {
					continue
				}
				o := &b.set.Outputs[i]
				if report.Status == "validated" {
					if !o.Required || !report.Published {
						b.t.Error("unpublished/nonrequired output validated")
					}
					root, e := archiveLibraryRoot(report.LibraryType)
					if e != nil {
						b.t.Fatal(e)
					}
					digest, e := musicFileDigest(filepath.Join(root, report.LibraryKey))
					if e != nil || digest != report.SHA256 {
						b.t.Error("output attested before durable publication")
					}
					o.SHA256, o.LibraryType, o.LibraryKey = report.SHA256, report.LibraryType, report.LibraryKey
				}
				o.Status = report.Status
				if report.Status == "bad" || report.Status == "failed" {
					b.set.State = report.Status
					b.set.Reason = report.Reason
				}
			}
			complete := true
			for _, o := range b.set.Outputs {
				if o.Required && o.Status != "validated" || !o.Required && o.Status != "skipped" {
					complete = false
				}
			}
			if complete {
				b.set.State = "validated"
			}
			encode()
			return
		}
		b.t.Errorf("unexpected archive endpoint %s", r.URL.Path)
		w.WriteHeader(404)
	}
}
func TestArchivePipelineImportsMusicCoverAndSkipsGarbage(t *testing.T) {
	b, s, j := setupArchivePipeline(t, "testdata/rar/music-valid")
	if e := processArchiveJob(context.Background(), s, j); e != nil {
		t.Fatal(e)
	}
	if !j.Done || b.set.State != "validated" || b.retries != 0 {
		t.Fatalf("unfinished archive: %v", b.set)
	}
	if len(b.set.Outputs) != 4 {
		t.Fatalf("partial output declaration: %v", b.set.Outputs)
	}
	files := []string{}
	filepath.WalkDir(filepath.Join(b.root, "Music"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 2 {
		t.Fatalf("support missing or garbage published: %v", files)
	}
	for _, p := range files {
		if strings.HasSuffix(p, ".exe") || strings.HasSuffix(p, ".txt") {
			t.Fatal("garbage imported")
		}
	}
	if len(j.Receipts) != 2 {
		t.Fatalf("primary/cover receipts missing: %v", j.Receipts)
	}
}
func TestArchivePartialCleanupRestartsWithoutReextracting(t *testing.T) {
	b, s, j := setupArchivePipeline(t, "testdata/rar/music-valid")
	b.failCleanupOnce = true
	if e := processArchiveJob(context.Background(), s, j); e == nil {
		t.Fatal("expected interrupted cleanup")
	}
	saved, e := loadArchiveState()
	if e != nil {
		t.Fatal(e)
	}
	b.failDownload = true
	b.failCleanupOnce = false
	if e := processArchiveJob(context.Background(), &saved, saved.Jobs[j.ID]); e != nil {
		t.Fatalf("could not resume from published receipts: %v", e)
	}
	if !saved.Jobs[j.ID].Done {
		t.Fatal("cleanup not completed")
	}
}
func TestArchiveMissingSourceVolumeDiscardsThenRequestsReplacement(t *testing.T) {
	b, s, j := setupArchivePipeline(t, "testdata/rar/music-valid")
	b.set.Volumes = append(b.set.Volumes[:1], b.set.Volumes[2:]...)
	if e := processArchiveJob(context.Background(), s, j); e != nil {
		t.Fatal(e)
	}
	if b.set.State != "bad" || b.set.Reason != "archive_missing_volumes" || b.retries != 1 || !j.Done {
		t.Fatalf("missing volume did not recover: %v retries %d", b.set, b.retries)
	}
}
func TestArchiveNetworkFailurePreservesEverySource(t *testing.T) {
	b, s, j := setupArchivePipeline(t, "testdata/rar/music-valid")
	b.failDownload = true
	if e := processArchiveJob(context.Background(), s, j); e == nil {
		t.Fatal("expected operational hold")
	}
	if b.cleanupCalls != 0 || b.retries != 0 || b.set.State != "failed" || b.set.Reason != "network" {
		t.Fatalf("unsafe network recovery: %v", b.set)
	}
}

func TestArchivePipelineMovieRetainsSubtitleTrailerAndArt(t *testing.T) {
	b, s, j := setupArchivePipeline(t, "testdata/rar/video-valid")
	b.set.MediaType = "movie"
	if e := processArchiveJob(context.Background(), s, j); e != nil {
		t.Fatal(e)
	}
	if !j.Done || len(b.set.Outputs) != 6 {
		t.Fatalf("incomplete manifest: %v", b.set.Outputs)
	}
	files := []string{}
	filepath.WalkDir(MoviesPath, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 4 {
		t.Fatalf("expected movie/subtitle/trailer/art only: %v", files)
	}
	for _, p := range files {
		if strings.HasSuffix(p, ".nfo") || strings.Contains(p, "sample") {
			t.Fatal("garbage imported")
		}
	}
}
func TestArchiveCorruptAndPasswordSetsRequeueWithReasons(t *testing.T) {
	for _, tc := range []struct{ fixture, reason string }{{"modern-corrupt", "archive_corrupt"}, {"modern-password", "archive_password_locked"}} {
		t.Run(tc.fixture, func(t *testing.T) {
			b, s, j := setupArchivePipeline(t, "testdata/rar/"+tc.fixture)
			if e := processArchiveJob(context.Background(), s, j); e != nil {
				t.Fatal(e)
			}
			if b.set.Reason != tc.reason || b.retries != 1 || !j.Done {
				t.Fatalf("bad set did not recover: %s %d", b.set.Reason, b.retries)
			}
		})
	}
}
func TestArchiveMissingPublishedFileBlocksRemainingCleanup(t *testing.T) {
	b, s, j := setupArchivePipeline(t, "testdata/rar/music-valid")
	b.failCleanupOnce = true
	if e := processArchiveJob(context.Background(), s, j); e == nil {
		t.Fatal("expected interrupted cleanup")
	}
	for _, o := range b.set.Outputs {
		if o.Required {
			if e := os.Remove(j.Receipts[o.ID].Path); e != nil {
				t.Fatal(e)
			}
		}
	}
	calls := b.cleanupCalls
	b.failCleanupOnce = false
	if e := processArchiveJob(context.Background(), s, j); e == nil {
		t.Fatal("deleted sources after publication disappeared")
	}
	if calls != b.cleanupCalls {
		t.Fatal("cleanup continued without local media")
	}
}

func TestArchiveRestoresMissingPublicationBeforeAnySourceCleanup(t *testing.T) {
	b, s, j := setupArchivePipeline(t, "testdata/rar/music-valid")
	b.failFirstCleanup = true
	if e := processArchiveJob(context.Background(), s, j); e == nil {
		t.Fatal("expected interrupted cleanup")
	}
	for _, o := range b.set.Outputs {
		if o.Required {
			if e := os.Remove(j.Receipts[o.ID].Path); e != nil {
				t.Fatal(e)
			}
		}
	}
	before := len(b.calls)
	saved, e := loadArchiveState()
	if e != nil {
		t.Fatal(e)
	}
	if e = processArchiveJob(context.Background(), &saved, saved.Jobs[j.ID]); e != nil {
		t.Fatal(e)
	}
	for _, call := range b.calls[before:] {
		if strings.Contains(call, "/outputs") {
			t.Fatalf("repair changed existing server receipt: %s", call)
		}
	}
	if !saved.Jobs[j.ID].Done {
		t.Fatal("restoration did not finish")
	}
}
func TestArchiveWrongLibraryReceiptNeverAuthorizesCleanup(t *testing.T) {
	b, s, j := setupArchivePipeline(t, "testdata/rar/video-valid")
	b.set.State = "validated"
	b.set.OutputsDeclared = true
	p := filepath.Join(b.root, "Music", "movie.mp4")
	if e := os.WriteFile(p, []byte("receipt bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	digest, e := musicFileDigest(p)
	if e != nil {
		t.Fatal(e)
	}
	b.set.Outputs = []archiveOutput{{ID: "video", Path: "movie.mp4", Size: 13, Required: true, Status: "validated", SHA256: digest, LibraryType: "music", LibraryKey: "movie.mp4"}}
	if e := processArchiveJob(context.Background(), s, j); e == nil {
		t.Fatal("accepted video in Music")
	}
	if b.cleanupCalls != 0 || b.retries != 0 {
		t.Fatal("wrong library receipt authorized source deletion")
	}
}
func TestArchiveRetryWaitsForEveryVolumeDiscard(t *testing.T) {
	old := RemoteServer
	t.Cleanup(func() { RemoteServer = old })
	t.Setenv("BOT_SERVICE_TOKEN", "test")
	cleanup := "pending"
	retries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/recovery/requests/request-test":
			w.Write([]byte(`{"request":{"id":"request-test","state":"failed","attempts":1,"attempt_limit":5,"retryable":true},"attempts":[{"id":12,"status":"bad"}]}`))
		case "/api/v1/links/12/files":
			fmt.Fprintf(w, `{"items":[{"file_id":1,"filename":"film.part01.rar","cleanup":%q}]}`, cleanup)
		case "/api/v1/recovery/requests/request-test/retry":
			retries++
			w.Write([]byte(`{"id":"request-test","state":"queued","attempts":2,"attempt_limit":5}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	RemoteServer = server.URL
	if e := retryRecoveryRequest("request-test"); e == nil {
		t.Fatal("retry before discard")
	}
	if retries != 0 {
		t.Fatal("replacement raced source cleanup")
	}
	cleanup = "discarded"
	if e := retryRecoveryRequest("request-test"); e != nil {
		t.Fatal(e)
	}
	if retries != 1 {
		t.Fatal("replacement missing after discard")
	}
}
