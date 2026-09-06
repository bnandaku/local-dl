package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func realRARTool(t *testing.T) string {
	t.Helper()
	name := os.Getenv("UNRAR_TEST_TOOL")
	if name == "" {
		name = "unrar"
	}
	p, e := exec.LookPath(name)
	if e != nil {
		t.Skip("install unrar or set UNRAR_TEST_TOOL to run real archive fixtures")
	}
	return p
}
func realVolumes(t *testing.T, dir string, pattern string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), pattern) {
			p, _ := filepath.Abs(filepath.Join(dir, e.Name()))
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
func TestRealMultipartRARExtractsExactSyntheticPayload(t *testing.T) {
	tool := realRARTool(t)
	old := archiveTool
	archiveTool = tool
	defer func() { archiveTool = old }()
	vols := realVolumes(t, "testdata/rar/modern-valid", "modern.part")
	got, err := extractRAR(context.Background(), vols, t.TempDir(), archiveLimits{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("files=%v", got)
	}
	var payload, captions bool
	for _, p := range got {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		if strings.HasSuffix(p, "payload.mp3") {
			payload = len(b) == 12000
			for _, x := range b {
				payload = payload && x == 0
			}
		}
		if strings.HasSuffix(p, "captions.srt") {
			captions = string(b) == "subtitle fixture\n"
		}
	}
	if !payload || !captions {
		t.Fatalf("synthetic payload mismatch: %v", got)
	}
}
func TestRealMultipartRARMissingMiddleIsIntrinsic(t *testing.T) {
	tool := realRARTool(t)
	old := archiveTool
	archiveTool = tool
	defer func() { archiveTool = old }()
	vols := realVolumes(t, "testdata/rar/modern-valid", "modern.part")
	vols = append(vols[:1], vols[2:]...)
	_, err := extractRAR(context.Background(), vols, t.TempDir(), archiveLimits{Timeout: time.Minute})
	if !isArchiveIntrinsic(err) {
		t.Fatalf("err=%v", err)
	}
}
func TestRealMultipartRARDamagedVolumeIsIntrinsic(t *testing.T) {
	tool := realRARTool(t)
	old := archiveTool
	archiveTool = tool
	defer func() { archiveTool = old }()
	vols := realVolumes(t, "testdata/rar/modern-corrupt", "modern.part")
	_, err := extractRAR(context.Background(), vols, t.TempDir(), archiveLimits{Timeout: time.Minute})
	if !isArchiveIntrinsic(err) {
		t.Fatalf("err=%v", err)
	}
}
func TestRealMultipartRARPasswordArchiveIsIntrinsic(t *testing.T) {
	tool := realRARTool(t)
	old := archiveTool
	archiveTool = tool
	defer func() { archiveTool = old }()
	vols := realVolumes(t, "testdata/rar/modern-password", "password.part")
	_, err := extractRAR(context.Background(), vols, t.TempDir(), archiveLimits{Timeout: time.Minute})
	if !isArchiveIntrinsic(err) {
		t.Fatalf("err=%v", err)
	}
}
func TestRealLegacyNamedVolumesExtract(t *testing.T) {
	tool := realRARTool(t)
	old := archiveTool
	archiveTool = tool
	defer func() { archiveTool = old }()
	vols := []string{"testdata/rar/legacy/archive.rar", "testdata/rar/legacy/archive.r00", "testdata/rar/legacy/archive.r01", "testdata/rar/legacy/archive.r02"}
	for i := range vols {
		vols[i], _ = filepath.Abs(vols[i])
	}
	_, err := extractRAR(context.Background(), vols, t.TempDir(), archiveLimits{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
}
