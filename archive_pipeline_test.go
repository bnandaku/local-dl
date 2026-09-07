package main

import (
	"path/filepath"
	"testing"
)

func TestArchiveCanonicalRoutingRejectsEpisodesInMovies(t *testing.T) {
	_, e := archivePrimaryItem(archiveSet{MediaType: "movie"}, archiveOutput{Path: "Show.S01 E01.mkv"})
	if e == nil {
		t.Fatal("episodic archive accepted for movie request")
	}
	item, e := archivePrimaryItem(archiveSet{MediaType: "tv"}, archiveOutput{Path: "Show.S01 E01.mkv"})
	if e != nil || item.Type != TVShow {
		t.Fatalf("episode not routed to TV: %v %v", item, e)
	}
	item, e = archivePrimaryItem(archiveSet{MediaType: "movie"}, archiveOutput{Path: "album/song.flac"})
	if e != nil || item.Type != Music {
		t.Fatalf("audio not routed to Music: %v %v", item, e)
	}
	_, e = archivePrimaryItem(archiveSet{MediaType: "music"}, archiveOutput{Path: "film.mp4"})
	if e == nil {
		t.Fatal("video accepted for music request")
	}
}
func TestArchiveJobSurvivesRestartWithoutSourceURLs(t *testing.T) {
	t.Setenv("ARCHIVE_STATE_PATH", filepath.Join(t.TempDir(), "state.json"))
	s := newArchiveState()
	s.Jobs["rar-one"] = &archiveJob{ID: "rar-one", Receipts: map[string]archiveReceipt{}}
	if e := saveArchiveState(s); e != nil {
		t.Fatal(e)
	}
	loaded, e := loadArchiveState()
	if e != nil || loaded.Jobs["rar-one"] == nil {
		t.Fatalf("job lost: %v", e)
	}
}
