"""Durable catalog notifications for the offline quarantine tool."""
import os
import sqlite3
from pathlib import Path


def catalog_database(root, configured=None):
    candidate = configured or os.environ.get("CATALOG_DB")
    if not candidate:
        default = Path(root) / "data" / "tvshows_catalog.db"
        if default.is_file():
            candidate = str(default)
    if not candidate:
        return None
    path = Path(candidate).absolute()
    try:
        with sqlite3.connect(path.as_uri() + "?mode=rw", uri=True, timeout=5) as db:
            if db.execute("SELECT generation FROM catalog_sync_state WHERE id=1").fetchone() is None:
                raise ValueError("catalog sync state missing")
            triggers = db.execute("SELECT name FROM sqlite_master WHERE type='trigger' AND name='catalog_dirty_update'").fetchone()
            if not triggers:
                raise ValueError("catalog dirty trigger missing")
    except sqlite3.Error as exc:
        raise ValueError("catalog database unavailable or not migrated; quarantine not started") from exc
    return path


def notify_quarantine(database, root, record):
    if database is None or record.get("status") != "moved":
        return
    source = Path(record["path"])
    library = record["library"]
    relative = source.relative_to(Path(root).absolute() / library)
    if ".." in relative.parts:
        raise ValueError("unsafe quarantine source")
    if source.exists() or source.is_symlink():
        raise ValueError("quarantine source reappeared; catalog preserved")
    destination = Path(record["destination"])
    destination.resolve().relative_to((Path(root).absolute() / ".local-dl-quarantine").resolve())
    if not destination.is_file() or destination.is_symlink():
        raise ValueError("quarantine copy unavailable; catalog preserved")
    roots = {"movies": os.environ.get("MOVIES_PATH", "/mnt/movies"),
             "tv": os.environ.get("TVSHOW_PATH", "/mnt/tvshows"),
             "music": os.environ.get("MUSIC_PATH", "/mnt/music")}
    mapped = str(Path(roots[library]) / relative)
    try:
        with sqlite3.connect(database.as_uri() + "?mode=rw", uri=True, timeout=5) as db:
            db.execute("UPDATE files SET status='deleted' WHERE status='active' AND file_path IN (?,?)",
                       (str(source), mapped))
            db.execute("UPDATE catalog_sync_state SET requested=CASE WHEN requested=completed_request OR requested=pending_request THEN requested+1 ELSE requested END WHERE id=1")
    except sqlite3.Error as exc:
        raise ValueError("catalog notification pending; replay the saved quarantine journal") from exc
