#!/usr/bin/env python3
"""Exercise the built downloader with synthetic audio and temporary mounts."""
import hashlib
import http.server
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

image = sys.argv[1] if len(sys.argv) > 1 else "local-dl:music-20260906"
fixture = (Path(__file__).resolve().parents[1] / "testdata/tagged.flac").read_bytes()

class Source(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Length", str(len(fixture)))
        self.end_headers()
        self.wfile.write(fixture)

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"items":[]}')

server = http.server.ThreadingHTTPServer(("0.0.0.0", 0), Source)
threading.Thread(target=server.serve_forever, daemon=True).start()
name = "local-dl-music-smoke-" + secrets.token_hex(4)
try:
    with tempfile.TemporaryDirectory(prefix="local-dl-smoke-") as tmp:
        root = Path(tmp)
        for folder in ("music", "data", "movies", "tv"):
            (root / folder).mkdir()
        token = secrets.token_hex(24)
        cmd = ["docker", "run", "-d", "--name", name, "--user", str(os.getuid())+":"+str(os.getgid()), "-p", "127.0.0.1::8080",
               "--add-host", "host.docker.internal:host-gateway",
               "-e", "CATALOG_DB=/data/catalog.db", "-e", "MUSIC_API_TOKEN=" + token,
               "-e", "REMOTE_SERVER=http://host.docker.internal:" + str(server.server_port)]
        for folder, dest in (("music", "/mnt/music"), ("data", "/data"), ("movies", "/mnt/movies"), ("tv", "/mnt/tvshows")):
            cmd += ["-v", str(root / folder) + ":" + dest]
        subprocess.run(cmd + [image], check=True, stdout=subprocess.DEVNULL)
        address = subprocess.check_output(["docker", "port", name, "8080/tcp"], text=True).strip()
        base = "http://" + address
        for _ in range(30):
            try:
                with urllib.request.urlopen(base + "/ping", timeout=2) as res:
                    assert json.load(res)["message"] == "pong"
                break
            except (OSError, urllib.error.URLError):
                time.sleep(1)
        else:
            raise AssertionError("container never became healthy")
        body = json.dumps({"type": "movie", "name": "song.flac", "file_size": len(fixture),
                           "url": "http://host.docker.internal:" + str(server.server_port) + "/audio"}).encode()
        request = urllib.request.Request(base + "/download", data=body,
                    headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
        with urllib.request.urlopen(request, timeout=5) as res:
            assert res.status == 200
        target = root / "music/Rock/Various Artists/Test Album/Disc 01/02 - Test Song.flac"
        for _ in range(50):
            if target.exists() and (root / "data/music-ingest.json").exists() and (root / "data/music-queue.json").exists() and json.loads((root / "data/music-queue.json").read_text()) == []:
                break
            time.sleep(1)
        assert target.is_file(), "music did not reach its genre/album directory"
        assert hashlib.sha256(target.read_bytes()).digest() == hashlib.sha256(fixture).digest()
        state = json.loads((root / "data/music-ingest.json").read_text())
        assert len(state["files"]) == 1
        assert json.loads((root / "data/music-queue.json").read_text()) == []
        assert not list((root / "movies").iterdir())
        print("MUSIC_CONTAINER_SMOKE_PASS")
finally:
    subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    server.shutdown()
