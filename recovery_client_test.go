package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryRejectsLegacyFallback(t *testing.T) {
	old := RemoteServer
	defer func() { RemoteServer = old }()
	t.Setenv("BOT_SERVICE_TOKEN", "test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("legacy fallback"))
	}))
	defer srv.Close()
	RemoteServer = srv.URL
	if err := requireRecoveryContract(); err == nil {
		t.Fatal("legacy fallback accepted")
	}
}
func TestPublishedRecoveryReportsBeforeCleanup(t *testing.T) {
	old := RemoteServer
	defer func() { RemoteServer = old }()
	t.Setenv("BOT_SERVICE_TOKEN", "test")
	path := filepath.Join(t.TempDir(), "film.mkv")
	os.WriteFile(path, []byte("verified test data"), 0600)
	digest, _ := musicFileDigest(path)
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/capabilities":
			w.Write([]byte(`{"service":"putio-go-server","recovery_contract":2,"attempt_limit":5,"permanent_blacklist":true,"file_publication_required":true,"features":["legacy_recovery","failed_transfers","scoped_discard","durable_events","request_reconciliation","json_api_errors"]}`))
		case "/api/v1/link-files/7":
			w.Write([]byte(`{"id":3,"group_id":"g","status":"queued"}`))
		case "/api/v1/links/3/report":
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			if body["published"] != true || body["status"] != "validated" {
				t.Error(body)
			}
			w.Write([]byte(`{"id":3,"group_id":"g","status":"validated"}`))
		case "/api/v1/links/3/cleanup":
			w.Write([]byte(`{"event_id":"local-dl:cleanup:7","attempt_id":3,"file_id":7,"status":"done"}`))
		case "/api/v1/links/3/files":
			w.Write([]byte(`{"items":[{"file_id":7,"filename":"film.mkv","required":true,"report":"validated","published":true,"cleanup":"purged"}]}`))
		case "/api/v1/links/3/confirm":
			w.Write([]byte(`{"confirmed_id":3,"removed_attempts":1}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	RemoteServer = srv.URL
	handled, e := recoverPublishedFile(musicImportRecord{FileID: 7, Name: "film.mkv", Path: path, SHA256: digest, Type: Movies})
	if !handled || e != nil {
		t.Fatal(handled, e)
	}
	order := strings.Join(calls, ",")
	if strings.Index(order, "/report") > strings.Index(order, "/cleanup") || strings.Index(order, "/cleanup") > strings.Index(order, "/confirm") {
		t.Fatal(order)
	}
}

func TestRecoveryExhaustedNeverSubmitsAgain(t *testing.T) {
	old := RemoteServer
	defer func() { RemoteServer = old }()
	for _, state := range []string{"exhausted", "completed", "queued", "validated"} {
		t.Run(state, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Fatal("terminal/current request submitted again")
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{"request": map[string]interface{}{"id": "group", "state": state, "attempts": 5, "attempt_limit": 5, "retryable": false}})
			}))
			defer srv.Close()
			RemoteServer = srv.URL
			if e := retryRecoveryRequest("group"); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestRecoveryUncertainNeverBlindlyResubmits(t *testing.T) {
	old := RemoteServer
	defer func() { RemoteServer = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Fatal("uncertain submission repeated")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"request":{"id":"g","state":"uncertain","attempts":1,"attempt_limit":5}}`))
	}))
	defer srv.Close()
	RemoteServer = srv.URL
	if e := retryRecoveryRequest("g"); e == nil {
		t.Fatal("uncertain state not held")
	}
}
func TestRecoveryRetryUsesServerAttemptCount(t *testing.T) {
	old := RemoteServer
	defer func() { RemoteServer = old }()
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"request":{"id":"g","state":"failed","attempts":4,"attempt_limit":5,"retryable":true}}`))
			return
		}
		posts++
		w.Write([]byte(`{"id":"g","state":"queued","attempts":5,"attempt_limit":5,"retryable":false}`))
	}))
	defer srv.Close()
	RemoteServer = srv.URL
	if e := retryRecoveryRequest("g"); e != nil || posts != 1 {
		t.Fatal(e, posts)
	}
}

func TestMalformedNotFoundCannotAuthorizeLegacyCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		untracked               bool
	}{
		{"proxy", "text/html", "<html>not found</html>", false},
		{"wrong_code", "application/json", `{"error":"missing route","code":"route_not_found","retryable":false}`, false},
		{"incomplete", "application/json", `{"code":"not_found"}`, false},
		{"untracked", "application/json", `{"error":"Record not found","code":"not_found","retryable":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BOT_SERVICE_TOKEN", "test")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/capabilities" {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"service":"putio-go-server","recovery_contract":2,"attempt_limit":5,"permanent_blacklist":true,"file_publication_required":true,"features":["legacy_recovery","failed_transfers","scoped_discard","durable_events","request_reconciliation","json_api_errors"]}`))
					return
				}
				if r.Method != "GET" {
					t.Fatal("unexpected mutation")
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(404)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			old := RemoteServer
			RemoteServer = srv.URL
			defer func() { RemoteServer = old }()
			handled, err := recoverPublishedFile(musicImportRecord{FileID: 7})
			if tc.untracked {
				if handled || err != nil {
					t.Fatalf("valid not_found: %v %v", handled, err)
				}
			} else if !handled || err == nil {
				t.Fatalf("unsafe fallback authorized: %v %v", handled, err)
			}
		})
	}
}
