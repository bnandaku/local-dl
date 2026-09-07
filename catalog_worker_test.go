package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func setupCatalogTest(t *testing.T) {
	t.Helper()
	oldDB, oldPath, oldMovies, oldTV := CatalogDB, CatalogPath, MoviesPath, TVShowPath
	root := t.TempDir()
	CatalogPath = filepath.Join(root, "catalog.db")
	MoviesPath = filepath.Join(root, "Movies")
	TVShowPath = filepath.Join(root, "TV")
	for _, p := range []string{MoviesPath, TVShowPath} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := InitCatalog(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		CatalogDB.Close()
		CatalogDB, CatalogPath, MoviesPath, TVShowPath = oldDB, oldPath, oldMovies, oldTV
	})
	t.Setenv("BOT_SERVICE_TOKEN", "test")
	t.Setenv("CATALOG_ALLOW_EMPTY_ROOTS", "movie,tv")
}
func TestCatalogDirtyGenerationIsTransactional(t *testing.T) {
	setupCatalogTest(t)
	before, e := readCatalogSyncState()
	if e != nil {
		t.Fatal(e)
	}
	tx, e := CatalogDB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`INSERT INTO files(file_path,filename,media_type,file_size) VALUES ('/a','a.mp4','movie',1)`); e != nil {
		t.Fatal(e)
	}
	tx.Rollback()
	after, _ := readCatalogSyncState()
	if after.Generation != before.Generation {
		t.Fatal("rolled back mutation dirtied catalog")
	}
	if _, e = CatalogDB.Exec(`INSERT INTO files(file_path,filename,media_type,file_size) VALUES ('/a','a.mp4','movie',1)`); e != nil {
		t.Fatal(e)
	}
	after, _ = readCatalogSyncState()
	if after.Generation != before.Generation+1 {
		t.Fatal("committed addition not durable")
	}
	CatalogDB.Exec(`UPDATE files SET last_seen_at=CURRENT_TIMESTAMP WHERE file_path='/a'`)
	same, _ := readCatalogSyncState()
	if same.Generation != after.Generation {
		t.Fatal("last-seen dirtied snapshot")
	}
	if e = RemoveFileFromCatalog("/a"); e != nil {
		t.Fatal(e)
	}
	removed, _ := readCatalogSyncState()
	if removed.Generation != after.Generation+1 {
		t.Fatal("removal not durable")
	}
}
func TestCatalogIdleAndDailySync(t *testing.T) {
	setupCatalogTest(t)
	old := RemoteServer
	t.Cleanup(func() { RemoteServer = old })
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"message": "Catalog updated successfully", "shows": 0, "episodes": 0})
	}))
	defer server.Close()
	RemoteServer = server.URL
	now := time.Now()
	if e := catalogSyncCycle(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	if sends != 1 {
		t.Fatal("initial sync missing")
	}
	for i := 1; i < 288; i++ {
		if e := catalogSyncCycle(context.Background(), now.Add(time.Duration(i)*5*time.Minute)); e != nil {
			t.Fatal(e)
		}
	}
	if sends != 1 {
		t.Fatalf("idle uploads: %d", sends)
	}
	if e := catalogSyncCycle(context.Background(), now.Add(24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if sends != 2 {
		t.Fatal("daily sync missing")
	}
}

func catalogTestVideo(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	tool, e := findMediaTool("ffmpeg")
	if e != nil {
		t.Fatal(e)
	}
	if b, e := exec.Command(tool, "-v", "error", "-f", "lavfi", "-i", "color=size=16x16:rate=1", "-t", "1", "-c:v", "mpeg4", path).CombinedOutput(); e != nil {
		t.Fatalf("fixture: %v %s", e, b)
	}
	return path
}
func catalogTestServer(t *testing.T, fn func(CatalogSyncData, []byte) (int, string)) {
	t.Helper()
	old := RemoteServer
	t.Cleanup(func() { RemoteServer = old })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/catalogUpdate" || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("wrong catalog contract")
		}
		reader, e := gzip.NewReader(r.Body)
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		defer reader.Close()
		b, e := io.ReadAll(reader)
		if e != nil {
			t.Error(e)
		}
		var payload CatalogSyncData
		if e = json.Unmarshal(b, &payload); e != nil {
			t.Error(e)
		}
		status, reply := fn(payload, b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if reply != "" {
			w.Write([]byte(reply))
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"message": "Catalog updated successfully", "shows": payload.Statistics.TotalShows, "episodes": payload.Statistics.TotalEpisodes})
	}))
	t.Cleanup(server.Close)
	RemoteServer = server.URL
}
func TestCatalogDebouncesChangesAndSendsCompleteMoves(t *testing.T) {
	setupCatalogTest(t)
	now := time.Now()
	var received []CatalogSyncData
	catalogTestServer(t, func(d CatalogSyncData, _ []byte) (int, string) { received = append(received, d); return 200, "" })
	if e := catalogSyncCycle(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	a := catalogTestVideo(t, MoviesPath, "Film.2020.mp4")
	if e := AddFileToCatalog(a); e != nil {
		t.Fatal(e)
	}
	if e := catalogSyncCycle(context.Background(), now.Add(10*time.Second)); e != nil {
		t.Fatal(e)
	}
	if len(received) != 1 {
		t.Fatal("burst was not debounced")
	}
	b := filepath.Join(MoviesPath, "Renamed.2020.mp4")
	if e := os.Rename(a, b); e != nil {
		t.Fatal(e)
	}
	if e := UpdateCatalogAfterMove(a, b); e != nil {
		t.Fatal(e)
	}
	if e := catalogSyncCycle(context.Background(), now.Add(40*time.Second)); e != nil {
		t.Fatal(e)
	}
	if len(received) != 2 || len(received[1].Movies) != 1 || received[1].Movies[0].FilePath != b {
		t.Fatal("move snapshot split old and new paths")
	}
	before, _ := readCatalogSyncState()
	if e := AddFileToCatalog(b); e != nil {
		t.Fatal(e)
	}
	after, _ := readCatalogSyncState()
	if before.Generation != after.Generation {
		t.Fatal("unchanged record triggered upload")
	}
	os.Remove(b)
	if e := RemoveFileFromCatalog(b); e != nil {
		t.Fatal(e)
	}
	if e := catalogSyncCycle(context.Background(), now.Add(80*time.Second)); e != nil {
		t.Fatal(e)
	}
	if len(received) != 3 || len(received[2].Movies) != 0 {
		t.Fatal("removal missing")
	}
}
func TestCatalogUncertainSnapshotSurvivesRestartAndNewChanges(t *testing.T) {
	setupCatalogTest(t)
	now := time.Now()
	var payloads [][]byte
	fail := true
	catalogTestServer(t, func(_ CatalogSyncData, b []byte) (int, string) {
		payloads = append(payloads, append([]byte(nil), b...))
		if fail {
			return 503, `{"error":"uncertain"}`
		}
		return 200, ""
	})
	if e := catalogSyncCycle(context.Background(), now); e == nil {
		t.Fatal("expected uncertain upload")
	}
	saved, _ := readCatalogSyncState()
	if len(saved.Pending) == 0 || saved.Acknowledged != 0 || saved.NextFull != 0 {
		t.Fatal("lost pending reconciliation")
	}
	path := catalogTestVideo(t, MoviesPath, "Later.2020.mp4")
	if e := AddFileToCatalog(path); e != nil {
		t.Fatal(e)
	}
	if e := CatalogDB.Close(); e != nil {
		t.Fatal(e)
	}
	if e := InitCatalog(); e != nil {
		t.Fatal(e)
	}
	fail = false
	if e := catalogSyncCycle(context.Background(), time.Unix(saved.RetryAt+1, 0)); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(payloads[0], payloads[1]) {
		t.Fatal("uncertain retry changed captured snapshot")
	}
	state, _ := readCatalogSyncState()
	if state.Generation <= state.Acknowledged {
		t.Fatal("acknowledged unseen mutation")
	}
	if e := catalogSyncCycle(context.Background(), now.Add(3*time.Minute)); e != nil {
		t.Fatal(e)
	}
	var latest CatalogSyncData
	json.Unmarshal(payloads[2], &latest)
	if len(latest.Movies) != 1 || latest.Movies[0].FilePath != path {
		t.Fatal("new generation missing")
	}
}
func TestCatalogMutationDuringUploadAndRacingResyncSerialize(t *testing.T) {
	setupCatalogTest(t)
	now := time.Now()
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var max atomic.Int32
	catalogTestServer(t, func(_ CatalogSyncData, _ []byte) (int, string) {
		n := active.Add(1)
		if n > max.Load() {
			max.Store(n)
		}
		defer active.Add(-1)
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return 200, ""
	})
	result := make(chan error, 1)
	go func() { result <- catalogSyncCycle(context.Background(), now) }()
	<-entered
	path := catalogTestVideo(t, MoviesPath, "Concurrent.2020.mp4")
	if e := AddFileToCatalog(path); e != nil {
		t.Fatal(e)
	}
	if e := RequestCatalogReconciliation(); e != nil {
		t.Fatal(e)
	}
	second := make(chan error, 1)
	go func() { second <- catalogSyncCycle(context.Background(), now.Add(time.Minute)) }()
	close(release)
	if e := <-result; e != nil {
		t.Fatal(e)
	}
	if e := <-second; e != nil {
		t.Fatal(e)
	}
	s, _ := readCatalogSyncState()
	if s.Generation != s.Acknowledged || s.Requested != s.CompletedRequest || calls.Load() != 2 || max.Load() != 1 {
		t.Fatalf("lost or concurrent sync: %+v", s)
	}
}
func TestCatalogStorageOutagePreservesSnapshot(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "identity", "scan-error"} {
		t.Run(mode, func(t *testing.T) {
			setupCatalogTest(t)
			now := time.Now()
			sends := 0
			catalogTestServer(t, func(_ CatalogSyncData, _ []byte) (int, string) { sends++; return 200, "" })
			p := catalogTestVideo(t, MoviesPath, "Kept.2020.mp4")
			if e := catalogSyncCycle(context.Background(), now); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "missing":
				os.Rename(MoviesPath, MoviesPath+"-offline")
			case "identity":
				os.Rename(MoviesPath, MoviesPath+"-offline")
				os.Mkdir(MoviesPath, 0700)
			case "empty":
				os.Remove(p)
			case "scan-error":
				os.Symlink("/missing", filepath.Join(TVShowPath, "unreadable"))
			}
			RequestCatalogReconciliation()
			if e := catalogSyncCycle(context.Background(), now.Add(time.Minute)); e == nil {
				t.Fatal("outage accepted")
			}
			if sends != 1 {
				t.Fatal("uploaded incomplete catalog")
			}
			var status string
			if e := CatalogDB.QueryRow(`SELECT status FROM files WHERE file_path=?`, p).Scan(&status); e != nil || status != "active" {
				t.Fatal("outage erased local catalog")
			}
		})
	}
}
func TestCatalogAcknowledgementAndAuthAreStrict(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		auth bool
	}{{"html", 200, "<html>ok</html>", false}, {"empty", 200, "{}", false}, {"wrong-count", 200, `{"message":"Catalog updated successfully","shows":5,"episodes":0}`, false}, {"auth", 401, "{}", true}} {
		t.Run(tc.name, func(t *testing.T) {
			setupCatalogTest(t)
			now := time.Now()
			calls := 0
			catalogTestServer(t, func(_ CatalogSyncData, _ []byte) (int, string) { calls++; return tc.code, tc.body })
			if e := catalogSyncCycle(context.Background(), now); e == nil {
				t.Fatal("accepted invalid acknowledgement")
			}
			s, _ := readCatalogSyncState()
			if len(s.Pending) == 0 || s.NextFull != 0 {
				t.Fatal("forgot pending snapshot")
			}
			catalogSyncCycle(context.Background(), now.Add(time.Second))
			if calls != 1 {
				t.Fatal("retry hot loop")
			}
			if tc.auth && s.RetryAt < now.Add(29*time.Minute).Unix() {
				t.Fatal("authentication not paused")
			}
		})
	}
}
func TestCatalogReconciliationRejectsIncompleteAndWrongLibraryMedia(t *testing.T) {
	setupCatalogTest(t)
	catalogTestVideo(t, MoviesPath, "Valid.2020.mp4")
	catalogTestVideo(t, MoviesPath, ".partial.mp4")
	catalogTestVideo(t, MoviesPath, "Show.S01E01.mp4")
	os.WriteFile(filepath.Join(MoviesPath, "Bad.2020.mp4"), []byte("not media"), 0600)
	if e := ScanAndUpdateCatalog(); e != nil {
		t.Fatal(e)
	}
	data, e := GetCatalogSyncData()
	if e != nil {
		t.Fatal(e)
	}
	if len(data.Movies) != 1 || data.Movies[0].Filename != "Valid.2020.mp4" {
		t.Fatal("invalid or incomplete files cataloged")
	}
}
func TestCatalogBurstMaximumDelay(t *testing.T) {
	now := time.Now()
	s := catalogSyncState{Generation: 2, Acknowledged: 1, FirstDirty: now.Add(-121 * time.Second).Unix(), LastDirty: now.Unix()}
	if !catalogBatchDue(s, now) {
		t.Fatal("continuous mutations postponed sync indefinitely")
	}
	s.FirstDirty = now.Add(-119 * time.Second).Unix()
	if catalogBatchDue(s, now) {
		t.Fatal("batch sent before debounce or ceiling")
	}
}

func TestCatalogNewDatabaseNeedsExplicitEmptyRootEnrollment(t *testing.T) {
	setupCatalogTest(t)
	t.Setenv("CATALOG_ALLOW_EMPTY_ROOTS", "")
	sends := 0
	catalogTestServer(t, func(_ CatalogSyncData, _ []byte) (int, string) { sends++; return 200, "" })
	if e := catalogSyncCycle(context.Background(), time.Now()); e == nil {
		t.Fatal("empty untrusted mount enrolled")
	}
	if sends != 0 {
		t.Fatal("fresh empty DB replaced remote catalog")
	}
}
func TestCatalogTVMetadataUsesEpisodeParent(t *testing.T) {
	setupCatalogTest(t)
	for _, folder := range []string{"Show.S01E01", "Show.S01E02"} {
		root := filepath.Join(TVShowPath, folder)
		os.Mkdir(root, 0700)
		catalogTestVideo(t, root, "video.mp4")
	}
	if e := ScanAndUpdateCatalog(); e != nil {
		t.Fatal(e)
	}
	snapshot, e := GetCatalogSyncData()
	if e != nil {
		t.Fatal(e)
	}
	if len(snapshot.Shows) != 1 || snapshot.Shows[0].ShowName != "Show" || len(snapshot.Shows[0].Seasons) != 1 || len(snapshot.Shows[0].Seasons[0].Episodes) != 2 {
		t.Fatal("parent episode metadata lost")
	}
	if snapshot.Shows[0].Seasons[0].Episodes[0].Episode == snapshot.Shows[0].Seasons[0].Episodes[1].Episode {
		t.Fatal("episodes collapse into one key")
	}
}

func TestCatalogUnknownDeliveryFencesNewerSnapshotsAcrossRestart(t *testing.T) {
	setupCatalogTest(t)
	now := time.Now()
	old := RemoteServer
	t.Cleanup(func() { RemoteServer = old })
	var payloads [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, e := gzip.NewReader(r.Body)
		if e != nil {
			t.Error(e)
			return
		}
		b, e := io.ReadAll(reader)
		reader.Close()
		if e != nil {
			t.Error(e)
		}
		payloads = append(payloads, append([]byte(nil), b...))
		if len(payloads) == 1 {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			conn.Close()
			return
		}
		var d CatalogSyncData
		json.Unmarshal(b, &d)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"message": "Catalog updated successfully", "shows": d.Statistics.TotalShows, "episodes": d.Statistics.TotalEpisodes})
	}))
	defer server.Close()
	RemoteServer = server.URL
	if e := catalogSyncCycle(context.Background(), now); e == nil {
		t.Fatal("lost response accepted")
	}
	state, _ := readCatalogSyncState()
	if !state.Uncertain {
		t.Fatal("unknown delivery not persisted")
	}
	path := catalogTestVideo(t, MoviesPath, "Newer.2020.mp4")
	if e := AddFileToCatalog(path); e != nil {
		t.Fatal(e)
	}
	CatalogDB.Close()
	if e := InitCatalog(); e != nil {
		t.Fatal(e)
	}
	if e := catalogSyncCycle(context.Background(), time.Unix(state.RetryAt+1, 0)); e == nil {
		t.Fatal("duplicate ACK incorrectly cleared uncertain predecessor")
	}
	state, _ = readCatalogSyncState()
	if !state.Uncertain || state.Generation == state.Acknowledged || len(state.Pending) == 0 || len(payloads) != 2 || !bytes.Equal(payloads[0], payloads[1]) {
		t.Fatal("uncertain retry advanced newer catalog")
	}
}
func TestCatalogControlRequiresServiceAuthentication(t *testing.T) {
	setupCatalogTest(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/catalog/scan", requireCatalogServiceAuth, CatalogScan)
	req := httptest.NewRequest("POST", "/catalog/scan", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 401 {
		t.Fatal("unauthenticated durable trigger accepted")
	}
	req = httptest.NewRequest("POST", "/catalog/scan", nil)
	req.Header.Set("Authorization", "Bearer test")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatal("authorized reconciliation rejected")
	}
}

func TestCatalogPredeliveryFailuresDoNotFence(t *testing.T) {
	setupCatalogTest(t)
	oldRemote, oldTransport := RemoteServer, http.DefaultTransport
	t.Cleanup(func() { RemoteServer = oldRemote; http.DefaultTransport = oldTransport })
	payload := []byte(`{"statistics":{"total_shows":0,"total_episodes":0}}`)
	for _, name := range []string{"dns", "refused", "tls"} {
		t.Run(name, func(t *testing.T) {
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			RemoteServer = "http://catalog.invalid"
			if name == "tls" {
				server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted TLS request reached handler") }))
				defer server.Close()
				RemoteServer = server.URL
			} else {
				transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
					if name == "dns" {
						return nil, &net.DNSError{Err: "no such host", Name: "catalog.invalid", IsNotFound: true}
					}
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
				}
			}
			http.DefaultTransport = transport
			_, err := transmitCatalogSnapshot(context.Background(), payload)
			var uncertain catalogDeliveryUncertain
			if err == nil || errors.As(err, &uncertain) {
				t.Fatalf("predelivery failure must remain retryable: %v", err)
			}
			state, _ := readCatalogSyncState()
			catalogSyncFailure(state, time.Now(), err, false)
			state, _ = readCatalogSyncState()
			if state.Uncertain {
				t.Fatal("predelivery failure fenced snapshot")
			}
		})
	}
}

func TestCatalogMigratesUncertainColumn(t *testing.T) {
	setupCatalogTest(t)
	if _, err := CatalogDB.Exec("ALTER TABLE catalog_sync_state DROP COLUMN uncertain"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := initCatalogSyncSchema(); err != nil {
			t.Fatal(err)
		}
		state, err := readCatalogSyncState()
		if err != nil || state.Uncertain {
			t.Fatalf("migration state=%+v error=%v", state, err)
		}
	}
}
