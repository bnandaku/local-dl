package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFeedbackPersistsAndDoesNotBlacklistTransientFailures(t *testing.T) {
	t.Setenv("BOT_SERVICE_TOKEN", "test-token")
	p := filepath.Join(t.TempDir(), "feedback.json")
	t.Setenv("LINK_FEEDBACK_PATH", p)
	i := &Item{FileId: 42}
	if reportBadMedia(i, errors.New("connection timeout")) {
		t.Fatal("transient failure classified as bad")
	}
	if !reportBadMedia(i, badMediaError{"no_media"}) {
		t.Fatal("content failure not saved")
	}
	if e := queueLinkFeedback(i, "validated", ""); e != nil {
		t.Fatal(e)
	}
	m, e := readLinkFeedback()
	if e != nil || m["42"].Status != "bad" {
		t.Fatalf("lost bad report: %v %v", m, e)
	}
	info, e := os.Stat(p)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("feedback must be private")
	}
	if invalidProbeInput("ffprobe not found") || invalidProbeInput("permission denied") {
		t.Fatal("operational failure blacklisted")
	}
	if !invalidProbeInput("Invalid data found when processing input") {
		t.Fatal("corrupt media not detected")
	}
}
