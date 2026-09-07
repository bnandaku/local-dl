package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestArchiveDownloadRejectsPrivateOriginAndRedirect(t *testing.T) {
	var hits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("RAR")) }))
	defer private.Close()
	t.Setenv("ARCHIVE_DOWNLOAD_ORIGINS", "")
	v := archiveVolume{FileID: 1, URL: private.URL, Size: 3}
	if e := downloadArchiveVolume(context.Background(), v, filepath.Join(t.TempDir(), "x.rar")); e == nil {
		t.Fatal("private download accepted")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, private.URL, 302) }))
	defer origin.Close()
	t.Setenv("ARCHIVE_DOWNLOAD_ORIGINS", origin.URL)
	v.URL = origin.URL
	if e := downloadArchiveVolume(context.Background(), v, filepath.Join(t.TempDir(), "x.rar")); e == nil {
		t.Fatal("private redirect accepted")
	}
	if hits.Load() != 0 {
		t.Fatal("unapproved private endpoint reached")
	}
}

func TestArchiveMissingFileJSONDoesNotBecomeNetworkHold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"error_type":"FileNotFound"}`)) }))
	defer server.Close()
	t.Setenv("ARCHIVE_DOWNLOAD_ORIGINS", server.URL)
	err := downloadArchiveVolume(context.Background(), archiveVolume{URL: server.URL, Size: 1000}, filepath.Join(t.TempDir(), "part01.rar"))
	var missing failedDownloadError
	if !errors.As(err, &missing) || missing.reason != "file_not_found" {
		t.Fatalf("missing source misclassified: %v", err)
	}
}
