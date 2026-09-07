package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"time"
)

type archiveOutputError struct {
	ID  string
	Err error
}

func (e archiveOutputError) Error() string { return e.Err.Error() }
func (e archiveOutputError) Unwrap() error { return e.Err }
func archiveEvent(a archiveSet, action, identity string) string {
	return fmt.Sprintf("local-dl:archive:%s:%x", action, sha256.Sum256([]byte(a.ID+"\x00"+identity)))
}
func archiveSourceLink(a archiveSet) recoveryLink {
	return recoveryLink{ID: a.AttemptID, Group: a.RequestID, Status: "validated"}
}
func archiveOutputReport(a archiveSet, id string, r archiveReport) (archiveSet, error) {
	return archiveMutation(a, archiveURL(a.ID)+"/outputs/"+url.PathEscape(id)+"/report", r)
}
func pollArchiveJobs(s *archiveWorkerState) error {
	after := ""
	for pageNum := 0; pageNum < 1000; pageNum++ {
		var page struct {
			Items []archiveSet `json:"items"`
			Next  string       `json:"next_after"`
		}
		if _, e := botLinkCall("GET", "/api/v1/archives?limit=50&after="+url.QueryEscape(after), nil, &page); e != nil {
			return e
		}
		if page.Items == nil {
			return fmt.Errorf("invalid archive queue page")
		}
		for _, a := range page.Items {
			if e := validateArchiveSet(a); e != nil {
				return e
			}
			if a.ID <= after {
				return fmt.Errorf("archive queue did not advance")
			}
			j := s.Jobs[a.ID]
			if j == nil {
				s.Jobs[a.ID] = &archiveJob{ID: a.ID, AttemptID: a.AttemptID, RequestID: a.RequestID, Receipts: map[string]archiveReceipt{}}
			}
			if j != nil && (j.AttemptID != a.AttemptID || j.RequestID != a.RequestID) {
				return fmt.Errorf("archive job ownership changed")
			}
		}
		if e := saveArchiveState(*s); e != nil {
			return e
		}
		if page.Next == "" {
			return nil
		}
		if page.Next <= after {
			return fmt.Errorf("archive queue pagination did not advance")
		}
		after = page.Next
	}
	return fmt.Errorf("archive queue page limit exceeded")
}
func runArchiveWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		archiveWorkerCycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func archiveWorkerCycle(ctx context.Context) {
	s, e := loadArchiveState()
	if e != nil {
		logMessage(LogLevelError, "Archive", "Cannot read archive jobs; preserving state for repair")
		return
	}
	e = requireArchiveContract()
	if e == nil {
		e = pollArchiveJobs(&s)
	}
	if e == nil {
		keys := make([]string, 0, len(s.Jobs))
		for k, j := range s.Jobs {
			if !j.Done {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ctx.Err() != nil {
				return
			}
			j := s.Jobs[k]
			err := processArchiveJob(ctx, &s, j)
			j.LastError = ""
			j.Updated = time.Now().UTC()
			if err != nil {
				j.LastError = err.Error()
				logMessage(LogLevelWarn, "Archive", "Archive %s held: %v", j.ID, err)
			}
			if saveErr := saveArchiveState(s); saveErr != nil {
				e = saveErr
				break
			}
		}
	}
	s.LastChecked = time.Now().UTC()
	s.LastError = ""
	if e != nil {
		s.LastError = e.Error()
	}
	if e := saveArchiveState(s); e != nil {
		logMessage(LogLevelError, "Archive", "Cannot save archive jobs: %v", e)
	}
}
func processArchiveJob(ctx context.Context, s *archiveWorkerState, j *archiveJob) error {
	a, e := fetchArchive(j.ID)
	if e != nil {
		return e
	}
	if j.AttemptID != a.AttemptID || j.RequestID != a.RequestID {
		return fmt.Errorf("archive job mapping changed")
	}
	if j.Pending != nil {
		a, e = sendPendingArchiveFailure(a, s, j)
		if e != nil {
			return e
		}
	}
	if a.State == "bad" || (a.State == "failed" && a.Reason == "file_not_found") {
		return finishRejectedArchive(a, s, j)
	}
	if a.State == "validated" {
		if e := verifyArchivePublications(a, j); e != nil {
			var missing missingArchivePublication
			if !errors.As(e, &missing) {
				return e
			}
			for _, v := range a.Volumes {
				if v.Cleanup != "pending" {
					return e
				}
			}
			if e = restoreArchivePublications(ctx, a, s, j); e != nil {
				return e
			}
		}
		return finishPublishedArchive(a, s, j)
	}
	work, e := archiveWorkdir(a.ID)
	if e != nil {
		return handleArchiveFailure(a, s, j, archiveFailure(archiveOperational, "storage", e))
	}
	defer os.RemoveAll(work)
	outputs, sources, e := extractArchiveSources(ctx, a, work)
	if e != nil {
		return handleArchiveFailure(a, s, j, e)
	}

	a, e = declareArchiveFiles(a, outputs)
	if e != nil {
		var api *botAPIError
		if errors.As(e, &api) && api.Code == "no_required_outputs" {
			e = badMediaError{"no_media"}
		}
		return handleArchiveFailure(a, s, j, e)
	}
	prepared := []preparedArchiveOutput{}
	// Validate all required outputs before publishing any new siblings.
	for _, o := range a.Outputs {
		if !o.Required {
			continue
		}
		p, e := prepareArchiveOutput(a, o, sources[o.Path])
		if e != nil {
			return handleArchiveFailure(a, s, j, archiveOutputError{o.ID, e})
		}
		prepared = append(prepared, p)
	}
	if len(prepared) == 0 {
		return handleArchiveFailure(a, s, j, badMediaError{"no_media"})
	}
	for _, p := range prepared {
		r, exists := j.Receipts[p.Output.ID]
		if exists {
			if e := verifyArchiveReceipt(r); e != nil {
				return e
			}
			if r.SHA256 != p.Digest {
				return fmt.Errorf("archive output changed since publication")
			}
		}
		if !exists {
			r, e = publishArchivePrimary(p)
			if e != nil {
				return handleArchiveFailure(a, s, j, archiveFailure(archiveOperational, "storage", e))
			}
			j.Receipts[p.Output.ID] = r
			if e = saveArchiveState(*s); e != nil {
				return e
			}
		}
		report := archiveReport{Status: "validated", Published: true, SHA256: r.SHA256, LibraryType: r.LibraryType, LibraryKey: r.LibraryKey}
		report.EventID = archiveEvent(a, "publish", p.Output.ID+"\x00"+r.SHA256+"\x00"+r.LibraryType+"\x00"+r.LibraryKey)
		a, e = archiveOutputReport(a, p.Output.ID, report)
		if e != nil {
			return e
		}
	}
	// Every non-required file receives a skip receipt, including retained supports.
	supports := append([]archiveOutput(nil), a.Outputs...)
	for _, o := range supports {
		if o.Required {
			continue
		}
		if _, exists := j.Receipts[o.ID]; !exists {
			r, e := publishArchiveSupport(o, sources[o.Path], prepared, j.Receipts)
			if e != nil {
				return handleArchiveFailure(a, s, j, archiveFailure(archiveOperational, "storage", e))
			}
			if r != nil {
				j.Receipts[o.ID] = *r
				if e = saveArchiveState(*s); e != nil {
					return e
				}
			}
		}
		report := archiveReport{EventID: archiveEvent(a, "skip", o.ID), Status: "skipped"}
		a, e = archiveOutputReport(a, o.ID, report)
		if e != nil {
			return e
		}
	}
	if a.State != "validated" {
		return fmt.Errorf("archive outputs await validated server state")
	}
	return finishPublishedArchive(a, s, j)
}
func finishPublishedArchive(a archiveSet, s *archiveWorkerState, j *archiveJob) error {
	if e := verifyArchivePublications(a, j); e != nil {
		return e
	}

	for _, v := range a.Volumes {
		if v.Cleanup == "discarded" {
			return fmt.Errorf("validated archive has a discarded volume")
		}
		if v.Cleanup == "pending" {
			if e := cleanupRecoveryFile(archiveSourceLink(a), v.FileID, true, ""); e != nil {
				return e
			}
		}
	}
	if e := confirmRecovery(archiveSourceLink(a)); e != nil {
		return e
	}
	j.Done = true
	return saveArchiveState(*s)
}
func finishRejectedArchive(a archiveSet, s *archiveWorkerState, j *archiveJob) error {
	for _, v := range a.Volumes {
		if v.Cleanup == "purged" {
			return fmt.Errorf("rejected archive has a published source volume")
		}
		if v.Cleanup == "pending" {
			if e := cleanupRecoveryFile(archiveSourceLink(a), v.FileID, false, a.Reason); e != nil {
				return e
			}
		}
	}
	if e := retryRecoveryRequest(a.RequestID); e != nil {
		return e
	}
	j.Done = true
	return saveArchiveState(*s)
}
func archiveFailureReport(err error) (status, reason string) {
	var bad badMediaError
	if errors.As(err, &bad) {
		return "bad", bad.reason
	}
	var missing failedDownloadError
	if errors.As(err, &missing) {
		return "failed", missing.reason
	}
	var ae *archiveError
	if errors.As(err, &ae) {
		if ae.Kind == archiveIntrinsic {
			if ae.Op == "no_media" {
				return "bad", "no_media"
			}
			if ae.Op == "password" {
				return "bad", "archive_password_locked"
			}
			if ae.Op == "volumes" {
				return "bad", "archive_missing_volumes"
			}
			if ae.Op == "preflight" || ae.Op == "manifest" || errors.Is(err, errArchiveLimit) {
				return "bad", "archive_unsafe"
			}
			return "bad", "archive_corrupt"
		}
		switch ae.Op {
		case "tool":
			return "failed", "tool_missing"
		case "network", "authentication":
			return "failed", ae.Op
		case "storage", "staging", "destination", "output", "volume":
			return "failed", "storage"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "failed", "timeout"
	}
	return "failed", "storage"
}
func handleArchiveFailure(a archiveSet, s *archiveWorkerState, j *archiveJob, err error) error {
	var api *botAPIError
	if errors.As(err, &api) {
		return err
	} // API conflicts are not evidence against the archive.
	status, reason := archiveFailureReport(err)
	if codec, ok := codecRejection(err); ok {
		if e := requireBotCodecExclusion(codec); e != nil {
			return e
		}
		status, reason = "bad", "wrong_content"
	}
	output := ""
	var oe archiveOutputError
	if errors.As(err, &oe) {
		output = oe.ID
		for _, o := range a.Outputs {
			if o.ID == output && o.Status == "validated" {
				output = ""
				break
			}
		}
	}
	j.Pending = &archiveReport{EventID: archiveEvent(a, "failure", output+"\x00"+status+"\x00"+reason), Status: status, Reason: reason}
	j.PendingOutput = output
	if e := saveArchiveState(*s); e != nil {
		return e
	}
	updated, e := sendPendingArchiveFailure(a, s, j)
	if e != nil {
		return e
	}
	if status == "bad" || reason == "file_not_found" {
		return finishRejectedArchive(updated, s, j)
	}
	return fmt.Errorf("archive retained for operational repair: %s", reason)
}
func sendPendingArchiveFailure(a archiveSet, s *archiveWorkerState, j *archiveJob) (archiveSet, error) {
	var result archiveSet
	var e error
	if j.PendingOutput != "" {
		result, e = archiveOutputReport(a, j.PendingOutput, *j.Pending)
	} else {
		result, e = archiveMutation(a, archiveURL(a.ID)+"/report", *j.Pending)
	}
	if e != nil {
		return a, e
	}
	j.Pending = nil
	j.PendingOutput = ""
	return result, saveArchiveState(*s)
}
