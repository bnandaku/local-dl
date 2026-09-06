package main

import "strings"

func normalizePlaylistText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func matchPlaylistTracks(source []PlaylistTrack, library []PlexTrack) (matched []PlexTrack, pending []PlaylistTrack, ambiguous int) {
	used := make(map[string]bool)
	for _, want := range source {
		var candidates []PlexTrack
		for _, have := range library {
			if used[have.RatingKey] || normalizePlaylistText(want.Title) != normalizePlaylistText(have.Title) || normalizePlaylistText(want.Artist) != normalizePlaylistText(have.Artist) {
				continue
			}
			if want.Album != "" && have.Album != "" && normalizePlaylistText(want.Album) != normalizePlaylistText(have.Album) {
				continue
			}
			if want.DurationMS > 0 && have.Duration > 0 && abs64(want.DurationMS-have.Duration) > 3000 {
				continue
			}
			candidates = append(candidates, have)
		}
		if len(candidates) != 1 {
			pending = append(pending, want)
			if len(candidates) > 1 {
				ambiguous++
			}
			continue
		}
		used[candidates[0].RatingKey] = true
		matched = append(matched, candidates[0])
	}
	return
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
