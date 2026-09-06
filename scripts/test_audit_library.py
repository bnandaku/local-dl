import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from audit_library import audit_library, is_episode


class AuditLibraryTests(unittest.TestCase):
    def test_episode_pattern_accepts_separators_and_is_case_insensitive(self):
        self.assertTrue(is_episode("Show.s01 e02.mkv"))
        self.assertTrue(is_episode("show_S2-E103.mp4"))
        self.assertFalse(is_episode("movie.2019.mkv"))

    def test_read_only_report_classifies_garbage_and_misplaced_media(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for name in ("movies", "tv", "music"):
                (root / name).mkdir()
            (root / "movies" / "Film.mkv").write_bytes(b"movie")
            (root / "movies" / "Show S01E02.mkv").write_bytes(b"episode")
            (root / "movies" / "junk.nfo").write_text("x")
            (root / "tv" / "song.mp3").write_bytes(b"audio")
            report = audit_library(root)
            self.assertEqual(report["counts"]["garbage"], 1)
            self.assertEqual(report["counts"]["misplaced"], 2)
            self.assertEqual(report["counts"]["quarantined"], 0)
            self.assertTrue((root / "movies" / "junk.nfo").exists())

    def test_episode_directory_marks_generic_video_in_movies_misplaced(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for name in ("movies", "tv", "music"):
                (root / name).mkdir()
            episode_dir = root / "movies" / "Show" / "S01E02"
            episode_dir.mkdir(parents=True)
            (episode_dir / "video.mkv").write_bytes(b"episode")
            report = audit_library(root)
            self.assertEqual(report["counts"]["misplaced"], 1)

    def test_active_staging_files_are_skipped(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for name in ("movies", "tv", "music"):
                (root / name).mkdir()
            (root / "music" / ".media-download.part").write_bytes(b"partial")
            (root / "music" / ".music-download.part").write_bytes(b"partial")
            report = audit_library(root, apply=True)
            self.assertEqual(report["counts"]["garbage"], 0)
            self.assertEqual(report["counts"]["skipped_staging"], 2)

    def test_apply_quarantines_only_garbage_with_manifest_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for name in ("movies", "tv", "music"):
                (root / name).mkdir()
            garbage = root / "music" / "notes.txt"
            garbage.write_text("keep me")
            media = root / "music" / "track.flac"
            media.write_bytes(b"audio")
            report = audit_library(root, apply=True)
            self.assertFalse(garbage.exists())
            self.assertTrue(media.exists())
            self.assertEqual(report["counts"]["quarantined"], 1)
            run_dir = Path(report["quarantine"])
            self.assertEqual((run_dir / "music" / "notes.txt").read_text(), "keep me")
            manifest = json.loads((run_dir / "manifest.json").read_text())
            self.assertEqual(manifest[0]["reason"], "unknown_extension")

    def test_symlinks_and_incoming_are_not_followed(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for name in ("movies", "tv", "music"):
                (root / name).mkdir()
            (root / "music" / ".incoming").mkdir()
            (root / "music" / ".incoming" / "bad.exe").write_bytes(b"x")
            outside = root / "outside.txt"
            outside.write_text("x")
            os.symlink(outside, root / "music" / "link.txt")
            report = audit_library(root, apply=True)
            self.assertTrue(outside.exists())
            self.assertTrue((root / "music" / "link.txt").is_symlink())
            self.assertEqual(report["counts"]["quarantined"], 0)

    def test_cli_emits_json_report(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for name in ("movies", "tv", "music"):
                (root / name).mkdir()
            (root / "movies" / "bad.exe").write_bytes(b"x")
            out = root / "report.json"
            result = subprocess.run([sys.executable, str(Path(__file__).with_name("audit_library.py")),
                                     "--root", str(root), "--report", str(out)],
                                    check=True, capture_output=True, text=True)
            self.assertEqual(json.loads(result.stdout)["counts"]["garbage"], 1)
            self.assertEqual(json.loads(out.read_text())["counts"]["garbage"], 1)

    def test_apply_rejects_symlink_quarantine_ancestor(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for name in ("movies", "tv", "music"):
                (root / name).mkdir()
            outside = root / "outside"
            outside.mkdir()
            os.symlink(outside, root / ".local-dl-quarantine")
            with self.assertRaises(ValueError):
                audit_library(root, apply=True)


if __name__ == "__main__":
    unittest.main()
