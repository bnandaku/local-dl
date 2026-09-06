import io
import json
import os
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.error import HTTPError

sys.path.insert(0, os.path.dirname(__file__))
from import_playlists import (
    build_manifest,
    parse_spotify_csv,
    parse_spotify_export,
    fetch_spotify_playlist,
    fetch_tidal_playlist,
    publish_manifest,
)
import import_playlists


class _Server(BaseHTTPRequestHandler):
    routes = {}
    posts = []

    def do_GET(self):
        status, body = self.routes.get(self.path, (404, {"error": "missing"}))
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.posts.append(json.loads(self.rfile.read(length)))
        self.send_response(201)
        self.end_headers()

    def log_message(self, *_):
        pass


class ImportPlaylistTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = HTTPServer(("127.0.0.1", 0), _Server)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.base = "http://127.0.0.1:%d" % cls.server.server_port

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()

    def setUp(self):
        _Server.routes = {}
        _Server.posts = []

    def test_export_preserves_order_duplicates_and_unavailable_items(self):
        payload = {"playlists": [{"name": "Road", "items": [
            {"trackName": "A", "artistName": "One", "albumName": "X", "trackUri": "spotify:track:1"},
            {"trackName": "A", "artistName": "One", "albumName": "X", "trackUri": "spotify:track:1"},
            {"track": None},
        ]}]}
        result = parse_spotify_export(payload)[0]
        self.assertEqual([x["title"] for x in result["tracks"]], ["A", "A", ""])
        self.assertTrue(result["tracks"][2]["unavailable"])

    def test_csv_normalization(self):
        csv_data = io.StringIO("Playlist,Track,Artist,Album,Track URI\nRoad,A,One,X,spotify:track:1\n")
        result = parse_spotify_csv(csv_data)[0]
        self.assertEqual(result["name"], "Road")
        self.assertEqual(result["tracks"][0]["artist"], "One")
        self.assertIsInstance(result["tracks"][0]["duration_ms"], int)

    def test_spotify_documented_url_and_bare_id_inputs(self):
        original = import_playlists._json_request
        calls = []
        def fake(url, headers=None, method="GET", body=None):
            calls.append(url)
            if url.endswith("/items"):
                return {"items": [], "next": None}
            return {"name": "S"}
        import_playlists._json_request = fake
        try:
            self.assertEqual(fetch_spotify_playlist("https://open.spotify.com/playlist/abc123", "t")["source_id"], "abc123")
            self.assertEqual(fetch_spotify_playlist("abc123", "t")["source_id"], "abc123")
        finally:
            import_playlists._json_request = original
        self.assertTrue(all("abc123" in url for url in calls))

    def test_spotify_requires_token_before_request(self):
        with self.assertRaises(ValueError):
            fetch_spotify_playlist("abc123", "")

    def test_spotify_current_item_wrapper(self):
        original = import_playlists._json_request
        import_playlists._json_request = lambda url, headers=None, method="GET", body=None: (
            {"name": "S"} if not url.endswith("/items") else {"items": [
                {"item": {"name": "A", "artists": [{"name": "O"}], "album": {"name": "X"}}}
            ]})
        try:
            result = fetch_spotify_playlist("https://api.spotify.com/v1/playlists/x", "token")
        finally:
            import_playlists._json_request = original
        self.assertEqual(result["tracks"][0]["title"], "A")

    def test_pagination_cycle_is_bounded(self):
        _Server.routes["/v1/playlists/x"] = (200, {"name": "S", "tracks": {"items": [], "next": self.base + "/v1/playlists/x"}})
        with self.assertRaises(ValueError):
            fetch_spotify_playlist(self.base + "/v1/playlists/x", "token")

    def test_tidal_fetch_maps_tracks(self):
        _Server.routes["/api/tidal/playlists/7"] = (200, {"id": "7", "name": "Tidal", "tracks": [
            {"title": "Song", "artist": "Artist", "album": "Album", "duration_ms": 1200}
        ]})
        result = fetch_tidal_playlist(self.base, "7")
        self.assertEqual(result["source"], "tidal")
        self.assertEqual(result["tracks"][0]["title"], "Song")

    def test_spotify_pagination_and_rejects_external_next(self):
        _Server.routes["/v1/playlists/x"] = (200, {"name": "S", "tracks": {"items": [
            {"track": {"name": "A", "artists": [{"name": "O"}], "album": {"name": "X"}}}
        ], "next": self.base + "/v1/next"}})
        with self.assertRaises(ValueError):
            fetch_spotify_playlist(self.base + "/v1/playlists/x", "token")

    def test_non_200_does_not_publish(self):
        _Server.routes["/bad"] = (500, {"error": "no"})
        with self.assertRaises(HTTPError):
            fetch_tidal_playlist(self.base, "bad")
        self.assertEqual(_Server.posts, [])

    def test_publish_posts_canonical_manifest(self):
        manifest = build_manifest("manual", "id", "Name", [{"title": "A", "artist": "B"}])
        publish_manifest(self.base, manifest, "secret")
        self.assertEqual(_Server.posts[0]["source"], "manual")
        self.assertEqual(_Server.posts[0]["tracks"][0]["album"], "")


if __name__ == "__main__":
    unittest.main()
