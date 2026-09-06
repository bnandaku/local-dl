package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryMonitorPreservesCorruptState(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("RECOVERY_STATE_PATH", p)
	original := []byte(`{"reported_transfers":`)
	if e := os.WriteFile(p, original, 0600); e != nil {
		t.Fatal(e)
	}
	recoveryMonitorCycle()
	got, e := os.ReadFile(p)
	if e != nil || string(got) != string(original) {
		t.Fatalf("corrupt state overwritten: %q %v", got, e)
	}
}

func TestOperationalHoldDoesNotBlockOtherRecoveryGroups(t *testing.T) {
	retried := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/links":
			w.Write([]byte(`{"items":[{"id":1,"group_id":"hold","status":"failed"},{"id":2,"group_id":"ready","status":"failed"}],"next_after":0}`))
		case "/api/v1/links/1/files", "/api/v1/links/2/files":
			w.Write([]byte(`{"items":[]}`))
		case "/api/v1/recovery/requests/hold":
			w.Write([]byte(`{"request":{"id":"hold","state":"failed","attempts":1,"attempt_limit":5,"retryable":false}}`))
		case "/api/v1/recovery/requests/ready":
			w.Write([]byte(`{"request":{"id":"ready","state":"failed","attempts":1,"attempt_limit":5,"retryable":true}}`))
		case "/api/v1/recovery/requests/ready/retry":
			retried = true
			w.Write([]byte(`{"id":"ready","state":"queued","attempts":2,"attempt_limit":5}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := RemoteServer
	RemoteServer = srv.URL
	defer func() { RemoteServer = old }()
	if e := reconcileRecoveryGroups(); e == nil {
		t.Fatal("operational hold must remain visible")
	}
	if !retried {
		t.Fatal("unrelated retry blocked by held request")
	}
}

func TestUnknownFailedTransferIsReportedAndSurfacedOnce(t *testing.T) {
	t.Setenv("RECOVERY_STATE_PATH", filepath.Join(t.TempDir(), "state.json"))
	reports := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/transfers/failed":
			w.Write([]byte(`{"items":[{"transfer_id":77,"title":"Unknown Movie","status":"ERROR","tracked":false}],"next_after":0}`))
		case "/api/v1/transfers/77/report":
			reports++
			w.Write([]byte(`{"transfer":{"transfer_id":77,"title":"Unknown Movie","status":"ERROR","tracked":false},"next_action":"create_legacy_recovery","known_attempts":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := RemoteServer
	RemoteServer = srv.URL
	defer func() { RemoteServer = old }()
	state, err := loadRecoveryMonitor()
	if err != nil {
		t.Fatal(err)
	}
	if e := pollFailedTransfers(&state); e != nil {
		t.Fatal(e)
	}
	saved, e := loadRecoveryMonitor()
	if e != nil || saved.Unknown[77] != "Unknown Movie" {
		t.Fatalf("missing durable review task: %v %v", saved, e)
	}
	if e := pollFailedTransfers(&saved); e != nil {
		t.Fatal(e)
	}
	if reports != 1 {
		t.Fatalf("duplicate reports: %d", reports)
	}
}

func TestFailedTransferIsReportedDiscardedThenRetried(t *testing.T) {
	t.Setenv("RECOVERY_STATE_PATH", filepath.Join(t.TempDir(), "state.json"))
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			calls = append(calls, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v1/transfers/failed":
			w.Write([]byte(`{"items":[{"transfer_id":88,"title":"Movie","status":"ERROR","tracked":true,"reason":"transfer_failed","attempt":{"id":4,"group_id":"request","transfer_id":88,"status":"queued"}}],"next_after":0}`))
		case "/api/v1/transfers/88/report":
			w.Write([]byte(`{"transfer":{"transfer_id":88,"tracked":true},"next_action":"retry_request"}`))
		case "/api/v1/links/4/discard":
			w.Write([]byte(`{"event_id":"local-dl:discard-transfer:88","attempt_id":4,"transfer_id":88,"status":"done"}`))
		case "/api/v1/recovery/requests/request":
			w.Write([]byte(`{"request":{"id":"request","attempt_limit":5,"attempts":1,"state":"failed","retryable":true}}`))
		case "/api/v1/recovery/requests/request/retry":
			w.Write([]byte(`{"id":"request","attempt_limit":5,"attempts":2,"state":"queued"}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := RemoteServer
	RemoteServer = srv.URL
	defer func() { RemoteServer = old }()
	s, e := loadRecoveryMonitor()
	if e != nil {
		t.Fatal(e)
	}
	if e := pollFailedTransfers(&s); e != nil {
		t.Fatal(e)
	}
	want := []string{"/api/v1/transfers/88/report", "/api/v1/links/4/discard", "/api/v1/recovery/requests/request/retry"}
	if len(calls) != len(want) {
		t.Fatalf("%v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("%v", calls)
		}
	}
}
