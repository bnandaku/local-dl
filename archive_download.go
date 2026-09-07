package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func archiveWorkdir(id string) (string, error) {
	root, e := filepath.Abs(archiveStagingRoot())
	if e != nil {
		return "", e
	}
	if e = musicMkdirAll(root); e != nil {
		return "", e
	}
	if e = safeArchiveDestination(root); e != nil {
		return "", e
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
	work := filepath.Join(root, key)
	if e = os.RemoveAll(work); e != nil {
		return "", e
	}
	if e = os.Mkdir(work, 0700); e != nil {
		return "", e
	}
	return work, nil
}
func downloadArchiveVolumes(ctx context.Context, a archiveSet, dir string) ([]string, error) {
	paths := make([]string, 0, len(a.Volumes))
	var total int64
	for _, v := range a.Volumes {
		if v.Cleanup != "pending" {
			return nil, fmt.Errorf("cannot extract an archive after partial cleanup")
		}
		if v.Size > 100<<30-total {
			return nil, archiveFailure(archiveIntrinsic, "preflight", errArchiveLimit)
		}
		total += v.Size
		paths = append(paths, filepath.Join(dir, v.Filename))
	}
	ordered, e := normalizeVolumes(paths)
	if e != nil {
		return nil, e
	}
	for _, v := range a.Volumes {
		if e := downloadArchiveVolume(ctx, v, filepath.Join(dir, v.Filename)); e != nil {
			return nil, e
		}
	}
	return ordered, nil
}
func downloadArchiveVolume(ctx context.Context, v archiveVolume, path string) error {
	u, e := url.Parse(v.URL)
	if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return archiveFailure(archiveOperational, "network", fmt.Errorf("invalid archive download URL"))
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", v.URL, nil)
	if e != nil {
		return archiveFailure(archiveOperational, "network", fmt.Errorf("invalid archive request"))
	}
	client, e := archiveHTTPClient()
	if e != nil {
		return archiveFailure(archiveOperational, "network", e)
	}
	defer client.CloseIdleConnections()
	resp, e := client.Do(req)
	if e != nil {
		return archiveFailure(archiveOperational, "network", fmt.Errorf("archive download unavailable"))
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return failedDownloadError{"file_not_found"}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return archiveFailure(archiveOperational, "authentication", fmt.Errorf("archive download authorization failed"))
	}
	if resp.StatusCode != 200 {
		return archiveFailure(archiveOperational, "network", fmt.Errorf("archive download status mismatch"))
	}
	// Put.io can return a small JSON FileNotFound body with HTTP 200. Inspect
	// a bounded prefix before rejecting its size, including chunked responses.
	body := bufio.NewReaderSize(resp.Body, 65537)
	prefix, _ := body.Peek(65537)
	var payload struct {
		Type string `json:"error_type"`
	}
	if len(prefix) <= 65536 && json.Unmarshal(prefix, &payload) == nil && payload.Type == "FileNotFound" {
		return failedDownloadError{"file_not_found"}
	}
	if resp.ContentLength >= 0 && resp.ContentLength != v.Size {
		return archiveFailure(archiveOperational, "network", fmt.Errorf("archive download status or size mismatch"))
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return archiveFailure(archiveOperational, "storage", e)
	}
	defer f.Close()
	writer := &archiveCountWriter{w: f, max: v.Size}
	n, e := io.Copy(writer, io.LimitReader(body, v.Size+1))
	if writer.err != nil {
		return archiveFailure(archiveOperational, "storage", fmt.Errorf("archive staging write failed"))
	}
	if e != nil || n != v.Size {
		return archiveFailure(archiveOperational, "network", fmt.Errorf("archive volume incomplete"))
	}
	if e = f.Sync(); e != nil {
		return archiveFailure(archiveOperational, "storage", e)
	}
	if e = f.Close(); e != nil {
		return archiveFailure(archiveOperational, "storage", e)
	}
	return detectDownloadFailure(path)
}
func archiveExtractionManifest(root string, paths []string) ([]archiveDeclaredOutput, map[string]string, error) {
	result := []archiveDeclaredOutput{}
	sources := map[string]string{}
	seen := map[string]bool{}
	stage := ""
	for _, path := range paths {
		relative, e := filepath.Rel(root, path)
		if e != nil {
			return nil, nil, e
		}
		parts := strings.SplitN(relative, string(filepath.Separator), 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[0], ".rar-extracted-") || !safeArchiveName(parts[1]) || len(parts[1]) > 1024 || seen[strings.ToLower(parts[1])] {
			return nil, nil, archiveFailure(archiveIntrinsic, "preflight", fmt.Errorf("unsafe extracted output manifest"))
		}
		if stage != "" && stage != parts[0] {
			return nil, nil, fmt.Errorf("inconsistent extraction roots")
		}
		stage = parts[0]
		info, e := os.Lstat(path)
		if e != nil {
			return nil, nil, e
		}
		if !info.Mode().IsRegular() {
			return nil, nil, archiveFailure(archiveIntrinsic, "preflight", fmt.Errorf("nonregular extracted file"))
		}
		seen[strings.ToLower(parts[1])] = true
		result = append(result, archiveDeclaredOutput{parts[1], info.Size()})
		sources[parts[1]] = path
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, sources, nil
}
