# Media routing and library hygiene

Imports use a file-extension allowlist at both the Put.io queue producer and the
local downloader. Audio always goes to music. Video always goes to movies or TV;
a case-insensitive episode marker such as `S01E02`, `S01 E02`, `S01.E02` or
`S01-E02` in its name or source directory forces TV routing, even if the incoming
label says movie. Existing explicit TV/anime labels are preserved for video.

Recognized audio: MP3, FLAC, M4A, AAC, OGG, Opus, WAV, AIF/AIFF, ALAC, WMA.
Recognized video: MKV, MP4, M4V, AVI, MOV, WMV, MPG/MPEG, TS/M2TS/MTS, WebM,
VOB, OGV, 3GP. Artwork: JPG/JPEG, PNG, WebP, GIF, TIF/TIFF, BMP.
Subtitles: SRT, ASS/SSA, SUB/IDX, VTT, SMI, SUP.

Other extensions, including NFO, TXT, EXE, scripts and archives, are excluded
before download. Small files are allowed when their format is supported. Artwork
and subtitles carry an explicit primary-media association; ambiguous supporting
files remain at the source instead of being guessed into a library. Music artwork
waits for the associated audio import so it can use the tagged album directory.
Video supports also wait for a verified primary receipt and follow its actual
published path, including collision suffixes. Movie imports use a per-movie directory; matched subtitles retain language suffixes
and follow the standardized episode name. Associated trailers use a Trailers folder.

Audio is probed and rejects moving video streams (embedded cover art is allowed).
Video must contain a video stream; downloads with invalid content, bad HTTP status,
or inconsistent byte counts cannot complete. Staged files are atomically published
without overwriting existing media. Unknown files are never acknowledged as imported.

## Existing library audit

`python3 scripts/audit_library.py --root /path/to/media --report audit.json`
scans movies, tv and music recursively without following symlinks. Add `--probe`
to validate audio/video streams and artwork, and detect failed-download payloads
in subtitles. Probe timeouts or unavailable tooling are reported as scan errors
and never quarantined. It reports
unknown files and audio/video in the wrong libraries. `--apply --probe` moves unknown or invalid
files outside those libraries into `.local-dl-quarantine/<run>/` and records an
fsynced recovery journal. It never permanently deletes them or guesses a destination
for recognized media. Active staging files and `.plexignore` are retained.

For a reviewed misplaced primary file, stop the downloader and run its image with
its existing mounts/environment and `repair-file /mnt/.../filename` as arguments.
This offline command validates content, publishes into the correct library using
the normal naming rules, saves a recovery copy and manifest under
`/data/media-quarantine`, and only then removes the misplaced source. It sends no
Put.io acknowledgement. Do not run it concurrently with downloader state writes.
