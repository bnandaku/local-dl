# Playlist imports

`scripts/import_playlists.py` converts Spotify exports or a playlist read from
the local Tidal bridge into the local-dl playlist manifest. The manifest keeps
source order and duplicate entries. A source item that is unavailable or has
been removed is retained as an entry with empty metadata and
`"unavailable": true`, so imports never silently shorten a playlist.

The importer only reads Spotify/Tidal data and posts the resulting manifest to
local-dl when `--local-dl` is supplied. It does not edit source playlists,
download tracks, or call Discord. The local-dl endpoint is:

```text
POST /music/playlists
Authorization: Bearer $MUSIC_API_TOKEN
```

## Tidal bridge

The bridge already exposes playlist reads, so no Tidal token is copied into
this tool:

```bash
python3 scripts/import_playlists.py \
  --tidal-bridge http://peaches-unraid:8090 \
  --tidal-playlist 123 \
  --output tidal-123.json

MUSIC_API_TOKEN=... python3 scripts/import_playlists.py \
  --tidal-bridge http://peaches-unraid:8090 --tidal-playlist 123 \
  --local-dl http://peaches-unraid:8080
```

## Spotify

Spotify's current Web API uses `GET /v1/playlists/{id}/items`; the importer
also accepts older embedded `tracks` responses. A user access token can be
provided through `SPOTIFY_ACCESS_TOKEN` and must have permission to read the
playlist. The importer follows only HTTPS pagination links on
`api.spotify.com` and rejects other hosts.

```bash
SPOTIFY_ACCESS_TOKEN=... python3 scripts/import_playlists.py \
  --spotify-playlist https://open.spotify.com/playlist/PLAYLIST_ID \
  --output spotify-playlist.json
```

For an account export, use the JSON export shape with `playlists[].items[]`
fields such as `trackName`, `artistName`, and `albumName`:

```bash
python3 scripts/import_playlists.py --spotify-export spotify-export.json \
  --output spotify-manifests.json
python3 scripts/import_playlists.py --spotify-csv spotify.csv
```

The CSV reader groups rows by `Playlist` and accepts common export columns
(`Track`, `Artist`, `Album`, `ISRC`, and `Duration (ms)`). Review the JSON
manifest before publishing it. HTTP failures, malformed JSON, oversized
responses, missing source playlists, and unsafe pagination links abort the
operation; publishing happens only after a complete source read.
