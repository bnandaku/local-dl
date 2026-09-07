package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"
)

type catalogAuth struct{}

func (catalogAuth) Error() string {
	return "catalog authentication refused; verify source and credential"
}

type catalogHold struct{ reason string }

func (e catalogHold) Error() string { return e.reason }

type catalogStale struct{}

func (catalogStale) Error() string { return "catalog snapshot is obsolete; reconcile watermark" }

var catalogSourceSyntax = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,99}$`)
var catalogHashSyntax = regexp.MustCompile(`^[0-9a-f]{64}$`)

type catalogWatermark struct {
	Enrolled *bool  `json:"enrolled"`
	Source   string `json:"source_id"`
	Sequence int64  `json:"sequence"`
	Hash     string `json:"sha256"`
}

func catalogAPIKey() string {
	if key := os.Getenv("CATALOG_API_KEY"); key != "" {
		return key
	}
	return os.Getenv("BOT_SERVICE_TOKEN")
}
func catalogHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func decodeCatalogJSON(response *http.Response, value any) error {
	content, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		return fmt.Errorf("catalog API did not return JSON")
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 65536))
	if err = decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid catalog API JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("catalog API JSON has trailing data")
	}
	return nil
}
func catalogRead(ctx context.Context, path string, value any) error {
	key := os.Getenv("CATALOG_API_KEY")
	if key == "" {
		return catalogAuth{}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", RemoteServer+path, nil)
	if err != nil {
		return fmt.Errorf("invalid catalog API configuration")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	response, err := catalogHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("catalog API read unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == 403 {
		var result struct {
			Error string `json:"error"`
		}
		if decodeCatalogJSON(response, &result) == nil && result.Error == "catalog_producer_conflict" {
			return catalogHold{"catalog producer conflict; restore original source and dedicated key ID"}
		}
		return catalogAuth{}
	}
	if response.StatusCode == 401 {
		return catalogAuth{}
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("catalog API read returned HTTP %d; verify source and credential", response.StatusCode)
	}
	return decodeCatalogJSON(response, value)
}
func readCatalogWatermark(ctx context.Context, source string) (catalogWatermark, error) {
	var w catalogWatermark
	err := catalogRead(ctx, "/api/v1/catalog/watermark?source_id="+url.QueryEscape(source), &w)
	if err != nil {
		return w, err
	}
	if w.Enrolled == nil {
		return w, fmt.Errorf("catalog watermark missing enrollment")
	}
	if *w.Enrolled {
		if w.Source != source || w.Sequence < 1 || !catalogHashSyntax.MatchString(w.Hash) {
			return w, catalogHold{"catalog watermark identity invalid"}
		}
	} else if w.Source != "" || w.Sequence != 0 || w.Hash != "" {
		return w, catalogHold{"unenrolled catalog watermark invalid"}
	}
	return w, nil
}
func prepareOrderedCatalog(ctx context.Context, s catalogSyncState) (catalogSyncState, error) {
	configured := os.Getenv("CATALOG_SOURCE_ID")
	if s.Source != "" && configured != "" && configured != s.Source {
		return s, catalogHold{"catalog source differs from persisted producer; restore original CATALOG_SOURCE_ID"}
	}
	source := s.Source
	if source == "" {
		source = configured
	}
	if source == "" {
		return s, nil
	}
	if os.Getenv("CATALOG_API_KEY") == "" {
		return s, catalogAuth{}
	}
	if !catalogSourceSyntax.MatchString(source) {
		return s, catalogHold{"invalid CATALOG_SOURCE_ID"}
	}
	if s.PendingSequence > 0 {
		if s.Source == "" || s.PendingSequence > s.Sequence || s.PendingHash != fmt.Sprintf("%x", sha256.Sum256(s.Pending)) {
			return s, catalogHold{"saved ordered snapshot identity invalid; preserve database for repair"}
		}
		return s, nil
	}
	var capabilities struct {
		Features []string `json:"features"`
	}
	if err := catalogRead(ctx, "/api/v1/capabilities", &capabilities); err != nil {
		return s, err
	}
	supported := false
	for _, feature := range capabilities.Features {
		if feature == "ordered_catalog_v1" {
			supported = true
		}
	}
	if !supported {
		return s, fmt.Errorf("bot ordered_catalog_v1 unavailable; catalog retained")
	}
	w, err := readCatalogWatermark(ctx, source)
	if err != nil {
		return s, err
	}
	sequence := s.Sequence
	if w.Sequence > sequence {
		sequence = w.Sequence
	}
	if sequence == math.MaxInt64 {
		return s, catalogHold{"catalog sequence exhausted"}
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(s.Pending))
	_, err = CatalogDB.Exec(`UPDATE catalog_sync_state SET source=?,sequence=?,pending_sequence=?,pending_hash=? WHERE id=1`, source, sequence+1, sequence+1, hash)
	if err != nil {
		return s, err
	}
	return readCatalogSyncState()
}
func reconcileStaleCatalog(ctx context.Context, s catalogSyncState) error {
	w, err := readCatalogWatermark(ctx, s.Source)
	if err != nil {
		return err
	}
	if !*w.Enrolled || w.Sequence <= s.PendingSequence {
		return catalogHold{"stale response contradicts catalog watermark; preserve snapshot"}
	}
	// The old snapshot is superseded, not acknowledged. Schedule a fresh full
	// reconciliation without claiming its dirty generation was accepted.
	_, err = CatalogDB.Exec(`UPDATE catalog_sync_state SET sequence=MAX(sequence,?),pending=NULL,pending_sequence=0,pending_hash='',pending_generation=0,pending_request=0,pending_full=0,uncertain=0,retry_at=0,failures=0,last_error='',requested=requested+1 WHERE id=1`, w.Sequence)
	return err
}
