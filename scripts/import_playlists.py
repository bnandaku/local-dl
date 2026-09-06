#!/usr/bin/env python3
"""Import Spotify exports or the local Tidal bridge into local-dl playlists.

This tool only reads source playlists and optionally publishes a manifest to
local-dl. It never changes a source playlist or downloads media.
"""
import argparse
import csv
import json
import os
import re
import sys
from urllib.error import HTTPError
from urllib.parse import urlparse
from urllib.request import Request, urlopen

MAX_RESPONSE_BYTES = 8 * 1024 * 1024
TIMEOUT_SECONDS = 20


def _json_request(url, headers=None, method="GET", body=None):
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise ValueError("URL must be absolute http(s): %s" % url)
    request = Request(url, headers=headers or {}, method=method)
    if body is not None:
        request.data = json.dumps(body).encode("utf-8")
        request.add_header("Content-Type", "application/json")
    with urlopen(request, timeout=TIMEOUT_SECONDS) as response:
        data = response.read(MAX_RESPONSE_BYTES + 1)
        if len(data) > MAX_RESPONSE_BYTES:
            raise ValueError("source response exceeds %d bytes" % MAX_RESPONSE_BYTES)
        return json.loads(data.decode("utf-8")) if data else {}


def _track(title="", artist="", album="", isrc="", duration_ms=None, unavailable=False):
    value = {
        "title": title or "",
        "artist": artist or "",
        "album": album or "",
        "isrc": isrc or "",
        "duration_ms": duration_ms if duration_ms is not None else 0,
    }
    if unavailable:
        value["unavailable"] = True
    return value


def build_manifest(source, source_id, name, tracks):
    if source not in ("spotify", "tidal", "manual"):
        raise ValueError("source must be spotify, tidal, or manual")
    normalized = []
    for item in tracks:
        normalized.append(_track(item.get("title"), item.get("artist"), item.get("album"), item.get("isrc"), item.get("duration_ms"), item.get("unavailable", False)))
    return {"source": source, "source_id": str(source_id or ""), "name": name or "Unnamed", "tracks": normalized}


def _spotify_item(item):
    track = item.get("track") if isinstance(item, dict) and "track" in item else item
    if not isinstance(track, dict):
        return _track(unavailable=True)
    artists = track.get("artists") or []
    artist = ", ".join(a.get("name", "") for a in artists if isinstance(a, dict))
    external = track.get("external_ids") or {}
    return _track(track.get("name", track.get("trackName", "")), artist or track.get("artistName", ""),
                  (track.get("album") or {}).get("name", track.get("albumName", "")) if isinstance(track.get("album", {}), dict) else track.get("albumName", ""),
                  external.get("isrc", ""), track.get("duration_ms", 0), False)


def parse_spotify_export(payload):
    playlists = payload.get("playlists") if isinstance(payload, dict) else None
    if not isinstance(playlists, list):
        raise ValueError("Spotify export must contain a playlists array")
    result = []
    for playlist in playlists:
        items = playlist.get("items", [])
        # Some Spotify export variants call this field tracks.
        if not isinstance(items, list):
            items = playlist.get("tracks", [])
        tracks = []
        for item in items:
            if isinstance(item, dict) and "trackName" in item:
                tracks.append(_track(item.get("trackName"), item.get("artistName"), item.get("albumName"),
                                     item.get("isrc", ""), item.get("duration_ms", 0), False))
            else:
                tracks.append(_spotify_item(item))
        result.append(build_manifest("spotify", playlist.get("id", playlist.get("name", "")), playlist.get("name", "Unnamed"), tracks))
    return result


def parse_spotify_csv(fileobj):
    reader = csv.DictReader(fileobj)
    grouped = {}
    for row in reader:
        name = row.get("Playlist") or row.get("playlist") or "Imported"
        grouped.setdefault(name, []).append(_track(row.get("Track") or row.get("trackName"),
            row.get("Artist") or row.get("artistName"), row.get("Album") or row.get("albumName"),
            row.get("ISRC") or row.get("isrc", ""), row.get("Duration (ms)") or row.get("duration_ms") or 0))
    return [build_manifest("spotify", name, name, tracks) for name, tracks in grouped.items()]


def fetch_tidal_playlist(bridge_url, playlist_id):
    base = bridge_url.rstrip("/")
    payload = _json_request(base + "/api/tidal/playlists/" + str(playlist_id))
    tracks = []
    for item in payload.get("tracks", []):
        tracks.append(_track(item.get("title"), item.get("artist"), item.get("album"), item.get("isrc", ""), item.get("duration_ms", 0), False))
    return build_manifest("tidal", payload.get("id", playlist_id), payload.get("name", "Tidal playlist"), tracks)


def _spotify_url(value):
    match = re.search(r"playlist[/:]([A-Za-z0-9]+)", value)
    if not match:
        raise ValueError("could not find Spotify playlist ID")
    return "https://api.spotify.com/v1/playlists/" + match.group(1)


def fetch_spotify_playlist(value, token):
    initial = _spotify_url(value) if not value.startswith("http") else value
    parsed = urlparse(initial)
    if parsed.netloc != "api.spotify.com" or not parsed.path.startswith("/v1/playlists/"):
        raise ValueError("Spotify URL must use api.spotify.com/v1/playlists")
    headers = {"Authorization": "Bearer " + token}
    payload = _json_request(initial, headers)
    tracks = []
    # Spotify's current API separates playlist metadata from its items. Keep
    # compatibility with older responses that embed tracks in the metadata.
    if not isinstance(payload.get("items"), list) and not isinstance((payload.get("tracks") or {}).get("items"), list):
        page = _json_request(initial.rstrip("/") + "/items", headers)
    else:
        page = payload
    while True:
        container = page.get("items") if isinstance(page.get("items"), list) else page.get("tracks", {}).get("items", [])
        tracks.extend(_spotify_item(item) for item in container)
        next_url = page.get("next") if "items" in page else (page.get("tracks") or {}).get("next")
        if not next_url:
            break
        next_parsed = urlparse(next_url)
        if next_parsed.scheme != "https" or next_parsed.netloc != "api.spotify.com" or not next_parsed.path.startswith("/v1/"):
            raise ValueError("Spotify pagination URL is outside api.spotify.com")
        page = _json_request(next_url, headers)
    playlist_id = initial.rstrip("/").split("/")[-1]
    return build_manifest("spotify", playlist_id, payload.get("name", "Spotify playlist"), tracks)


def publish_manifest(local_dl_url, manifest, token):
    if not token:
        raise ValueError("MUSIC_API_TOKEN is required to publish")
    return _json_request(local_dl_url.rstrip("/") + "/music/playlists", {
        "Authorization": "Bearer " + token,
    }, "POST", manifest)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tidal-playlist")
    parser.add_argument("--tidal-bridge", default="http://127.0.0.1:8090")
    parser.add_argument("--spotify-playlist")
    parser.add_argument("--spotify-export")
    parser.add_argument("--spotify-csv")
    parser.add_argument("--local-dl")
    parser.add_argument("--output", help="JSON output path (default: stdout)")
    args = parser.parse_args(argv)
    selected = sum(bool(x) for x in (args.tidal_playlist, args.spotify_playlist, args.spotify_export, args.spotify_csv))
    if selected != 1:
        parser.error("choose exactly one playlist source")
    if args.tidal_playlist:
        manifests = [fetch_tidal_playlist(args.tidal_bridge, args.tidal_playlist)]
    elif args.spotify_playlist:
        manifests = [fetch_spotify_playlist(args.spotify_playlist, os.environ.get("SPOTIFY_ACCESS_TOKEN", ""))]
    elif args.spotify_export:
        with open(args.spotify_export, encoding="utf-8") as stream:
            manifests = parse_spotify_export(json.load(stream))
    else:
        with open(args.spotify_csv, encoding="utf-8", newline="") as stream:
            manifests = parse_spotify_csv(stream)
    output = json.dumps(manifests, indent=2, ensure_ascii=False) + "\n"
    if args.output:
        with open(args.output, "w", encoding="utf-8") as stream:
            stream.write(output)
    else:
        sys.stdout.write(output)
    if args.local_dl:
        token = os.environ.get("MUSIC_API_TOKEN", "")
        for manifest in manifests:
            publish_manifest(args.local_dl, manifest, token)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
