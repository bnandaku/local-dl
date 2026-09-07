package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

type archiveVolume struct {
	FileID   int64  `json:"file_id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
	Cleanup  string `json:"cleanup"`
}
type archiveOutput struct {
	ID          string `json:"output_id"`
	Path        string `json:"relative_path"`
	Size        int64  `json:"size"`
	Required    bool   `json:"required"`
	Status      string `json:"status"`
	SHA256      string `json:"sha256"`
	LibraryType string `json:"library_type"`
	LibraryKey  string `json:"library_key"`
}
type archiveSet struct {
	ID              string          `json:"archive_id"`
	AttemptID       int64           `json:"attempt_id"`
	RequestID       string          `json:"request_id"`
	MediaType       string          `json:"media_type"`
	Name            string          `json:"name"`
	State           string          `json:"state"`
	Reason          string          `json:"reason"`
	Complete        bool            `json:"manifest_complete"`
	OutputsDeclared bool            `json:"outputs_declared"`
	Volumes         []archiveVolume `json:"volumes"`
	Outputs         []archiveOutput `json:"outputs"`
}
type archiveReport struct {
	EventID     string `json:"event_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Published   bool   `json:"published,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	LibraryType string `json:"library_type,omitempty"`
	LibraryKey  string `json:"library_key,omitempty"`
}
type archiveDeclaredOutput struct {
	Path string `json:"relative_path"`
	Size int64  `json:"size"`
}

func archiveURL(id string) string { return "/api/v1/archives/" + url.PathEscape(id) }
func safeArchiveID(id string) bool {
	return id != "" && len(id) <= 160 && !strings.ContainsAny(id, "/\\?#\x00\r\n") && id != "." && id != ".."
}
func validateArchiveSet(a archiveSet) error {
	if !safeArchiveID(a.ID) || !safeArchiveID(a.RequestID) || a.AttemptID <= 0 || !a.Complete || len(a.Volumes) == 0 || len(a.Volumes) > 1000 {
		return fmt.Errorf("invalid or incomplete archive identity")
	}
	switch a.State {
	case "pending", "extracting", "failed", "bad", "validated":
	default:
		return fmt.Errorf("unknown archive state")
	}
	ids := map[int64]bool{}
	names := map[string]bool{}
	for _, v := range a.Volumes {
		if v.FileID <= 0 || ids[v.FileID] || v.Size < 0 || !safeArchiveName(v.Filename) || filepath.Base(v.Filename) != v.Filename || !isRARVolume(v.Filename) || names[strings.ToLower(v.Filename)] {
			return fmt.Errorf("unsafe archive volume manifest")
		}
		ids[v.FileID] = true
		names[strings.ToLower(v.Filename)] = true
		switch v.Cleanup {
		case "pending", "purged", "discarded":
		default:
			return fmt.Errorf("unknown volume cleanup state")
		}
	}
	paths := map[string]bool{}
	outIDs := map[string]bool{}
	for _, o := range a.Outputs {
		if !safeArchiveID(o.ID) || !safeArchiveName(o.Path) || len(o.Path) > 1024 || o.Size < 0 || outIDs[o.ID] || paths[strings.ToLower(o.Path)] {
			return fmt.Errorf("unsafe archive output manifest")
		}
		outIDs[o.ID] = true
		paths[strings.ToLower(o.Path)] = true
	}
	return nil
}
func requireArchiveContract() error {
	if e := requireRecoveryContract(); e != nil {
		return e
	}
	var c struct {
		Features []string `json:"features"`
	}
	if _, e := botLinkCall("GET", "/api/v1/capabilities", nil, &c); e != nil {
		return e
	}
	for _, f := range c.Features {
		if f == "archive_sets_v1" {
			return nil
		}
	}
	return fmt.Errorf("bot archive_sets_v1 capability unavailable")
}
func fetchArchive(id string) (archiveSet, error) {
	var a archiveSet
	_, e := botLinkCall("GET", archiveURL(id), nil, &a)
	if e == nil {
		e = validateArchiveSet(a)
	}
	if e == nil && a.ID != id {
		e = fmt.Errorf("archive identity changed")
	}
	return a, e
}
func sameArchiveIdentity(before, after archiveSet) bool {
	if before.ID != after.ID || before.AttemptID != after.AttemptID || before.RequestID != after.RequestID || before.MediaType != after.MediaType || len(before.Volumes) != len(after.Volumes) {
		return false
	}
	if before.OutputsDeclared {
		if !after.OutputsDeclared || len(before.Outputs) != len(after.Outputs) {
			return false
		}
		outputs := map[string]archiveOutput{}
		for _, o := range before.Outputs {
			outputs[o.ID] = o
		}
		for _, o := range after.Outputs {
			prior, ok := outputs[o.ID]
			if !ok || prior.Path != o.Path || prior.Size != o.Size || prior.Required != o.Required {
				return false
			}
		}
	}
	old := map[int64]archiveVolume{}
	for _, v := range before.Volumes {
		old[v.FileID] = v
	}
	for _, v := range after.Volumes {
		p, ok := old[v.FileID]
		if !ok || p.Filename != v.Filename || p.Size != v.Size {
			return false
		}
	}
	return true
}
func archiveMutation(a archiveSet, path string, body interface{}) (archiveSet, error) {
	var result archiveSet
	_, e := botLinkCall("POST", path, body, &result)
	if e == nil {
		e = validateArchiveSet(result)
	}
	if e == nil && !sameArchiveIdentity(a, result) {
		e = fmt.Errorf("archive mutation returned a different source manifest")
	}
	return result, e
}
func declareArchiveFiles(a archiveSet, outputs []archiveDeclaredOutput) (archiveSet, error) {
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Path < outputs[j].Path })
	body := struct {
		Event    string                  `json:"event_id"`
		Complete bool                    `json:"complete"`
		Outputs  []archiveDeclaredOutput `json:"outputs"`
	}{"local-dl:" + a.ID + ":manifest", true, outputs}
	b, e := json.Marshal(body)
	if e != nil {
		return a, e
	}
	if len(b) > 16384 || len(outputs) > 100 {
		return a, archiveFailure(archiveIntrinsic, "preflight", errArchiveLimit)
	}
	result, e := archiveMutation(a, archiveURL(a.ID)+"/outputs", body)
	if e != nil {
		return a, e
	}
	if !result.OutputsDeclared || len(outputs) != len(result.Outputs) {
		return a, fmt.Errorf("bot returned an incomplete output manifest")
	}
	expected := map[string]int64{}
	for _, o := range outputs {
		expected[o.Path] = o.Size
	}
	for _, o := range result.Outputs {
		size, ok := expected[o.Path]
		if !ok || size != o.Size {
			return a, fmt.Errorf("bot output manifest differs from extraction")
		}
	}
	return result, nil
}
