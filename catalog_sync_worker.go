package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"mime"
	"net/http"
	"net/http/httptrace"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// An unacknowledged POST may still be executing on the legacy server. Without
// server-side ordering, even a later duplicate ACK cannot prove it has settled.
// Keep replaying only the frozen snapshot; never advance to newer generations.
type catalogDeliveryUncertain struct{}

func (catalogDeliveryUncertain) Error() string {
	return "catalog delivery uncertain; newer snapshots fenced pending server ordering/reconciliation"
}

var catalogWorkerMu sync.Mutex
var catalogWake = make(chan struct{}, 1)

// Compatibility trigger: all callers schedule the same durable worker.
func DebouncedCatalogSync() {
	select {
	case catalogWake <- struct{}{}:
	default:
	}
}
func SendCatalogUpdate() error { return RequestCatalogReconciliation() }
func FinishInitialScan() {
	if e := RequestCatalogReconciliation(); e != nil {
		logMessage(LogLevelWarn, "CatalogSync", "Cannot persist reconciliation request")
	}
}
func runCatalogSync(ctx context.Context) {
	if CatalogDB == nil {
		return
	}
	FinishInitialScan()
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = catalogSyncCycle(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-catalogWake:
		}
	}
}
func captureCatalogSnapshot(now time.Time, full bool) error {
	catalogMutationMu.Lock()
	defer catalogMutationMu.Unlock()
	tx, e := CatalogDB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	s, e := scanCatalogState(tx.QueryRow(catalogStateSelect))
	if e != nil {
		return e
	}
	if len(s.Pending) > 0 {
		return nil
	}
	data, e := catalogSnapshot(tx, now)
	if e != nil {
		return e
	}
	payload, e := json.Marshal(data)
	if e != nil {
		return e
	}
	request := s.CompletedRequest
	if full {
		request = s.Requested
	}
	_, e = tx.Exec(`UPDATE catalog_sync_state SET pending=?,pending_generation=?,pending_request=?,pending_full=? WHERE id=1`, payload, s.Generation, request, full)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func catalogSyncCycle(ctx context.Context, now time.Time) error {
	catalogWorkerMu.Lock()
	defer catalogWorkerMu.Unlock()
	s, e := readCatalogSyncState()
	if e != nil {
		return e
	}
	if s.Blocked || now.Unix() < s.RetryAt {
		return nil
	}
	if len(s.Pending) == 0 {
		full := s.Requested > s.CompletedRequest || now.Unix() >= s.NextFull
		if !full && !catalogBatchDue(s, now) {
			return nil
		}
		if full {
			if e = ScanAndUpdateCatalog(); e != nil {
				return catalogSyncFailure(s, now, e, false)
			}
		}
		if e = captureCatalogSnapshot(now, full); e != nil {
			return catalogSyncFailure(s, now, e, false)
		}
		s, e = readCatalogSyncState()
		if e != nil {
			return e
		}
	}
	if _, e = checkCatalogRoots(true); e != nil {
		return catalogSyncFailure(s, now, e, false)
	}
	s, e = prepareOrderedCatalog(ctx, s)
	if e != nil {
		return catalogSyncFailure(s, now, e, false)
	}
	auth, e := transmitCatalogSnapshot(ctx, s.Pending, s)
	if e != nil {
		var stale catalogStale
		if errors.As(e, &stale) {
			if e = reconcileStaleCatalog(ctx, s); e == nil {
				return nil
			}
		}
		return catalogSyncFailure(s, now, e, auth)
	}
	if s.Uncertain && s.PendingSequence == 0 {
		return catalogSyncFailure(s, now, catalogDeliveryUncertain{}, false)
	}
	catalogMutationMu.Lock()
	_, e = CatalogDB.Exec(`UPDATE catalog_sync_state SET acknowledged=?,completed_request=MAX(completed_request,?),
 first_dirty=CASE WHEN generation=? THEN 0 ELSE first_dirty END,last_dirty=CASE WHEN generation=? THEN 0 ELSE last_dirty END,
 last_full=CASE WHEN ? THEN ? ELSE last_full END,next_full=CASE WHEN ? THEN ? ELSE next_full END,
 uncertain=0,pending_sequence=0,pending_hash='',pending=NULL,pending_generation=0,pending_request=0,pending_full=0,retry_at=0,failures=0,last_error='' WHERE id=1`,
		s.PendingGeneration, s.PendingRequest, s.PendingGeneration, s.PendingGeneration, s.PendingFull, now.Unix(), s.PendingFull, now.Add(24*time.Hour).Unix())
	catalogMutationMu.Unlock()
	if e != nil {
		return e
	}
	logMessage(LogLevelInfo, "CatalogSync", "Full snapshot acknowledged: generation %d, %d bytes, reconciliation=%t", s.PendingGeneration, len(s.Pending), s.PendingFull)
	return nil
}
func catalogSyncFailure(s catalogSyncState, now time.Time, cause error, auth bool) error {
	var authentication catalogAuth
	auth = auth || errors.As(cause, &authentication)
	var uncertain catalogDeliveryUncertain
	deliveryUncertain := s.PendingSequence == 0 && errors.As(cause, &uncertain)
	var hold catalogHold
	blocked := errors.As(cause, &hold)
	failures := s.Failures + 1
	delay := 30 * time.Second
	for i := 1; i < failures && delay < 30*time.Minute; i++ {
		delay *= 2
	}
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	delay = time.Duration(float64(delay) * (0.8 + rand.Float64()*0.4))
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	message := cause.Error()
	if auth {
		delay = 30 * time.Minute
		message = "catalog authentication failed; configure CATALOG_API_KEY (required for ordered sync), or BOT_SERVICE_TOKEN for legacy sync"
	}
	_, e := CatalogDB.Exec(`UPDATE catalog_sync_state SET retry_at=?,failures=?,last_error=?,uncertain=MAX(uncertain,?),blocked=? WHERE id=1`, now.Add(delay).Unix(), failures, message, deliveryUncertain, blocked)
	if e != nil {
		return e
	}
	if blocked {
		logMessage(LogLevelWarn, "CatalogSync", "Catalog held for operator repair: %s", message)
	} else {
		logMessage(LogLevelWarn, "CatalogSync", "Catalog retained; retry in %s: %s", delay.Round(time.Second), message)
	}
	return cause
}
func transmitCatalogSnapshot(ctx context.Context, payload []byte, identity ...catalogSyncState) (authFailure bool, err error) {
	key := catalogAPIKey()
	if key == "" {
		return true, fmt.Errorf("catalog bearer credential unavailable")
	}
	var ordered catalogSyncState
	if len(identity) > 0 {
		ordered = identity[0]
	}
	if ordered.PendingSequence > 0 && os.Getenv("CATALOG_API_KEY") == "" {
		return true, catalogAuth{}
	}
	if len(payload) > 32<<20 {
		return false, catalogHold{"catalog snapshot exceeds 32 MiB limit"}
	}
	var expected CatalogSyncData
	if e := json.Unmarshal(payload, &expected); e != nil {
		return false, fmt.Errorf("saved catalog snapshot unreadable; preserve for repair")
	}
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	if _, e := gz.Write(payload); e != nil {
		return false, e
	}
	if e := gz.Close(); e != nil {
		return false, e
	}
	request, e := http.NewRequestWithContext(ctx, "POST", RemoteServer+"/catalogUpdate", &body)
	if e != nil {
		return false, fmt.Errorf("invalid catalog API configuration")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("Authorization", "Bearer "+key)
	if ordered.PendingSequence > 0 {
		request.Header.Set("X-Catalog-Source", ordered.Source)
		request.Header.Set("X-Catalog-Sequence", strconv.FormatInt(ordered.PendingSequence, 10))
		request.Header.Set("X-Catalog-SHA256", ordered.PendingHash)
	}
	client := catalogHTTPClient()
	var wrote atomic.Bool
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{WroteHeaders: func() { wrote.Store(true) }}))
	response, e := client.Do(request)
	if e != nil {
		if !wrote.Load() {
			return false, fmt.Errorf("catalog connection failed before request delivery")
		}
		if ordered.PendingSequence > 0 {
			return false, fmt.Errorf("ordered catalog response lost; retry saved snapshot identity")
		}
		return false, catalogDeliveryUncertain{}
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return true, fmt.Errorf("catalog authentication refused")
	}
	if ordered.PendingSequence > 0 && (response.StatusCode == 400 || response.StatusCode == 413) {
		return false, catalogHold{fmt.Sprintf("catalog payload rejected with HTTP %d; repair before requesting reconciliation", response.StatusCode)}
	}
	if ordered.PendingSequence > 0 && response.StatusCode == 409 {
		var result struct {
			Error string `json:"error"`
		}
		if e := decodeCatalogJSON(response, &result); e != nil {
			return false, e
		}
		if result.Error == "stale_catalog_sequence" {
			return false, catalogStale{}
		}
		return false, catalogHold{"catalog identity conflict; preserve snapshot and restore original producer/sequence"}
	}
	if response.StatusCode != 200 {
		return false, fmt.Errorf("catalog API returned HTTP %d", response.StatusCode)
	}
	content, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if e != nil || content != "application/json" {
		return false, fmt.Errorf("catalog API did not return JSON acknowledgement")
	}
	var ack struct {
		Source   string `json:"source_id"`
		Sequence int64  `json:"sequence"`
		Hash     string `json:"sha256"`
		Replayed *bool  `json:"replayed"`
		Message  string `json:"message"`
		Shows    *int   `json:"shows"`
		Episodes *int   `json:"episodes"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 65536))
	if e = decoder.Decode(&ack); e != nil || ack.Message != "Catalog updated successfully" || ack.Shows == nil || ack.Episodes == nil || *ack.Shows != expected.Statistics.TotalShows || *ack.Episodes != expected.Statistics.TotalEpisodes {
		return false, fmt.Errorf("catalog acknowledgement invalid or mismatched")
	}
	if decoder.Decode(new(interface{})) != io.EOF {
		return false, fmt.Errorf("catalog acknowledgement has trailing data")
	}
	if ordered.PendingSequence > 0 && (ack.Source != ordered.Source || ack.Sequence != ordered.PendingSequence || ack.Hash != ordered.PendingHash || ack.Replayed == nil) {
		return false, fmt.Errorf("ordered catalog acknowledgement identity missing or mismatched")
	}
	return false, nil
}
