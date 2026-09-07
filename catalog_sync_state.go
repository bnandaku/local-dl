package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type catalogSyncState struct {
	Uncertain         bool   `json:"uncertain_delivery"`
	Generation        int64  `json:"generation"`
	Acknowledged      int64  `json:"acknowledged"`
	FirstDirty        int64  `json:"first_dirty"`
	LastDirty         int64  `json:"last_dirty"`
	Requested         int64  `json:"requested"`
	CompletedRequest  int64  `json:"completed_request"`
	LastFull          int64  `json:"last_full"`
	NextFull          int64  `json:"next_full"`
	RetryAt           int64  `json:"retry_at"`
	Failures          int    `json:"failures"`
	LastError         string `json:"last_error,omitempty"`
	Pending           []byte `json:"-"`
	PendingGeneration int64  `json:"pending_generation"`
	PendingRequest    int64  `json:"pending_request"`
	PendingFull       bool   `json:"pending_full"`
}

func initCatalogSyncSchema() error {
	_, e := CatalogDB.Exec(`CREATE TABLE IF NOT EXISTS catalog_sync_state (
 id INTEGER PRIMARY KEY CHECK(id=1), uncertain INTEGER NOT NULL DEFAULT 0, generation INTEGER NOT NULL DEFAULT 0, acknowledged INTEGER NOT NULL DEFAULT 0,
 first_dirty INTEGER NOT NULL DEFAULT 0,last_dirty INTEGER NOT NULL DEFAULT 0,
 requested INTEGER NOT NULL DEFAULT 1,completed_request INTEGER NOT NULL DEFAULT 0,
 last_full INTEGER NOT NULL DEFAULT 0,next_full INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0,failures INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',
 pending BLOB,pending_generation INTEGER NOT NULL DEFAULT 0,pending_request INTEGER NOT NULL DEFAULT 0,pending_full INTEGER NOT NULL DEFAULT 0);
 INSERT OR IGNORE INTO catalog_sync_state(id) VALUES(1);
 CREATE TABLE IF NOT EXISTS catalog_roots(path TEXT PRIMARY KEY,identity TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS catalog_verified(file_path TEXT PRIMARY KEY,proof TEXT NOT NULL,policy TEXT NOT NULL);`)
	if e != nil {
		return e
	}
	rows, e := CatalogDB.Query("PRAGMA table_info(catalog_sync_state)")
	if e != nil {
		return e
	}
	hasUncertain := false
	for rows.Next() {
		var id, required, primary int
		var name, kind string
		var defaultValue interface{}
		if e = rows.Scan(&id, &name, &kind, &required, &defaultValue, &primary); e != nil {
			rows.Close()
			return e
		}
		if name == "uncertain" {
			hasUncertain = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if !hasUncertain {
		if _, e = CatalogDB.Exec("ALTER TABLE catalog_sync_state ADD COLUMN uncertain INTEGER NOT NULL DEFAULT 0"); e != nil {
			return e
		}
	}
	meaningful := `OLD.file_path IS NOT NEW.file_path OR OLD.filename IS NOT NEW.filename OR OLD.media_type IS NOT NEW.media_type OR OLD.show_name IS NOT NEW.show_name OR OLD.season IS NOT NEW.season OR OLD.episode IS NOT NEW.episode OR OLD.title IS NOT NEW.title OR OLD.year IS NOT NEW.year OR OLD.quality IS NOT NEW.quality OR OLD.file_size IS NOT NEW.file_size OR OLD.modified_at IS NOT NEW.modified_at OR OLD.status IS NOT NEW.status`
	for _, item := range []struct{ name, action, when string }{{"insert", "INSERT", ""}, {"update", "UPDATE", " WHEN " + meaningful}, {"delete", "DELETE", ""}} {
		_, e = CatalogDB.Exec(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS catalog_dirty_%s AFTER %s ON files%s BEGIN
 UPDATE catalog_sync_state SET generation=generation+1,
 first_dirty=CASE WHEN generation=acknowledged THEN CAST(strftime('%%s','now') AS INTEGER) ELSE first_dirty END,
 last_dirty=CAST(strftime('%%s','now') AS INTEGER) WHERE id=1; END;`, item.name, item.action, item.when))
		if e != nil {
			return e
		}
		prefix, event := "NEW", "'added'"
		if item.name == "delete" {
			prefix, event = "OLD", "'deleted'"
		}
		if item.name == "update" {
			event = "CASE WHEN NEW.status IN ('deleted','moved') THEN NEW.status ELSE 'updated' END"
		}
		values := []string{event}
		for _, column := range []string{"file_path", "media_type", "show_name", "season", "episode", "title", "year"} {
			values = append(values, prefix+"."+column)
		}
		_, e = CatalogDB.Exec(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS catalog_audit_%s AFTER %s ON files%s BEGIN INSERT INTO catalog_events(event_type,file_path,media_type,show_name,season,episode,title,year) VALUES(%s); END;`, item.name, item.action, item.when, strings.Join(values, ",")))
		if e != nil {
			return e
		}
	}
	return nil
}

const catalogStateSelect = `SELECT uncertain,generation,acknowledged,first_dirty,last_dirty,requested,completed_request,last_full,next_full,retry_at,failures,last_error,pending,pending_generation,pending_request,pending_full FROM catalog_sync_state WHERE id=1`

func scanCatalogState(row *sql.Row) (s catalogSyncState, e error) {
	e = row.Scan(&s.Uncertain, &s.Generation, &s.Acknowledged, &s.FirstDirty, &s.LastDirty, &s.Requested, &s.CompletedRequest, &s.LastFull, &s.NextFull, &s.RetryAt, &s.Failures, &s.LastError, &s.Pending, &s.PendingGeneration, &s.PendingRequest, &s.PendingFull)
	return
}
func readCatalogSyncState() (catalogSyncState, error) {
	if CatalogDB == nil {
		return catalogSyncState{}, fmt.Errorf("catalog database unavailable")
	}
	return scanCatalogState(CatalogDB.QueryRow(catalogStateSelect))
}

// Requests persist and coalesce until captured. A request arriving during an
// upload advances the counter so the older acknowledgement cannot erase it.
func RequestCatalogReconciliation() error {
	if CatalogDB == nil {
		return fmt.Errorf("catalog database unavailable")
	}
	_, e := CatalogDB.Exec(`UPDATE catalog_sync_state SET requested=CASE WHEN requested=completed_request OR requested=pending_request THEN requested+1 ELSE requested END WHERE id=1`)
	DebouncedCatalogSync()
	return e
}
func catalogBatchDue(s catalogSyncState, now time.Time) bool {
	return s.Generation > s.Acknowledged && (now.Unix() >= s.LastDirty+30 || now.Unix() >= s.FirstDirty+120)
}
