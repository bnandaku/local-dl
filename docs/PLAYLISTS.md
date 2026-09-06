# Music playlists

The service accepts canonical manifests at `POST /music/playlists` and reconciles them into Plex audio playlists. Requests require `Authorization: Bearer $MUSIC_API_TOKEN`; all playlist routes are omitted when that variable is unset.

The manifest schema is:

```json
{
  "source": "spotify",
  "source_id": "spotify-playlist-id",
  "name": "Road Trip",
  "tracks": [
    {"title":"Track","artist":"Artist","album":"Album","isrc":"US...","duration_ms":210000}
  ]
}
```

`source` is one of `spotify`, `tidal`, or `manual`. `source_id` identifies the source playlist and is required for idempotent replacement. Track order is retained in the manifest. Matching uses ISRC when available on both sides, otherwise normalized artist and title. Supplied album and duration must also agree. Unavailable source tracks use `"unavailable": true` and remain pending. Missing and ambiguous tracks remain pending and are reported by `GET /music/playlists`.

`PLEX_URL`, `PLEX_TOKEN`, and `PLEX_MUSIC_SECTION_ID` enable synchronization. `MUSIC_PLAYLIST_DIR` defaults to `/data/music-playlists`. The persisted `playlists.json` records imported manifests, app-owned Plex playlist IDs, and reconciliation status. Spotify and Tidal exports must be converted to the schema above by the caller before or after acquisition; this service does not use either provider's credentials.
