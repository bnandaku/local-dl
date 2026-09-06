# Music ingest and playlist delivery

Approved scope: implement the proposed new-download music pipeline and genre playlists. Explain and enable ordered Spotify/Tidal playlist recreation after acquisition. Existing music is not migrated.

1. Add tests for legacy audio routing, ffprobe metadata, genre/artist/album paths, fallback, safe file placement, truncated/HTTP failures, and repeated downloads. Implement music.go and music_download.go; call from StartDownload before legacy video routing. Use ffprobe (ffmpeg package) and Go standard library. Store incomplete downloads outside Plex library, close/sync and validate byte count/audio before placement. No overwrites; exact-content retries reuse destination.
2. Add isolated Plex subsystem in plex_*.go / playlist_*.go with bounded API calls, durable source manifests and app-owned IDs, conservative matching, periodic reconciliation and download triggers. Use protected /music API routes. Preserve unrelated playlists and old items on errors. Integrate initialization in main.go.
3. Inspect source bridge and provide ordered manifest export/import for Spotify/Tidal, preserving missing entries. Direct account imports depend on available source account authorization; never infer authorization from elapsed time.
4. Add Unraid music volume and runtime ffprobe, env documentation and deployment recipe. Verify local tests/race/vet/build and independent review. Inspect live settings without exposing credentials; prepare reversible rollout and verify service health.

Go quality profile: gofmt, go vet ./..., go test -race ./..., go build. Existing repository has no enforced complexity tooling; use focused modules/functions and independent review of metadata paths, file publication, API ownership and reconciliation failure boundaries. Exclude existing binaries/vendor/generated files. Preserve companion-repo uncommitted work. No live test downloads through endpoints that emit Discord messages without user authorization.
