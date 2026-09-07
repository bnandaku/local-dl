import json
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from audit_library import audit_library
from catalog_notify import notify_quarantine


class CatalogNotificationTests(unittest.TestCase):
    def prepare(self, root):
        for library in ("movies", "tv", "music"):
            (root / library).mkdir()
        path = root / "catalog.db"
        with sqlite3.connect(path) as db:
            db.executescript("""
                CREATE TABLE files(file_path TEXT,status TEXT);
                CREATE TABLE catalog_sync_state(id INTEGER PRIMARY KEY,generation INTEGER,requested INTEGER,completed_request INTEGER,pending_request INTEGER);
                INSERT INTO catalog_sync_state VALUES(1,0,0,0,0);
                CREATE TRIGGER catalog_dirty_update AFTER UPDATE ON files BEGIN UPDATE catalog_sync_state SET generation=generation+1; END;
                INSERT INTO files VALUES('/mnt/movies/junk.nfo','active');
            """)
        (root / "movies/junk.nfo").write_text("junk")
        return path

    def test_quarantine_marks_catalog_and_requests_reconciliation(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            database = self.prepare(root)
            audit_library(root, apply=True, catalog_db=str(database))
            with sqlite3.connect(database) as db:
                self.assertEqual(db.execute("SELECT status FROM files").fetchone()[0], "deleted")
                self.assertEqual(db.execute("SELECT generation,requested FROM catalog_sync_state").fetchone(), (1, 1))

    def test_interrupted_notification_replays_saved_journal(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            database = self.prepare(root)
            with mock.patch("audit_library.notify_quarantine", side_effect=ValueError("DB unavailable")):
                with self.assertRaises(ValueError):
                    audit_library(root, apply=True, catalog_db=str(database))
            record = json.loads(next((root / ".local-dl-quarantine").glob("*/manifest.json")).read_text())[0]
            self.assertEqual(record["status"], "moved")
            notify_quarantine(database, root, record)
            notify_quarantine(database, root, record)
            with sqlite3.connect(database) as db:
                self.assertEqual(db.execute("SELECT generation FROM catalog_sync_state").fetchone()[0], 1)

    def test_missing_explicit_database_prevents_file_moves(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.prepare(root)
            with self.assertRaises(ValueError):
                audit_library(root, apply=True, catalog_db=str(root / "missing.db"))
            self.assertTrue((root / "movies/junk.nfo").exists())
