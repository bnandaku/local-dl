# Gates: Music ingest and Plex playlists

OWNS: main.go, music*.go, plex*.go, playlist*.go, Dockerfile, unraid/local-dl.xml, docs/**, GATES.md

Scope: New put.io music downloads reach genre/artist/album folders safely; managed Plex playlists reconcile genre and ordered source manifests.

- [x] G1: Audio downloads route to music even with legacy movie/TV labels; tags determine album-preserving paths, missing tags use Unsorted, collisions never overwrite, HTTP/truncation failures never acknowledge completion.
  CHECK: go test -count=1 -run 'TestMusic|TestAudio' ./...
  EXPECT: ok
  EVIDENCE: 2026-09-06 full Go race suite passed; ingest failure/restart, playlist ordering/ownership, pagination and matching covered.
- [x] G2: Managed playlists preserve source order, retain unmatched/ambiguous entries, fill gaps later, protect unrelated playlists, and persist across restart.
  CHECK: go test -count=1 -run 'TestPlaylist|TestPlex|TestMatch' ./...
  EXPECT: ok
  EVIDENCE: 2026-09-06 full Go race suite passed; ingest failure/restart, playlist ordering/ownership, pagination and matching covered.
- [x] G3: Integrated Go tests, race checks, vet, and build pass.
  CHECK: go test -race -count=1 ./...
  EXPECT: ok
  EVIDENCE: 2026-09-06 full Go race suite passed; ingest failure/restart, playlist ordering/ownership, pagination and matching covered.
- [ ] G4: Container configuration includes music mount, ffprobe, persistent manifests, secret-safe Plex settings; rollout and rollback documented and live health verified if deployed.
  EVIDENCE: final Docker image built; scripts/smoke_music_container.py passed an authenticated audio download with exact bytes and genre/album destination. Live rollout pending.
- [x] G5: Spotify/Tidal recreation has a documented working import path and source access gaps are explicit; existing music and personal playlists stay untouched.
  EVIDENCE: 10 Python importer tests passed. Live Tidal classic rock export returned 20 ordered tracks; Starred returned all 718. Spotify import supports token or export; actual Spotify account access and source selection remain user testing inputs.

Checks are run directly under the user's implementation authorization; this ledger records results, not executable approval delegation.
