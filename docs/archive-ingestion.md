# RAR ingestion

Local-dl requires recovery contract 2 and `archive_sets_v1`. The bot discovers completed transfer archive sets; local-dl polls them every minute, saves durable jobs, and fetches fresh volume URLs when processing. Extracted files never receive invented Put.io file IDs.

## Processing

1. Download all declared `.partNN.rar` or `.rar/.r00` volumes into private staging. Validate naming/order, run unrar listing and CRC testing, then extract regular files only. Password protection, missing parts, corrupt archives, unsafe paths, links and special files are rejected.
2. Declare every extracted output, including garbage, to obtain stable bot output IDs. Nested archives are not recursively extracted; a set without primary media is rejected.
3. Validate every required media output before publishing new files. Music goes through metadata/genre import; videos pass ffprobe and sampled decoding. No video enters Music, and movie requests containing episodes enter replacement recovery.
4. Publish atomically with SHA-256 receipts. Existing differing files are preserved. Retain verified, unambiguously associated artwork, subtitles and trailers. Samples, previews, executables, NFO, TXT and other garbage are skipped. The bot receives skip receipts for optional assets, including retained support files, because its required publication receipts cover primary media.
5. Verify local primary receipts against the bot's validated output manifest, then clean each mapped source volume and confirm the attempt. Discord removal/failure events are delivered by the bot's durable event API.

Intrinsic failures report a precise bad-link reason before scoped volume discard and replacement request. The bot enforces five total candidates and its bad-link search filter. Authentication, network, storage, missing-tool and timeout failures preserve source volumes for repair; local-dl retries the same set. A genuinely missing Put.io file can request replacement. The generic recovery monitor waits for archive-volume discard before requesting a replacement.

Restart resumes from durable jobs and publication receipts. Once any source volume has been purged, cleanup can resume without extracting again, but missing/changed local media blocks further cleanup. If all source volumes still exist, missing primary files can be restored only from the exact declared manifest and matching publication hashes. Existing changed files are never overwritten.

## Configuration and limits

- `ARCHIVE_STATE_PATH`: defaults to `archive-jobs.json` beside `CATALOG_DB` (normally `/data/archive-jobs.json` in deployment).
- `ARCHIVE_STAGING_PATH`: defaults to `archive-staging` beside the state file. Allow space for both compressed and extracted data; failed/completed work directories are removed, and interrupted staging is cleared on retry.
- `ARCHIVE_DOWNLOAD_ORIGINS`: optional comma-separated exact trusted origins, such as `https://mirror.example`. HTTPS `put.io` and its subdomains are allowed by default; every redirect is checked. Other origins and private/reserved destinations are rejected unless explicitly configured. Download requests do not include the bot bearer token.
- Runtime requires `unrar`, `ffprobe` and `ffmpeg`, included in the image.
- One set processes at a time. Limits: 1,000 source volumes, 100 GiB compressed, 100 GiB expanded, 100 archive entries, 100 declared files and a 16 KiB declaration body. Extraction times out after 30 minutes; each volume download after six hours. Sets exceeding archive/declaration limits are rejected as `archive_unsafe`.
- Existing `MUSIC_PATH`, movie/TV paths and video codec settings remain in effect. A codec rejection requires matching bot search policy before blacklisting and replacement.

Authenticated `GET /recovery/status` exposes `archives.jobs`, per-job receipts/errors, `archives.last_checked` and worker/state errors. Signed volume URLs are not persisted there.

## Verification

Run `UNRAR_TEST_TOOL=/path/to/unrar go test -race ./...` with ffprobe and ffmpeg on PATH, followed by `go vet ./...`. The permanent synthetic media fixtures exercise good music/movie sets, retained supports, garbage exclusion, missing/corrupt/password volumes, network holds, restart after partial cleanup, canonical library checks and restoration before cleanup. Without unrar, real extractor tests explicitly skip; do not interpret that as runtime verification.
