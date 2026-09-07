package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func catalogRootIdentity(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("catalog requires absolute movie and TV roots")
	}
	if e := safeArchiveDestination(path); e != nil {
		return "", fmt.Errorf("managed root unavailable; catalog retained")
	}
	info, e := os.Stat(path)
	if e != nil {
		return "", fmt.Errorf("managed root unavailable; catalog retained")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("cannot verify managed root identity")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
func checkCatalogRoots(requireKnown bool) (map[string]string, error) {
	roots := map[string]string{}
	for _, path := range []string{MoviesPath, TVShowPath} {
		identity, e := catalogRootIdentity(path)
		if e != nil {
			return nil, e
		}
		var known string
		e = CatalogDB.QueryRow(`SELECT identity FROM catalog_roots WHERE path=?`, path).Scan(&known)
		if e != nil && e != sql.ErrNoRows {
			return nil, e
		}
		if e == nil && known != identity {
			return nil, fmt.Errorf("managed root identity changed; verify mount before catalog reconciliation")
		}
		if e == sql.ErrNoRows && requireKnown {
			return nil, fmt.Errorf("managed root has not been reconciled")
		}
		roots[path] = identity
	}
	if len(roots) != 2 {
		return nil, fmt.Errorf("movie and TV roots must be distinct")
	}
	return roots, nil
}

func allowEmptyCatalogRoot(root string) bool {
	kind := "movie"
	if root == TVShowPath {
		kind = "tv"
	}
	for _, allowed := range strings.Split(os.Getenv("CATALOG_ALLOW_EMPTY_ROOTS"), ",") {
		if strings.TrimSpace(allowed) == kind {
			return true
		}
	}
	return false
}

func ScanAndUpdateCatalog() error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database unavailable")
	}
	start, e := readCatalogSyncState()
	if e != nil {
		return e
	}
	roots, e := checkCatalogRoots(false)
	if e != nil {
		return e
	}
	previous := map[string]bool{}
	rows, e := CatalogDB.Query(`SELECT file_path FROM files WHERE status='active'`)
	if e != nil {
		return e
	}
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			rows.Close()
			return e
		}
		previous[p] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	found := map[string]catalogRecord{}
	for root := range roots {
		count := 0
		validCount := 0
		e = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return fmt.Errorf("library scan failed; catalog retained")
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("library scan contains a symlink; catalog retained")
			}
			if info.IsDir() {
				return nil
			}
			if _, ok := catalogPrimary(path); !ok {
				return nil
			}
			if !info.Mode().IsRegular() || info.Size() <= 0 {
				return nil
			}
			count++
			r, e := makeCatalogRecord(path, info)
			if e != nil {
				return e
			}
			verified, e := hasCatalogVerification(path, info)
			if e != nil {
				return e
			}
			if !verified {
				if e = probeVideo(path); e != nil {
					var bad badMediaError
					if errors.As(e, &bad) {
						return nil
					}
					if _, excluded := codecRejection(e); excluded {
						return nil
					}
					return fmt.Errorf("media validation unavailable during scan; catalog retained")
				}
			}
			after, e := os.Lstat(path)
			if e != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
				return fmt.Errorf("library changed during scan; retry reconciliation")
			}
			if !verified {
				if e = saveCatalogVerification(CatalogDB, path, after); e != nil {
					return e
				}
			}
			validCount++
			found[path] = r
			return nil
		})
		if e != nil {
			return e
		}
		// An unexpectedly empty mount must not erase a previously populated root.
		if validCount == 0 {
			var known string
			rootErr := CatalogDB.QueryRow(`SELECT identity FROM catalog_roots WHERE path=?`, root).Scan(&known)
			if rootErr == sql.ErrNoRows && !allowEmptyCatalogRoot(root) {
				return fmt.Errorf("initial library root is empty; verify mount and explicitly enroll with CATALOG_ALLOW_EMPTY_ROOTS")
			}
			if rootErr != nil && rootErr != sql.ErrNoRows {
				return rootErr
			}

		}
		if count == 0 {
			for p := range previous {
				rel, err := filepath.Rel(root, p)
				if err == nil && rel != ".." && len(rel) > 0 && !filepath.IsAbs(rel) && (len(rel) < 3 || rel[:3] != "../") {
					return fmt.Errorf("populated library appears empty; verify storage before reconciliation")
				}
			}
		}
	}
	for root, identity := range roots {
		current, e := catalogRootIdentity(root)
		if e != nil || current != identity {
			return fmt.Errorf("managed root changed during scan")
		}
	}
	for path, r := range found {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Size() != r.Size || !info.ModTime().Equal(r.Modified) {
			return fmt.Errorf("library changed after scan; retry reconciliation")
		}
	}
	catalogMutationMu.Lock()
	defer catalogMutationMu.Unlock()
	tx, e := CatalogDB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	current, e := scanCatalogState(tx.QueryRow(catalogStateSelect))
	if e != nil {
		return e
	}
	if current.Generation != start.Generation {
		return fmt.Errorf("catalog changed during scan; retry reconciliation")
	}
	for _, r := range found {
		if e = upsertCatalogRecord(tx, r); e != nil {
			return e
		}
	}
	for p := range previous {
		if _, ok := found[p]; !ok {
			if _, e = tx.Exec(`UPDATE files SET status='deleted' WHERE file_path=? AND status='active'`, p); e != nil {
				return e
			}
		}
	}
	for root, identity := range roots {
		if _, e = tx.Exec(`INSERT OR IGNORE INTO catalog_roots(path,identity) VALUES(?,?)`, root, identity); e != nil {
			return e
		}
	}
	return tx.Commit()
}
