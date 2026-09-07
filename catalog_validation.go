package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// A legacy catalog row alone is not evidence of media validation. Persist
// successful checks separately so the first reconciliation validates old rows,
// while subsequent daily scans reuse unchanged files and playback policy.
func catalogProof(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano())
}
func catalogPolicyKey() string {
	return strings.ToLower(strings.TrimSpace(musicEnv("VIDEO_EXCLUDED_CODECS", "dolby_vision")))
}
func hasCatalogVerification(path string, info os.FileInfo) (bool, error) {
	var proof, policy string
	e := CatalogDB.QueryRow(`SELECT proof,policy FROM catalog_verified WHERE file_path=?`, path).Scan(&proof, &policy)
	if e == sql.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return proof != "" && proof == catalogProof(info) && policy == catalogPolicyKey(), nil
}
func saveCatalogVerification(exec interface {
	Exec(string, ...interface{}) (sql.Result, error)
}, path string, info os.FileInfo) error {
	proof := catalogProof(info)
	if proof == "" {
		return fmt.Errorf("cannot fingerprint validated media")
	}
	_, e := exec.Exec(`INSERT INTO catalog_verified(file_path,proof,policy) VALUES(?,?,?) ON CONFLICT(file_path) DO UPDATE SET proof=excluded.proof,policy=excluded.policy`, path, proof, catalogPolicyKey())
	return e
}
