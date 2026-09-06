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
- [x] G4: Container configuration includes music mount, ffprobe, persistent manifests, secret-safe Plex settings; rollout and rollback documented and live health verified if deployed.
  EVIDENCE: final Docker image built; scripts/smoke_music_container.py passed an authenticated audio download with exact bytes and genre/album destination. Live rollout passed: /ping, authenticated playlist status, unauthenticated 401, correct music mount and ffprobe. Plex created owned Rock playlist 11336; all five original personal playlist IDs remain. Old downloader retained stopped for rollback.
- [x] G5: Spotify/Tidal recreation has a documented working import path and source access gaps are explicit; existing music and personal playlists stay untouched.
  EVIDENCE: 10 Python importer tests passed. Live Tidal classic rock export returned 20 ordered tracks; Starred returned all 718. Spotify import supports token or export; actual Spotify account access and source selection remain user testing inputs.

Checks are run directly under the user's implementation authorization; this ledger records results, not executable approval delegation.

## Media hygiene follow-up — 2026-09-06

- [x] Audio/video routing overrides incorrect labels; spaced/dotted episode markers and parent context force TV; unknown extensions rejected before download.
- [x] Supporting media waits for a verified primary receipt and follows its actual destination; invalid HTTP/error payloads cannot publish or acknowledge.
- [x] Main and companion Go race suites and vet passed. Python suite: 22 tests passed. Built container audio smoke passed; final live garbage request returned 400 without enqueueing.
- [x] All 4,556 existing files audited, including primary stream probes and artwork/error-payload checks. Quarantined 157 confirmed Put.io error responses, zero scan errors/timeouts. Repaired the Lilo & Stitch episode from Movies to TV with recovery copy.
- [x] Follow-up inventory: 4,399 files, zero unknown extensions, zero misplaced media. All three Plex library refresh requests accepted. Personal playlists preserved.
- [x] Downloader and companion committed, pushed, deployed and healthy; prior containers and configuration backups retained. Full report lives privately under reports/ and on Unraid /mnt/user/media/data/media-hygiene-report-20260906.json.
