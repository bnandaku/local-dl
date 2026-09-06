#!/usr/bin/env python3
"""Audit Plex media libraries and optionally quarantine unknown files.

The default operation is read-only.  Quarantine is reversible: files are moved
under a timestamped directory and recorded in a manifest; nothing is deleted.
"""

import argparse
import errno
import json
import os
import re
import shutil
import sys
import tempfile
import time
from pathlib import Path

AUDIO = {"mp3", "flac", "m4a", "aac", "ogg", "opus", "wav", "aif", "aiff", "alac", "wma"}
VIDEO = {"mkv", "mp4", "m4v", "avi", "mov", "wmv", "mpg", "mpeg", "ts", "m2ts", "mts", "webm", "vob", "ogv", "3gp"}
ART = {"jpg", "jpeg", "png", "webp", "gif", "tif", "tiff", "bmp"}
SUBTITLES = {"srt", "ass", "ssa", "sub", "idx", "vtt", "smi", "sup"}
EPISODE_RE = re.compile(r"s\d{1,2}[\s._-]*e\d{1,3}", re.IGNORECASE)
LIBRARIES = ("movies", "tv", "music")


def is_episode(name):
    return bool(EPISODE_RE.search(name))


def _is_staging(name):
    return bool(re.match(r"^\.(?:media|music)-.+\.part$", name, re.IGNORECASE))


def _extension(path):
    suffix = Path(path).suffix.lower()
    return suffix[1:] if suffix.startswith(".") else ""


def _category(ext):
    if ext in AUDIO:
        return "audio"
    if ext in VIDEO:
        return "video"
    if ext in ART:
        return "art"
    if ext in SUBTITLES:
        return "subtitle"
    return None


def _entry(path, library, kind, reason):
    return {"path": str(path), "library": library, "kind": kind, "reason": reason}


def _safe_destination(base, relative):
    destination = (base / relative).resolve(strict=False)
    base_resolved = base.resolve()
    if os.path.commonpath((str(base_resolved), str(destination))) != str(base_resolved):
        raise ValueError("quarantine path escapes quarantine root")
    return destination


def _revalidate(path, original):
    try:
        current = os.lstat(path)
    except OSError:
        return False
    return (current.st_dev, current.st_ino, current.st_size, current.st_mtime_ns) == original


def _quarantine_one(source, destination, original):
    if not _revalidate(source, original):
        return "source_changed"
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists() or destination.is_symlink():
        return "destination_exists"
    created_destination = False
    try:
        # Hard-link then unlink is atomic within a filesystem and cannot
        # overwrite a pre-existing destination.
        os.link(source, destination)
        created_destination = True
        if not _revalidate(source, original):
            destination.unlink(missing_ok=True)
            return "source_changed"
        os.unlink(source)
    except OSError as exc:
        if created_destination:
            destination.unlink(missing_ok=True)
        if exc.errno != errno.EXDEV:
            return "move_error:%s" % exc
        # Cross-filesystem fallback: complete and fsync a fresh copy, verify
        # the source has not changed, then remove the source.
        temporary_handle = tempfile.NamedTemporaryFile(
            prefix=".local-dl-", suffix=".part", dir=str(destination.parent), delete=False)
        temporary = Path(temporary_handle.name)
        temporary_handle.close()
        try:
            shutil.copy2(source, temporary)
            with open(temporary, "rb") as fh:
                os.fsync(fh.fileno())
            # Linking the completed temporary file makes the final placement
            # fail safely if another process created the destination meanwhile.
            os.link(temporary, destination)
            created_destination = True
            os.unlink(temporary)
            if not _revalidate(source, original):
                if created_destination:
                    destination.unlink(missing_ok=True)
                return "source_changed"
            os.unlink(source)
        except OSError as copy_exc:
            temporary.unlink(missing_ok=True)
            if created_destination:
                destination.unlink(missing_ok=True)
            return "move_error:%s" % copy_exc
    return None


def audit_library(root, apply=False, report=None):
    root = Path(root).absolute()
    if not root.is_dir():
        raise ValueError("root must be a directory")
    records = []
    counts = {"files": 0, "recognized": 0, "garbage": 0, "misplaced": 0,
              "quarantined": 0, "errors": 0, "skipped_symlinks": 0, "skipped_staging": 0}
    quarantine_dir = None
    if apply:
        quarantine_base = root / ".local-dl-quarantine"
        for ancestor in (root, quarantine_base):
            if ancestor.is_symlink():
                raise ValueError("quarantine ancestor must not be a symlink")
        quarantine_base.mkdir(exist_ok=True)
        quarantine_dir = quarantine_base / time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        # Avoid collisions if two audits happen within the same second.
        if quarantine_dir.exists():
            quarantine_dir = quarantine_dir.with_name(quarantine_dir.name + "-%d" % os.getpid())
        if quarantine_dir.exists() or quarantine_dir.is_symlink():
            quarantine_dir = quarantine_dir.with_name(quarantine_dir.name + "-%d" % os.getpid())
        quarantine_dir.mkdir()

    journal = []

    def persist_journal():
        if quarantine_dir is None:
            return
        journal_path = quarantine_dir / "manifest.json"
        temporary = tempfile.NamedTemporaryFile(prefix=".manifest-", suffix=".part",
                                                 dir=str(quarantine_dir), delete=False, mode="w")
        try:
            json.dump(journal, temporary, indent=2)
            temporary.write("\n")
            temporary.flush()
            os.fsync(temporary.fileno())
            temporary.close()
            os.replace(temporary.name, journal_path)
            dir_fd = os.open(quarantine_dir, os.O_DIRECTORY)
            try:
                os.fsync(dir_fd)
            finally:
                os.close(dir_fd)
        finally:
            if not temporary.closed:
                temporary.close()
            Path(temporary.name).unlink(missing_ok=True)

    for library in LIBRARIES:
        library_root = root / library
        if not library_root.is_dir() or library_root.is_symlink():
            continue
        pending = [library_root]
        while pending:
            current = pending.pop()
            try:
                entries = os.scandir(current)
            except OSError as exc:
                counts["errors"] += 1
                records.append(_entry(current, library, "error", str(exc)))
                continue
            with entries:
                for item in entries:
                    if item.name == ".incoming":
                        continue
                    if _is_staging(item.name):
                        counts["skipped_staging"] += 1
                        continue
                    try:
                        st = item.stat(follow_symlinks=False)
                    except OSError as exc:
                        counts["errors"] += 1
                        records.append(_entry(Path(item.path), library, "error", str(exc)))
                        continue
                    if item.is_symlink():
                        counts["skipped_symlinks"] += 1
                        continue
                    if item.is_dir(follow_symlinks=False):
                        pending.append(Path(item.path))
                        continue
                    if not item.is_file(follow_symlinks=False):
                        continue
                    counts["files"] += 1
                    path = Path(item.path)
                    if item.name == ".plexignore":
                        counts["recognized"] += 1
                        continue
                    ext = _extension(path)
                    kind = _category(ext)
                    if kind is None:
                        counts["garbage"] += 1
                        rec = _entry(path, library, "garbage", "unknown_extension")
                        if apply:
                            relative = path.relative_to(library_root)
                            dest = _safe_destination(quarantine_dir / library, relative)
                            rec["destination"] = str(dest)
                            rec["status"] = "pending"
                            journal.append(rec.copy())
                            persist_journal()
                            error = _quarantine_one(path, dest, (st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns))
                            if error is None:
                                counts["quarantined"] += 1
                                journal[-1]["status"] = "moved"
                            else:
                                counts["errors"] += 1
                                rec["error"] = error
                                journal[-1]["status"] = "error"
                                journal[-1]["error"] = error
                            persist_journal()
                        records.append(rec)
                        continue
                    counts["recognized"] += 1
                    misplaced = ((kind == "audio" and library != "music") or
                                 (kind == "video" and library == "music") or
                                 (kind == "video" and library == "movies" and
                                  is_episode(str(path.relative_to(library_root)))))
                    if misplaced:
                        counts["misplaced"] += 1
                        records.append(_entry(path, library, kind, "misplaced"))

    result = {"root": str(root), "apply": bool(apply), "counts": counts, "items": records,
              "garbage": [item for item in records if item["kind"] == "garbage"],
              "misplaced": [item for item in records if item["reason"] == "misplaced"]}
    if quarantine_dir is not None:
        result["quarantine"] = str(quarantine_dir)
        if apply:
            persist_journal()
    if report:
        Path(report).write_text(json.dumps(result, indent=2) + "\n")
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, help="media root containing movies, tv, and music")
    parser.add_argument("--report", help="write JSON report to this path")
    parser.add_argument("--apply", action="store_true", help="move garbage to quarantine")
    args = parser.parse_args(argv)
    try:
        result = audit_library(args.root, apply=args.apply, report=args.report)
    except (OSError, ValueError) as exc:
        parser.error(str(exc))
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
