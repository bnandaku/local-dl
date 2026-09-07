package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var catalogMutationMu sync.Mutex

type catalogRecord struct {
	Path, Name, Kind, Show, Season, Episode, Title, Year, Quality string
	Size                                                          int64
	Modified                                                      time.Time
}

func catalogPrimary(path string) (string, bool) {
	if mediaKind(path) != "video" {
		return "", false
	}
	for _, root := range []struct{ path, kind string }{{MoviesPath, "movie"}, {TVShowPath, "tv"}} {
		if root.path == "" {
			continue
		}
		rel, e := filepath.Rel(root.path, path)
		if e != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		if archiveTrailer.MatchString(rel) || archiveSample.MatchString(rel) {
			return "", false
		}
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") {
				return "", false
			}
		}
		_, e = archivePrimaryItem(archiveSet{MediaType: root.kind}, archiveOutput{Path: rel})
		if root.kind == "tv" {
			info := catalogTVInfo(path)
			if !info.HasSeasonInfo || info.ShowName == "" {
				return "", false
			}
		}
		return root.kind, e == nil
	}
	return "", false
}
func catalogTVInfo(path string) TVShowInfo {
	name := filepath.Base(path)
	info := parseTVShowInfo(name)
	if info.HasSeasonInfo && info.ShowName != "" {
		return info
	}
	relative, e := filepath.Rel(TVShowPath, path)
	if e != nil {
		return info
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for i := len(parts) - 2; i >= 0; i-- {
		candidate := parseTVShowInfo(parts[i] + "." + name)
		if candidate.HasSeasonInfo && candidate.ShowName != "" {
			return candidate
		}
	}
	return info
}
func makeCatalogRecord(path string, info os.FileInfo) (catalogRecord, error) {
	kind, ok := catalogPrimary(path)
	if !ok || !info.Mode().IsRegular() || info.Size() <= 0 {
		return catalogRecord{}, fmt.Errorf("not managed primary video")
	}
	r := catalogRecord{Path: path, Name: filepath.Base(path), Kind: kind, Size: info.Size(), Modified: info.ModTime().UTC()}
	if kind == "movie" {
		m := parseMovieInfo(r.Name)
		r.Title, r.Year, r.Quality = m.Title, m.Year, m.Quality
	} else {
		m := catalogTVInfo(path)
		r.Show, r.Season, r.Episode = m.ShowName, m.Season, m.Episode
	}
	return r, nil
}
func upsertCatalogRecord(tx *sql.Tx, r catalogRecord) error {
	_, e := tx.Exec(`INSERT INTO files(file_path,filename,media_type,show_name,season,episode,title,year,quality,file_size,created_at,modified_at,last_seen_at,status)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'active') ON CONFLICT(file_path) DO UPDATE SET filename=excluded.filename,media_type=excluded.media_type,show_name=excluded.show_name,season=excluded.season,episode=excluded.episode,title=excluded.title,year=excluded.year,quality=excluded.quality,file_size=excluded.file_size,modified_at=excluded.modified_at,last_seen_at=excluded.last_seen_at,status='active'`,
		r.Path, r.Name, r.Kind, r.Show, r.Season, r.Episode, r.Title, r.Year, r.Quality, r.Size, r.Modified, r.Modified, time.Now().UTC())
	return e
}
func AddFileToCatalog(path string) error { return addCatalogFile(path, true) }

// Publication callers already ran media validation before their atomic rename.
func AddPublishedFileToCatalog(path string) error { return addCatalogFile(path, false) }

func addCatalogFile(path string, validate bool) error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database unavailable")
	}
	catalogMutationMu.Lock()
	defer catalogMutationMu.Unlock()
	info, e := os.Lstat(path)
	if e != nil {
		return e
	}
	r, e := makeCatalogRecord(path, info)
	if e != nil {
		return e
	}
	if e = safeArchiveDestination(filepath.Dir(path)); e != nil {
		return e
	}
	// Callers publish atomically; also validate this public catalog entry point.
	if validate {
		if e = probeVideo(path); e != nil {
			return e
		}
	}
	after, e := os.Lstat(path)
	if e != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("media changed during catalog validation")
	}
	tx, e := CatalogDB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = upsertCatalogRecord(tx, r); e != nil {
		return e
	}
	if e = saveCatalogVerification(tx, path, after); e != nil {
		return e
	}
	if e = tx.Commit(); e == nil {
		DebouncedCatalogSync()
	}
	return e
}
func RemoveFileFromCatalog(path string) error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database unavailable")
	}
	catalogMutationMu.Lock()
	defer catalogMutationMu.Unlock()
	_, e := CatalogDB.Exec(`UPDATE files SET status='deleted',last_seen_at=? WHERE file_path=? AND status='active'`, time.Now().UTC(), path)
	if e == nil {
		DebouncedCatalogSync()
	}
	return e
}

// A repair can remove/quarantine media or move it into another library. Publish
// the old/new catalog paths in a single transaction so snapshots cannot split it.
func UpdateCatalogAfterMove(oldPath, newPath string) error {
	if CatalogDB == nil {
		return nil
	}
	catalogMutationMu.Lock()
	defer catalogMutationMu.Unlock()
	var record *catalogRecord
	if _, ok := catalogPrimary(newPath); ok {
		info, e := os.Lstat(newPath)
		if e != nil {
			return e
		}
		r, e := makeCatalogRecord(newPath, info)
		if e != nil {
			return e
		}
		record = &r
	}
	tx, e := CatalogDB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE files SET status='moved' WHERE file_path=? AND status='active'`, oldPath); e != nil {
		return e
	}
	if record != nil {
		if e = upsertCatalogRecord(tx, *record); e != nil {
			return e
		}
	}
	if e = tx.Commit(); e == nil {
		DebouncedCatalogSync()
	}
	return e
}
