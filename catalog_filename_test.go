package main

import "testing"

func TestParseTVShowInfoDoesNotSlicePastExtension(t *testing.T) {
	info := parseTVShowInfo("Example Show - 01.mkv")
	if !info.HasSeasonInfo || info.ShowName == "" || info.Episode != "01" {
		t.Fatalf("unexpected TV parsing result: %#v", info)
	}
}

func TestCatalogFilenameParsingPreservesMovieAndMusicRouting(t *testing.T) {
	movie := parseMovieInfo("Example.Movie.2024.1080p.mkv")
	if movie.Title != "Example Movie" || movie.Year != "2024" || movie.Quality != "1080p" {
		t.Fatalf("unexpected movie parsing result: %#v", movie)
	}
	music := &Item{Name: "Example Song.flac", Type: Movies}
	if err := routeMedia(music); err != nil {
		t.Fatal(err)
	}
	if music.Type != Music {
		t.Fatalf("music route changed: %q", music.Type)
	}
}
