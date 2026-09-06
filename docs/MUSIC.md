# Music downloads and Plex playlists

New audio downloads are recognized by extension even when older putio-go-server
versions label them movie or tvshow. Supported formats: MP3, FLAC, M4A, AAC, OGG,
Opus, WAV, AIFF, ALAC and WMA. Archives and companion artwork are not extracted or
retagged by this pipeline. Existing music is never reorganized.

Audio is staged as .part, checked against the source file size or HTTP length,
and probed with ffprobe before publication. Unknown-length downloads without a
source size are retained for retry. Album artist/album tags determine folders;
the first imported track establishes the album's primary genre. Multiple discs
use Disc NN directories. Missing artist/album tags go to Unsorted, with separate
fallback album directories. Original tags and bytes remain unchanged. Exact
content collisions reuse the file; different content gets a digest suffix.

The queue and completion receipts persist before upstream claims/acknowledgements.
Only verified local files are acknowledged. Acknowledgements cause putio-go-server
to delete the source leaf file. Its companion patch removes the independent
12-hour deletion timer for Plex jobs; deploy that patch for full source-retention
safety. Direct-download cleanup remains unchanged. Empty put.io directories are
retained. Music queue files contain signed download URLs and are written 0600.

## Configuration

| Setting | Default / purpose |
| --- | --- |
| MUSIC_PATH | /mnt/music; bind /mnt/user/media/music on Unraid |
| MUSIC_STAGING_PATH | MUSIC_PATH/.incoming; optional separate staging mount |
| MUSIC_STATE_PATH | music-ingest.json beside CATALOG_DB |
| MUSIC_QUEUE_PATH | music-queue.json beside MUSIC_STATE_PATH / CATALOG_DB |
| PLEX_URL | Plex URL reachable from the downloader |
| PLEX_TOKEN | Account token that owns the managed playlists |
| PLEX_MUSIC_SECTION_ID | Music library section; peaches-unraid uses 3 |
| PLEX_MUSIC_PATH | /media/music, Plex's container path for the same music |
| MUSIC_PLAYLIST_DIR | /data/music-playlists; persistent source manifests/ownership |
| MUSIC_API_TOKEN | Bearer secret for music download/import/status/sync HTTP endpoints |

The existing /data mount must remain persistent. Plex configuration is optional;
without its URL/token/section, downloads still work but playlist synchronization
is disabled. Without MUSIC_API_TOKEN, music POST /download is rejected and the
playlist HTTP endpoints are disabled. Queue polling continues to work.

Managed genre playlists are standard Plex playlists refreshed automatically;
they are not Plex smart-filter playlists. The service scans after a new import
and reconciles every five minutes. It combines Plex album genres with original
file genres for newly ingested music. It keeps a separate ownership ID for each
playlist and never adopts a personal playlist merely because its name matches.
A server identity change stops synchronization to protect existing ownership.
An explicit empty source snapshot clears its managed playlist. A nonempty source
with zero matches retains its previous playlist and reports that condition.
Genres absent from a complete, nonempty library are cleared; an entirely empty
library preserves playlists until the library becomes available again.

## Spotify and Tidal

See [playlist import instructions](PLAYLIST-IMPORTS.md). Importing saves a snapshot
of the source playlist, including ordered duplicate and unavailable entries.
Matching uses ISRC where available in imported local metadata, otherwise exact
normalized artist/title with supplied album/duration discriminators. Ambiguous
or missing entries remain pending, with their original positions reported.
Playlists with no available songs exist as pending manifests until the first
song arrives; Plex receives a playlist when there is something to play.

New downloads trigger matching automatically. Re-run the importer to pick up
subsequent edits to a Spotify/Tidal source playlist; source-account refresh is
not a background task. Source playlists are never modified. This service does
not download music from Spotify or Tidal.

## Verification and rollback

Run `go test -race ./...`, `go vet ./...`, `go build -o /tmp/local-dl-check .`,
and `python3 -m unittest discover -s scripts -p 'test_*.py'` before building the
container. Docker requires ffmpeg/ffprobe (provided by Dockerfile).

Before replacing the live downloader, verify /queue is empty. Preserve its old
container and image, all current volumes/ports/environment, and add the music
mount and settings above. Verify /ping, protected playlist status, and ffprobe.
Do not run two downloaders against the same remote queue simultaneously.

Rollback: stop the new downloader, rename it out of the way, restore the retained
old container's original name, and start it. Existing audio and manifests remain
on /mnt/user/media; the old version cannot process new music jobs safely, so pause
music acquisition until the new version is restored. Do not delete playlist
ownership state: it is how the service distinguishes its playlists from yours.

## Deployment record — 2026-09-06

The downloader runs `local-dl:music-20260906` on peaches-unraid, built from
implementation commit `25ffe62` (subsequent documentation-only commit records
verification). Its previous container is retained stopped as
`downloader-local-dl-rollback-music-20260906`. Both Unraid templates were updated
and backed up with `.pre-music-20260906` suffixes. The music API credential is
stored privately at `/mnt/user/media/data/music-api.env` on Unraid.

Live checks confirmed health, authenticated status, rejection without credentials,
ffprobe, the music mount, and creation of the managed Rock playlist. All five
preexisting personal playlist IDs were preserved. The current companion image
`bharatram1/putiobot:67992d1` already contains the complete music routing, file size,
and timer-removal patch; its deployed source passed the exact reverse-patch check
and has no differences from that commit in the three affected files.

No Spotify/Tidal source manifests have been imported into production yet. Choose
source playlists for the first user test; Tidal bridge access was verified, and
Spotify needs an export or authorized access token.
