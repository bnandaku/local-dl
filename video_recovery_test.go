package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestCodecRejectionWaitsForMatchingBotPolicyThenRequeues(t *testing.T) {
	t.Setenv("BOT_SERVICE_TOKEN", "test")
	t.Setenv("LINK_FEEDBACK_PATH", filepath.Join(t.TempDir(), "feedback.json"))
	matches := false
	var mutations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			mutations = append(mutations, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v1/capabilities":
			excluded := []string{}
			if matches {
				excluded = []string{"dolby_vision"}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"service": "putio-go-server", "recovery_contract": 2, "attempt_limit": 5, "permanent_blacklist": true, "file_publication_required": true, "features": []string{"legacy_recovery", "failed_transfers", "scoped_discard", "durable_events", "request_reconciliation", "json_api_errors", "video_codec_policy"}, "video_excluded_codecs": excluded})
		case "/api/v1/link-files/17":
			w.Write([]byte(`{"id":9,"group_id":"movie","status":"queued"}`))
		case "/api/v1/links/9/report":
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			if body["status"] != "bad" || body["reason"] != "wrong_content" {
				t.Errorf("codec rejection mislabeled: %v", body)
			}
			w.Write([]byte(`{"id":9,"group_id":"movie","status":"bad"}`))
		case "/api/v1/links/9/discard":
			w.Write([]byte(`{"event_id":"local-dl:discard:17","attempt_id":9,"file_id":17,"status":"done"}`))
		case "/api/v1/recovery/requests/movie":
			w.Write([]byte(`{"request":{"id":"movie","attempts":1,"attempt_limit":5,"state":"failed","retryable":true}}`))
		case "/api/v1/recovery/requests/movie/retry":
			w.Write([]byte(`{"id":"movie","attempts":2,"attempt_limit":5,"state":"queued"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := RemoteServer
	RemoteServer = srv.URL
	defer func() { RemoteServer = old }()
	if !reportBadMedia(&Item{FileId: 17}, videoCodecError{"dolby_vision"}) {
		t.Fatal("codec failure not saved")
	}
	feedback, e := readLinkFeedback()
	if e != nil || feedback["17"].Codec != "dolby_vision" {
		t.Fatalf("observed codec not durable: %v %v", feedback, e)
	}
	retryLinkFeedback()
	if len(mutations) != 0 {
		t.Fatalf("deleted before search policy aligned: %v", mutations)
	}
	matches = true
	retryLinkFeedback()
	want := []string{"/api/v1/links/9/report", "/api/v1/links/9/discard", "/api/v1/recovery/requests/movie/retry"}
	if len(mutations) != len(want) {
		t.Fatalf("%v", mutations)
	}
	for i := range want {
		if mutations[i] != want[i] {
			t.Fatalf("%v", mutations)
		}
	}
	feedback, e = readLinkFeedback()
	if e != nil || len(feedback) != 0 {
		t.Fatalf("successful recovery not acknowledged: %v %v", feedback, e)
	}
}
