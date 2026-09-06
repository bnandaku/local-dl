package main

import "testing"

func TestMatchTracksPreservesSourceOrderAndReportsPending(t *testing.T) {
	source := []PlaylistTrack{
		{Title: "Second", Artist: "Band", Album: "A"},
		{Title: "Missing", Artist: "Band", Album: "A"},
		{Title: "First", Artist: "Band", Album: "A"},
	}
	library := []PlexTrack{
		{RatingKey: "1", Title: "First", Artist: "Band", Album: "A"},
		{RatingKey: "2", Title: "Second", Artist: "Band", Album: "A"},
	}
	got, pending, ambiguous := matchPlaylistTracks(source, library)
	if len(got) != 2 || got[0].RatingKey != "2" || got[1].RatingKey != "1" {
		t.Fatalf("unexpected order: %#v", got)
	}
	if len(pending) != 1 || pending[0].Title != "Missing" || ambiguous != 0 {
		t.Fatalf("unexpected pending=%#v ambiguous=%d", pending, ambiguous)
	}
}

func TestMatchTracksLeavesAmbiguousMatchesPending(t *testing.T) {
	source := []PlaylistTrack{{Title: "Song", Artist: "Band"}}
	library := []PlexTrack{
		{RatingKey: "1", Title: "Song", Artist: "Band"},
		{RatingKey: "2", Title: "Song", Artist: "Band"},
	}
	got, pending, ambiguous := matchPlaylistTracks(source, library)
	if len(got) != 0 || len(pending) != 1 || ambiguous != 1 {
		t.Fatalf("got=%#v pending=%#v ambiguous=%d", got, pending, ambiguous)
	}
}

func TestMatchTracksPreservesDuplicateSourceOccurrences(t *testing.T) {
	source := []PlaylistTrack{{Title: "Song", Artist: "Band"}, {Title: "Song", Artist: "Band"}}
	library := []PlexTrack{{RatingKey: "1", Title: "Song", Artist: "Band"}}
	got, pending, ambiguous := matchPlaylistTracks(source, library)
	if len(got) != 2 || len(pending) != 0 || ambiguous != 0 {
		t.Fatalf("got=%#v pending=%#v ambiguous=%d", got, pending, ambiguous)
	}
}
