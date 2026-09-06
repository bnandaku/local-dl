package main

import "strings"

func normalizePlaylistText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func matchPlaylistTracks(source []PlaylistTrack, library []PlexTrack) (matched []PlexTrack, pending []PlaylistTrack, ambiguous int) {
	matched, details, ambiguous := matchPlaylistDetails(source, library)
	for _, detail := range details {
		pending = append(pending, detail.Track)
	}
	return matched, pending, ambiguous
}

func matchPlaylistDetails(source []PlaylistTrack, library []PlexTrack) (matched []PlexTrack, pending []PlaylistPending, ambiguous int) {
	byName := map[string][]PlexTrack{}
	byISRC := map[string][]PlexTrack{}
	for _, track := range library {
		key := normalizePlaylistText(track.Title) + "\x00" + normalizePlaylistText(track.Artist)
		byName[key] = append(byName[key], track)
		if track.ISRC != "" {
			key = normalizePlaylistText(track.ISRC)
			byISRC[key] = append(byISRC[key], track)
		}
	}
	for index, want := range source {
		var candidates []PlexTrack
		if !want.Unavailable && strings.TrimSpace(want.Title) != "" && strings.TrimSpace(want.Artist) != "" {
			pool := byISRC[normalizePlaylistText(want.ISRC)]
			if len(pool) == 0 {
				pool = byName[normalizePlaylistText(want.Title)+"\x00"+normalizePlaylistText(want.Artist)]
			}
			for _, have := range pool {
				if want.ISRC != "" && have.ISRC != "" && normalizePlaylistText(want.ISRC) != normalizePlaylistText(have.ISRC) {
					continue
				}
				if want.Album != "" && normalizePlaylistText(want.Album) != normalizePlaylistText(have.Album) {
					continue
				}
				if want.DurationMS > 0 && (have.Duration <= 0 || abs64(want.DurationMS-have.Duration) > 3000) {
					continue
				}
				candidates = append(candidates, have)
			}
		}
		if len(candidates) != 1 {
			pending = append(pending, PlaylistPending{Index: index, Track: want})
			if len(candidates) > 1 {
				ambiguous++
			}
			continue
		}
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
