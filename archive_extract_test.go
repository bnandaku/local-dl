package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRARVolumeOrderingAndCompleteness(t *testing.T) {
	got, e := normalizeVolumes([]string{"/tmp/set.part2.rar", "/tmp/set.part1.rar"})
	if e != nil || !reflect.DeepEqual(got, []string{"/tmp/set.part1.rar", "/tmp/set.part2.rar"}) {
		t.Fatalf("%v %v", got, e)
	}
	got, e = normalizeVolumes([]string{"/tmp/set.r00", "/tmp/set.rar"})
	if e != nil || got[0] != "/tmp/set.rar" {
		t.Fatalf("%v %v", got, e)
	}
	for _, v := range [][]string{{}, {"relative.rar"}, {"/tmp/set.part2.rar"}, {"/tmp/set.rar", "/tmp/set.r01"}, {"/tmp/set.rar", "/tmp/other.r00"}, {"/tmp/set.rar", "/tmp/set.rar"}, {"/tmp/set.part1.rar", "/tmp/set.r00"}, {"/tmp/set.rar", "/other/set.r00"}} {
		if _, e := normalizeVolumes(v); !isArchiveIntrinsic(e) {
			t.Fatalf("accepted incomplete/mixed volumes: %v (%v)", v, e)
		}
	}
}
func TestRARManifestRejectsUnsafeEntries(t *testing.T) {
	for _, name := range []string{"../escape.mp3", "/escape.mp3", "x/../../escape.mp3", "x\\escape.mp3", "*.mp3", "-switch", "x/./a.mp3"} {
		listing := fmt.Sprintf("Name: %s\nType: File\nSize: 10\nAttributes: -rw-r--r--\n", name)
		if _, e := parseRARListing([]byte(listing), archiveLimits{MaxEntries: 10, MaxExpandedBytes: 100}); !isArchiveIntrinsic(e) {
			t.Fatalf("accepted %q", name)
		}
	}
	for _, kind := range []string{"Unix symbolic link", "Unix hard link", "Device"} {
		listing := fmt.Sprintf("Name: song.mp3\nType: %s\nSize: 10\nAttributes: -rw-r--r--\n", kind)
		if _, e := parseRARListing([]byte(listing), archiveLimits{MaxEntries: 10, MaxExpandedBytes: 100}); !isArchiveIntrinsic(e) {
			t.Fatalf("accepted %s", kind)
		}
	}
	good := "Name: song.mp3\nType: File\nSize: 10\nAttributes: -rw-r--r--\n"
	for _, listing := range []string{good + good, strings.ReplaceAll(good, "Size: 10", "Size: nope"), good + "Ratio: -->\n", strings.ReplaceAll(good, "Size: 10", "Size: 101")} {
		if _, e := parseRARListing([]byte(listing), archiveLimits{MaxEntries: 1, MaxExpandedBytes: 100}); !isArchiveIntrinsic(e) {
			t.Fatalf("accepted invalid listing %s", listing)
		}
	}
}
func TestRARMissingToolAndDiskFailureStayOperational(t *testing.T) {
	old := archiveTool
	archiveTool = "/no/unrar"
	defer func() { archiveTool = old }()
	_, e := extractRAR(context.Background(), []string{filepath.Join(t.TempDir(), "set.rar")}, t.TempDir(), archiveLimits{})
	var ae *archiveError
	if !errors.As(e, &ae) || ae.Kind != archiveOperational || ae.Op != "tool" {
		t.Fatalf("%v", e)
	}
	diskErr := errors.New("disk full")
	w := &archiveCountWriter{w: archiveFailWriter{diskErr}, max: 100}
	_, e = w.Write([]byte("data"))
	if !errors.Is(e, diskErr) || !errors.Is(w.err, diskErr) {
		t.Fatal("lost storage failure")
	}
}

type archiveFailWriter struct{ err error }

func (w archiveFailWriter) Write([]byte) (int, error) { return 0, w.err }
func TestRARRejectsSymlinkDestination(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if e := os.Symlink(t.TempDir(), link); e != nil {
		t.Fatal(e)
	}
	if safeArchiveDestination(link) == nil {
		t.Fatal("symlink destination accepted")
	}
}

func TestRARListingOverflowIsIntrinsic(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "unrar-test")
	if e := os.WriteFile(tool, []byte("#!/bin/sh\nhead -c 9000000 /dev/zero\n"), 0700); e != nil {
		t.Fatal(e)
	}
	_, e := listRAR(context.Background(), tool, "unused.rar", archiveLimits{MaxEntries: 10, MaxExpandedBytes: 100})
	if !isArchiveIntrinsic(e) || !errors.Is(e, errArchiveLimit) {
		t.Fatalf("overflow classified incorrectly: %v", e)
	}
}
