package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type missingArchivePublication struct{ Path string }

func (e missingArchivePublication) Error() string {
	return "published archive file is missing; source retained"
}
func archiveOutputReceipt(a archiveSet, o archiveOutput) (archiveReceipt, error) {
	item, e := archivePrimaryItem(a, o)
	if e != nil {
		return archiveReceipt{}, fmt.Errorf("invalid canonical archive publication")
	}
	expected := "movie"
	if item.Type == TVShow {
		expected = "tv"
	} else if item.Type == Music {
		expected = "music"
	}
	if o.LibraryType != expected || mediaKind(o.LibraryKey) != mediaKind(o.Path) {
		return archiveReceipt{}, fmt.Errorf("archive receipt targets wrong library or file type")
	}
	if _, e = archivePrimaryItem(a, archiveOutput{Path: o.LibraryKey}); e != nil {
		return archiveReceipt{}, fmt.Errorf("unsafe archive receipt routing")
	}
	root, e := archiveLibraryRoot(o.LibraryType)
	if e != nil || !safeArchiveName(o.LibraryKey) {
		return archiveReceipt{}, fmt.Errorf("unsafe archive publication key")
	}
	return archiveReceipt{Path: filepath.Join(root, o.LibraryKey), SHA256: o.SHA256, LibraryType: o.LibraryType, LibraryKey: o.LibraryKey}, nil
}
func verifyArchivePublications(a archiveSet, j *archiveJob) error {
	if !a.OutputsDeclared || len(a.Outputs) == 0 {
		return fmt.Errorf("archive lacks a complete output receipt manifest")
	}
	required := 0
	for _, o := range a.Outputs {
		if !o.Required {
			if o.Status != "skipped" {
				return fmt.Errorf("archive support receipt is pending")
			}
			continue
		}
		required++
		if o.Status != "validated" {
			return fmt.Errorf("archive primary receipt is pending")
		}
		r, e := archiveOutputReceipt(a, o)
		if e != nil {
			return e
		}
		if local, ok := j.Receipts[o.ID]; ok && (local.SHA256 != r.SHA256 || local.LibraryKey != r.LibraryKey || local.LibraryType != r.LibraryType) {
			return fmt.Errorf("local and server archive receipts disagree")
		}
		if e := verifyArchiveReceipt(r); e != nil {
			if _, statErr := os.Lstat(r.Path); os.IsNotExist(statErr) {
				return missingArchivePublication{r.Path}
			}
			return e
		}
	}
	if required == 0 {
		return fmt.Errorf("archive has no published primary")
	}
	return nil
}
func extractArchiveSources(ctx context.Context, a archiveSet, work string) ([]archiveDeclaredOutput, map[string]string, error) {
	paths, e := downloadArchiveVolumes(ctx, a, work)
	if e != nil {
		return nil, nil, e
	}
	extracted := filepath.Join(work, "extracted")
	if e = os.Mkdir(extracted, 0700); e != nil {
		return nil, nil, archiveFailure(archiveOperational, "storage", e)
	}
	files, e := extractRAR(ctx, paths, extracted, archiveLimits{MaxEntries: 100, MaxExpandedBytes: 100 << 30, Timeout: 30 * time.Minute})
	if e != nil {
		return nil, nil, e
	}
	return archiveExtractionManifest(extracted, files)
}

// Repair only absent local files from an unchanged, still-complete source set.
// Existing differing files are preserved for review. No new remote receipt is sent.
func restoreArchivePublications(ctx context.Context, a archiveSet, s *archiveWorkerState, j *archiveJob) error {
	work, e := archiveWorkdir(a.ID)
	if e != nil {
		return e
	}
	defer os.RemoveAll(work)
	declared, sources, e := extractArchiveSources(ctx, a, work)
	if e != nil {
		return e
	}
	if len(declared) != len(a.Outputs) {
		return fmt.Errorf("archive extraction changed since publication")
	}
	sizes := map[string]int64{}
	for _, o := range declared {
		sizes[o.Path] = o.Size
	}
	type restore struct {
		source  string
		receipt archiveReceipt
		id      string
	}
	pending := []restore{}
	for _, o := range a.Outputs {
		size, ok := sizes[o.Path]
		if !ok || size != o.Size {
			return fmt.Errorf("archive output manifest changed since publication")
		}
		if !o.Required {
			continue
		}
		r, e := archiveOutputReceipt(a, o)
		if e != nil {
			return e
		}
		if e = verifyArchiveReceipt(r); e == nil {
			continue
		}
		if _, e = os.Lstat(r.Path); !os.IsNotExist(e) {
			return fmt.Errorf("existing archive publication changed; preserving it for review")
		}
		p, e := prepareArchiveOutput(a, o, sources[o.Path])
		if e != nil {
			return e
		}
		if p.Digest != r.SHA256 {
			return fmt.Errorf("archive repair source differs from publication receipt")
		}
		pending = append(pending, restore{sources[o.Path], r, o.ID})
	}
	for _, p := range pending {
		if e := musicMkdirAll(filepath.Dir(p.receipt.Path)); e != nil {
			return e
		}
		target, e := publishMusicFile(p.source, p.receipt.Path, p.receipt.SHA256)
		if e != nil {
			return e
		}
		if target != p.receipt.Path {
			return fmt.Errorf("archive repair destination changed; existing file preserved")
		}
		j.Receipts[p.id] = p.receipt
		if e := saveArchiveState(*s); e != nil {
			return e
		}
		if p.receipt.LibraryType == "music" {
			TriggerMusicSync()
		} else if CatalogDB != nil {
			if e := AddPublishedFileToCatalog(target); e != nil {
				return e
			}
		}
	}
	return verifyArchivePublications(a, j)
}
