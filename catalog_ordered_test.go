package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestCatalogOrderedReplayAfterLostResponse(t *testing.T) {
	setupCatalogTest(t)
	t.Setenv("CATALOG_SOURCE_ID", "peaches-unraid")
	t.Setenv("CATALOG_API_KEY", "test")
	old := RemoteServer
	defer func() { RemoteServer = old }()
	var sequence int64 = 40
	var hash string
	var sent [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing bearer")
		}
		switch r.URL.Path {
		case "/api/v1/capabilities":
			io.WriteString(w, `{"features":["ordered_catalog_v1"]}`)
		case "/api/v1/catalog/watermark":
			json.NewEncoder(w).Encode(map[string]any{"enrolled": true, "source_id": "peaches-unraid", "sequence": sequence, "sha256": fmt.Sprintf("%064s", hash)})
		case "/catalogUpdate":
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			defer gz.Close()
			b, _ := io.ReadAll(gz)
			sent = append(sent, b)
			n, _ := strconv.ParseInt(r.Header.Get("X-Catalog-Sequence"), 10, 64)
			sum := fmt.Sprintf("%x", sha256.Sum256(b))
			if n != 41 || r.Header.Get("X-Catalog-Source") != "peaches-unraid" || r.Header.Get("X-Catalog-SHA256") != sum {
				t.Error("missing ordered identity")
			}
			sequence, hash = n, sum
			if len(sent) == 1 {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"message": "Catalog updated successfully", "shows": 0, "episodes": 0, "source_id": "peaches-unraid", "sequence": n, "sha256": sum, "replayed": true})
		default:
			t.Error("unexpected endpoint")
		}
	}))
	defer server.Close()
	RemoteServer = server.URL
	now := time.Now()
	if err := catalogSyncCycle(context.Background(), now); err == nil {
		t.Fatal("lost response must retry")
	}
	CatalogDB.Close()
	if err := InitCatalog(); err != nil {
		t.Fatal(err)
	}
	if err := catalogSyncCycle(context.Background(), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s, err := readCatalogSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if s.Uncertain || len(s.Pending) != 0 || len(sent) != 2 || string(sent[0]) != string(sent[1]) {
		t.Fatal("ordered replay did not resolve durable outbox")
	}
}

func orderedTestServer(t *testing.T, post func(http.ResponseWriter, *http.Request), watermark func() map[string]any) {
	t.Helper()
	setupCatalogTest(t)
	t.Setenv("CATALOG_SOURCE_ID", "peaches-unraid")
	t.Setenv("CATALOG_API_KEY", "test")
	old := RemoteServer
	t.Cleanup(func() { RemoteServer = old })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/capabilities":
			io.WriteString(w, `{"features":["ordered_catalog_v1"]}`)
		case "/api/v1/catalog/watermark":
			if r.URL.Query().Get("source_id") != "peaches-unraid" {
				t.Error("wrong source query")
			}
			if watermark != nil {
				json.NewEncoder(w).Encode(watermark())
			} else {
				io.WriteString(w, `{"enrolled":false,"source_id":"","sequence":0,"sha256":""}`)
			}
		case "/catalogUpdate":
			post(w, r)
		default:
			t.Error("unknown request")
		}
	}))
	RemoteServer = server.URL
	t.Cleanup(server.Close)
}
func orderedAck(w http.ResponseWriter, r *http.Request) map[string]any {
	n, _ := strconv.ParseInt(r.Header.Get("X-Catalog-Sequence"), 10, 64)
	return map[string]any{"message": "Catalog updated successfully", "shows": 0, "episodes": 0, "source_id": r.Header.Get("X-Catalog-Source"), "sequence": n, "sha256": r.Header.Get("X-Catalog-SHA256"), "replayed": false}
}
func TestCatalogOrderedRejectsForgedAcknowledgement(t *testing.T) {
	for _, field := range []string{"source_id", "sequence", "sha256", "replayed"} {
		t.Run(field, func(t *testing.T) {
			orderedTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				ack := orderedAck(w, r)
				delete(ack, field)
				json.NewEncoder(w).Encode(ack)
			}, nil)
			if err := catalogSyncCycle(context.Background(), time.Now()); err == nil {
				t.Fatal("forged ack accepted")
			}
			s, _ := readCatalogSyncState()
			if len(s.Pending) == 0 || s.CompletedRequest != 0 || s.PendingSequence != 1 {
				t.Fatal("snapshot lost")
			}
		})
	}
}
func TestCatalogOrderedConflictsHoldWithoutBlindRetries(t *testing.T) {
	for _, status := range []int{400, 409, 413} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			calls := 0
			orderedTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				io.WriteString(w, `{"error":"catalog_sequence_conflict"}`)
			}, nil)
			now := time.Now()
			if err := catalogSyncCycle(context.Background(), now); err == nil {
				t.Fatal("rejection accepted")
			}
			s, _ := readCatalogSyncState()
			if !s.Blocked || len(s.Pending) == 0 {
				t.Fatal("rejected snapshot not held")
			}
			if err := catalogSyncCycle(context.Background(), now.Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("permanent failure retried blindly")
			}
		})
	}
}
func TestCatalogOrderedStaleReconcilesAboveWatermark(t *testing.T) {
	calls := 0
	orderedTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		n, _ := strconv.Atoi(r.Header.Get("X-Catalog-Sequence"))
		if calls == 1 {
			if n != 1 {
				t.Error("initial sequence")
			}
			w.WriteHeader(409)
			io.WriteString(w, `{"error":"stale_catalog_sequence"}`)
			return
		}
		if n != 10 {
			t.Error("did not bootstrap above watermark")
		}
		json.NewEncoder(w).Encode(orderedAck(w, r))
	}, func() map[string]any {
		if calls == 0 {
			return map[string]any{"enrolled": false, "source_id": "", "sequence": 0, "sha256": ""}
		}
		return map[string]any{"enrolled": true, "source_id": "peaches-unraid", "sequence": 9, "sha256": fmt.Sprintf("%064d", 0)}
	})
	now := time.Now()
	if err := catalogSyncCycle(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	s, _ := readCatalogSyncState()
	if s.CompletedRequest != 0 || s.Sequence != 9 || len(s.Pending) > 0 {
		t.Fatal("stale snapshot falsely acknowledged")
	}
	if err := catalogSyncCycle(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s, _ = readCatalogSyncState()
	if s.Sequence != 10 || s.Requested != s.CompletedRequest {
		t.Fatal("reconciliation incomplete")
	}
}
func TestCatalogOrderedPersistsIdentityAndAdvancesAfterReplay(t *testing.T) {
	calls := 0
	orderedTestServer(t, func(w http.ResponseWriter, r *http.Request) { calls++; json.NewEncoder(w).Encode(orderedAck(w, r)) }, nil)
	now := time.Now()
	if err := catalogSyncCycle(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_SOURCE_ID", "") // Persisted identity must prohibit downgrade.
	if err := RequestCatalogReconciliation(); err != nil {
		t.Fatal(err)
	}
	if err := catalogSyncCycle(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s, _ := readCatalogSyncState()
	if s.Sequence != 2 || s.Source != "peaches-unraid" || calls != 2 {
		t.Fatal("identity/sequence lost")
	}
	t.Setenv("CATALOG_SOURCE_ID", "competing-source")
	RequestCatalogReconciliation()
	if err := catalogSyncCycle(context.Background(), now.Add(2*time.Minute)); err == nil {
		t.Fatal("changed source allowed")
	}
	if calls != 2 {
		t.Fatal("competing producer transmitted")
	}
}
func TestCatalogOrderedPromotesLegacyUncertainSnapshot(t *testing.T) {
	orderedTestServer(t, func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(orderedAck(w, r)) }, nil)
	if _, err := CatalogDB.Exec(`UPDATE catalog_sync_state SET uncertain=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := catalogSyncCycle(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	s, _ := readCatalogSyncState()
	if s.Uncertain || s.Sequence != 1 || len(s.Pending) > 0 {
		t.Fatal("legacy uncertainty not resolved by ordered acknowledgement")
	}
}

func TestCatalogOrderedGatesBeforeTransmission(t *testing.T) {
	for _, mode := range []string{"unsupported", "auth", "bad-watermark"} {
		t.Run(mode, func(t *testing.T) {
			setupCatalogTest(t)
			t.Setenv("CATALOG_SOURCE_ID", "peaches-unraid")
			t.Setenv("CATALOG_API_KEY", "test")
			old := RemoteServer
			defer func() { RemoteServer = old }()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "POST" {
					t.Error("unverified ordered server received snapshot")
				}
				if mode == "auth" {
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == "/api/v1/capabilities" {
					if mode == "unsupported" {
						io.WriteString(w, `{"features":[]}`)
					} else {
						io.WriteString(w, `{"features":["ordered_catalog_v1"]}`)
					}
				} else {
					io.WriteString(w, `{"enrolled":true,"source_id":"other","sequence":10,"sha256":"bad"}`)
				}
			}))
			defer server.Close()
			RemoteServer = server.URL
			now := time.Now()
			if err := catalogSyncCycle(context.Background(), now); err == nil {
				t.Fatal("invalid preflight accepted")
			}
			s, _ := readCatalogSyncState()
			if len(s.Pending) == 0 || s.Sequence != 0 {
				t.Fatal("snapshot lost or sequence allocated before verification")
			}
			if mode == "auth" && s.RetryAt != now.Add(30*time.Minute).Unix() {
				t.Fatal("authentication not held for 30 minutes")
			}
		})
	}
}
func TestCatalogAutomaticResyncCannotClearOperatorHold(t *testing.T) {
	setupCatalogTest(t)
	CatalogDB.Exec(`UPDATE catalog_sync_state SET blocked=1 WHERE id=1`)
	RequestCatalogReconciliation()
	s, _ := readCatalogSyncState()
	if !s.Blocked {
		t.Fatal("automatic resync cleared hold")
	}
	if err := RetryCatalogReconciliation(); err != nil {
		t.Fatal(err)
	}
	s, _ = readCatalogSyncState()
	if s.Blocked {
		t.Fatal("manual repair could not resume")
	}
}
func TestCatalogOrderedSchemaUpgradePreservesLegacyOutbox(t *testing.T) {
	setupCatalogTest(t)
	CatalogDB.Exec(`UPDATE catalog_sync_state SET pending='saved',generation=7,pending_generation=6,uncertain=1 WHERE id=1`)
	for _, column := range []string{"source", "sequence", "pending_sequence", "pending_hash", "blocked"} {
		if _, err := CatalogDB.Exec("ALTER TABLE catalog_sync_state DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	if err := initCatalogSyncSchema(); err != nil {
		t.Fatal(err)
	}
	s, err := readCatalogSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Pending) != "saved" || s.Generation != 7 || s.PendingGeneration != 6 || !s.Uncertain || s.Sequence != 0 {
		t.Fatal("upgrade changed legacy outbox")
	}
}

func TestCatalogOrderedRequiresDedicatedCredential(t *testing.T) {
	orderedTestServer(t, func(http.ResponseWriter, *http.Request) { t.Error("shared token used for ordered POST") }, nil)
	t.Setenv("CATALOG_API_KEY", "") // Shared legacy service token is still present.
	now := time.Now()
	if err := catalogSyncCycle(context.Background(), now); err == nil {
		t.Fatal("shared credential enrolled")
	}
	s, _ := readCatalogSyncState()
	if s.Sequence != 0 || s.RetryAt != now.Add(30*time.Minute).Unix() {
		t.Fatal("missing dedicated credential not held as authentication failure")
	}
}
func TestCatalogOrderedWatermarkProducerConflictIsHeld(t *testing.T) {
	setupCatalogTest(t)
	t.Setenv("CATALOG_SOURCE_ID", "peaches-unraid")
	t.Setenv("CATALOG_API_KEY", "test")
	old := RemoteServer
	defer func() { RemoteServer = old }()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/capabilities" {
			io.WriteString(w, `{"features":["ordered_catalog_v1"]}`)
			return
		}
		if r.Method == "POST" {
			t.Error("conflicting producer posted")
		}
		w.WriteHeader(403)
		io.WriteString(w, `{"error":"catalog_producer_conflict"}`)
	}))
	defer server.Close()
	RemoteServer = server.URL
	now := time.Now()
	if err := catalogSyncCycle(context.Background(), now); err == nil {
		t.Fatal("conflict accepted")
	}
	s, _ := readCatalogSyncState()
	if !s.Blocked {
		t.Fatal("conflict not held")
	}
	catalogSyncCycle(context.Background(), now.Add(time.Hour))
	if calls != 2 {
		t.Fatal("conflict retried blindly")
	}
}
