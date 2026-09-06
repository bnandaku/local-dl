package main

import (
	"encoding/json"
	"os"
)

// Missing Put.io objects are transport/history failures, not proof that a
// torrent's content was bad. Never permanently blacklist on this evidence.
func detectDownloadFailure(path string) error {
	info, e := os.Stat(path)
	if e != nil {
		return e
	}
	if info.Size() > 65536 {
		return nil
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var payload struct {
		Type string `json:"error_type"`
	}
	if json.Unmarshal(b, &payload) == nil && payload.Type == "FileNotFound" {
		return failedDownloadError{"file_not_found"}
	}
	return nil
}
