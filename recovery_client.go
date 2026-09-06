package main

import (
	"fmt"
	"os"
	"strings"
)

type recoveryLink struct {
	ID         int64  `json:"id"`
	Group      string `json:"group_id"`
	Status     string `json:"status"`
	TransferID int64  `json:"transfer_id"`
}
type recoveryRequest struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	Attempts  int    `json:"attempts"`
	Limit     int    `json:"attempt_limit"`
	Latest    int64  `json:"latest_attempt_id"`
	Retryable bool   `json:"retryable"`
}
type recoveryState struct {
	Request  recoveryRequest `json:"request"`
	Attempts []recoveryLink  `json:"attempts"`
}
type recoveryFile struct {
	ID        int64  `json:"file_id"`
	Name      string `json:"filename"`
	Required  bool   `json:"required"`
	Report    string `json:"report"`
	Published bool   `json:"published"`
	Cleanup   string `json:"cleanup"`
}

func requireRecoveryContract() error {
	if os.Getenv("BOT_SERVICE_TOKEN") == "" {
		return fmt.Errorf("BOT_SERVICE_TOKEN is required for recovery")
	}
	var c struct {
		Service     string   `json:"service"`
		Contract    int      `json:"recovery_contract"`
		Limit       int      `json:"attempt_limit"`
		Permanent   bool     `json:"permanent_blacklist"`
		Publication bool     `json:"file_publication_required"`
		Features    []string `json:"features"`
	}
	if _, e := botLinkCall("GET", "/api/v1/capabilities", nil, &c); e != nil {
		return e
	}
	if c.Service != "putio-go-server" || c.Contract != 2 || c.Limit != 5 || !c.Permanent || !c.Publication {
		return fmt.Errorf("bot recovery contract 2 with five attempts and permanent blacklist required")
	}
	for _, required := range []string{"legacy_recovery", "failed_transfers", "scoped_discard", "durable_events", "request_reconciliation", "json_api_errors"} {
		found := false
		for _, feature := range c.Features {
			if feature == required {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("bot recovery feature unavailable: %s", required)
		}
	}
	return nil
}

func reportRecoveryFile(a recoveryLink, f linkFeedback) (recoveryLink, error) {
	body := map[string]interface{}{"file_id": f.FileID, "status": f.Status}
	if f.Reason != "" {
		body["reason"] = f.Reason
	}
	if f.Status == "validated" {
		body["published"] = true
	}
	var result recoveryLink
	_, e := botLinkCall("POST", fmt.Sprintf("/api/v1/links/%d/report", a.ID), body, &result)
	if e == nil && (result.ID != a.ID || result.Group == "") {
		e = fmt.Errorf("invalid bot file-report response")
	}
	return result, e
}
func cleanupRecoveryFile(a recoveryLink, file int64, success bool, reason string) error {
	action := "discard"
	if success {
		action = "cleanup"
		reason = "validated"
	}
	event := fmt.Sprintf("local-dl:%s:%d", action, file)
	var out struct {
		Event   string `json:"event_id"`
		Attempt int64  `json:"attempt_id"`
		File    int64  `json:"file_id"`
		Status  string `json:"status"`
	}
	_, e := botLinkCall("POST", fmt.Sprintf("/api/v1/links/%d/%s", a.ID, action), map[string]interface{}{"event_id": event, "file_id": file, "reason": reason}, &out)
	if e == nil && (out.Event != event || out.Attempt != a.ID || out.File != file || out.Status != "done") {
		return fmt.Errorf("invalid scoped cleanup receipt")
	}
	return e
}

// Cleanup only explicit non-media leaves. Archives are never treated as garbage:
// their source volumes must survive until archive-set validation is complete.
func cleanupUnwantedFiles(a recoveryLink) error {
	var page struct {
		Items []recoveryFile `json:"items"`
	}
	if _, e := botLinkCall("GET", fmt.Sprintf("/api/v1/links/%d/files", a.ID), nil, &page); e != nil {
		return e
	}
	if page.Items == nil {
		return fmt.Errorf("invalid attempt file manifest")
	}
	for _, f := range page.Items {
		if f.ID <= 0 || f.Required || f.Cleanup != "pending" || f.Published || mediaKind(f.Name) != "" || isRARVolume(f.Name) {
			continue
		}
		if _, e := reportRecoveryFile(a, linkFeedback{FileID: f.ID, Status: "skipped"}); e != nil {
			return e
		}
		if e := cleanupRecoveryFile(a, f.ID, false, "supporting_file"); e != nil {
			return e
		}
	}
	return nil
}

func confirmRecovery(a recoveryLink) error {
	if a.Status != "validated" && a.Status != "completed" {
		return nil
	}
	var out struct {
		ID int64 `json:"confirmed_id"`
	}
	status, e := botLinkCall("POST", fmt.Sprintf("/api/v1/links/%d/confirm", a.ID), map[string]interface{}{}, &out)
	if status == 409 {
		return nil
	} // Other members/attempts still need scoped cleanup.
	if e == nil && out.ID != a.ID {
		return fmt.Errorf("invalid confirmation receipt")
	}
	return e
}

func recoverPublishedFile(record musicImportRecord) (bool, error) {
	if os.Getenv("BOT_SERVICE_TOKEN") == "" {
		return false, nil
	}
	if e := requireRecoveryContract(); e != nil {
		return true, e
	}
	var a recoveryLink
	code, e := botLinkCall("GET", fmt.Sprintf("/api/v1/link-files/%d", record.FileID), nil, &a)
	if code == 404 && isBotNotFound(e) {
		return false, nil
	} // Explicitly untracked historical success only.
	if e != nil {
		return true, e
	}
	if a.ID <= 0 || a.Group == "" {
		return true, fmt.Errorf("invalid file history mapping")
	}
	digest, e := musicFileDigest(record.Path)
	if e != nil || digest != record.SHA256 {
		return true, fmt.Errorf("published media is missing or changed")
	}
	a, e = reportRecoveryFile(a, linkFeedback{FileID: record.FileID, Status: "validated"})
	if e != nil {
		return true, e
	}
	if e = cleanupRecoveryFile(a, record.FileID, true, ""); e != nil {
		return true, e
	}
	if e = cleanupUnwantedFiles(a); e != nil {
		return true, e
	}
	return true, confirmRecovery(a)
}

func retryRecoveryRequest(group string) error {
	if group == "" || strings.ContainsAny(group, "/\\?#") {
		return fmt.Errorf("invalid recovery request ID")
	}
	var state recoveryState
	if _, e := botLinkCall("GET", "/api/v1/recovery/requests/"+group, nil, &state); e != nil {
		return e
	}
	r := state.Request
	if r.ID != group || r.Limit != 5 || r.Attempts < 0 || r.Attempts > 5 {
		return fmt.Errorf("invalid recovery request state")
	}
	switch r.State {
	case "completed", "exhausted", "queued", "validated":
		return nil
	case "submitting", "uncertain":
		return fmt.Errorf("submission requires reconciliation; no new candidate submitted")
	case "pending", "failed":
		if !r.Retryable {
			return fmt.Errorf("recovery request requires operational repair")
		}
	default:
		return fmt.Errorf("unknown recovery request state")
	}
	var out recoveryRequest
	_, e := botLinkCall("POST", "/api/v1/recovery/requests/"+group+"/retry", map[string]interface{}{}, &out)
	if e == nil && out.ID != group {
		return fmt.Errorf("invalid recovery retry response")
	}
	return e
}

func discardFailedTransfer(a recoveryLink, transfer int64) error {
	event := fmt.Sprintf("local-dl:discard-transfer:%d", transfer)
	var out struct {
		Event    string `json:"event_id"`
		Attempt  int64  `json:"attempt_id"`
		Transfer int64  `json:"transfer_id"`
		Status   string `json:"status"`
	}
	_, e := botLinkCall("POST", fmt.Sprintf("/api/v1/links/%d/discard", a.ID), map[string]interface{}{"event_id": event, "transfer_id": transfer, "reason": "transfer_failed"}, &out)
	if e == nil && (out.Event != event || out.Attempt != a.ID || out.Transfer != transfer || out.Status != "done") {
		return fmt.Errorf("invalid transfer discard receipt")
	}
	return e
}
