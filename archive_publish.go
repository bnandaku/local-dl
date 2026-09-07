package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func archiveLibraryRoot(kind string) (string, error) {
	switch kind {
	case "movie":
		return MoviesPath, nil
	case "tv":
		return TVShowPath, nil
	case "music":
		return musicEnv("MUSIC_PATH", "/mnt/music"), nil
	}
	return "", fmt.Errorf("unknown archive library type")
}
func archivePrimaryItem(a archiveSet, o archiveOutput) (*Item, error) {
	i := &Item{Name: filepath.Base(o.Path), RelativePath: o.Path}
	switch mediaKind(o.Path) {
	case "audio":
		i.Type = Music
	case "video":
		if a.MediaType != "movie" && a.MediaType != "tv" {
			return nil, badMediaError{"wrong_content"}
		}
		i.Type = Movies
		if a.MediaType == "tv" {
			i.Type = TVShow
		}
		if e := routeMedia(i); e != nil {
			return nil, e
		}
		if a.MediaType == "movie" && i.Type == TVShow {
			return nil, badMediaError{"wrong_content"}
		}
	default:
		return nil, badMediaError{"wrong_content"}
	}
	return i, nil
}

type preparedArchiveOutput struct {
	Output   archiveOutput
	Source   string
	Item     *Item
	Metadata MusicMetadata
	Digest   string
}

func prepareArchiveOutput(a archiveSet, o archiveOutput, source string) (preparedArchiveOutput, error) {
	p := preparedArchiveOutput{Output: o, Source: source}
	i, e := archivePrimaryItem(a, o)
	if e != nil {
		return p, e
	}
	p.Item = i
	if e = detectDownloadFailure(source); e != nil {
		return p, e
	}
	if i.Type == Music {
		p.Metadata, e = readMusicMetadata(source)
	} else {
		e = probeVideo(source)
	}
	if e != nil {
		return p, e
	}
	p.Digest, e = musicFileDigest(source)
	return p, e
}
func archiveReceiptAt(path, digest, kind string) (archiveReceipt, error) {
	root, e := archiveLibraryRoot(kind)
	if e != nil {
		return archiveReceipt{}, e
	}
	root, e = filepath.Abs(root)
	if e != nil {
		return archiveReceipt{}, e
	}
	absolute, e := filepath.Abs(path)
	if e != nil {
		return archiveReceipt{}, e
	}
	key, e := filepath.Rel(root, absolute)
	if e != nil || !safeArchiveName(key) || len(key) > 1024 {
		return archiveReceipt{}, fmt.Errorf("publication outside archive library")
	}
	return archiveReceipt{Path: absolute, SHA256: digest, LibraryType: kind, LibraryKey: key}, nil
}
func verifyArchiveReceipt(r archiveReceipt) error {
	expected, e := archiveReceiptAt(r.Path, r.SHA256, r.LibraryType)
	if e != nil || expected.LibraryKey != r.LibraryKey || len(r.SHA256) != 64 {
		return fmt.Errorf("unsafe archive publication receipt")
	}
	if e = safeArchiveDestination(filepath.Dir(r.Path)); e != nil {
		return e
	}
	info, e := os.Lstat(r.Path)
	if e != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("archive publication is missing or not a regular file")
	}
	digest, e := musicFileDigest(r.Path)
	if e != nil || digest != r.SHA256 {
		return fmt.Errorf("archive publication is missing or changed")
	}
	return nil
}
func publishArchivePrimary(p preparedArchiveOutput) (archiveReceipt, error) {
	kind := "movie"
	if p.Item.Type == TVShow {
		kind = "tv"
	} else if p.Item.Type == Music {
		kind = "music"
	}
	root, e := archiveLibraryRoot(kind)
	if e != nil {
		return archiveReceipt{}, e
	}
	if e = safeArchiveDestination(root); e != nil {
		return archiveReceipt{}, e
	}
	var target string
	if p.Item.Type == Music {
		// FileId remains zero: extracted outputs do not have Put.io file IDs.
		target, e = publishMusic(root, p.Source, p.Item, p.Metadata, p.Digest)
	} else {
		target, e = videoDestination(p.Item.Name, p.Item.Type, p.Item.RelativePath)
		if e == nil {
			e = musicMkdirAll(filepath.Dir(target))
		}
		if e == nil {
			target, e = publishMusicFile(p.Source, target, p.Digest)
		}
	}
	if e != nil {
		return archiveReceipt{}, e
	}
	r, e := archiveReceiptAt(target, p.Digest, kind)
	if e != nil {
		return r, e
	}
	if p.Item.Type == Music {
		TriggerMusicSync()
	} else if CatalogDB != nil {
		if e := AddPublishedFileToCatalog(target); e != nil {
			logMessage(LogLevelWarn, "Archive", "Published video catalog update will retry on next scan")
		}
	}
	return r, verifyArchiveReceipt(r)
}

var archiveTrailer = regexp.MustCompile(`(?i)(^|[ ._/-])trailer([ ._/-]|$)`)
var archiveSample = regexp.MustCompile(`(?i)(^|[ ._/-])(sample|preview)([ ._/-]|$)`)

// Supports are optional: retain only verified, unambiguously associated assets.
// The bot receives skipped for supports because its required receipts model is
// intentionally limited to primary media; our local receipt retains kept assets.
func publishArchiveSupport(o archiveOutput, source string, prepared []preparedArchiveOutput, receipts map[string]archiveReceipt) (*archiveReceipt, error) {
	kind := mediaKind(o.Path)
	if archiveSample.MatchString(o.Path) || kind == "" || kind == "audio" || (kind == "video" && !archiveTrailer.MatchString(o.Path)) {
		return nil, nil
	}
	var chosen *archiveReceipt
	var primary preparedArchiveOutput
	parent := filepath.Dir(o.Path)
	for {
		candidates := []preparedArchiveOutput{}
		for _, p := range prepared {
			if filepath.Dir(p.Output.Path) != parent {
				continue
			}
			r, ok := receipts[p.Output.ID]
			if !ok {
				continue
			}
			if (kind == "subtitle" || kind == "video") && r.LibraryType == "music" {
				continue
			}
			candidates = append(candidates, p)
		}
		if len(candidates) > 0 {
			if kind == "art" {
				directory := ""
				for _, p := range candidates {
					r := receipts[p.Output.ID]
					dir := filepath.Dir(r.Path)
					if directory != "" && directory != dir {
						return nil, nil
					}
					directory = dir
				}
				primary = candidates[0]
			} else {
				matching := []preparedArchiveOutput{}
				for _, p := range candidates {
					stem := strings.ToLower(strings.TrimSuffix(filepath.Base(p.Output.Path), filepath.Ext(p.Output.Path)))
					name := strings.ToLower(filepath.Base(o.Path))
					if strings.HasPrefix(name, stem+".") || strings.HasPrefix(name, stem+"-") {
						matching = append(matching, p)
					}
				}
				if len(matching) == 1 {
					primary = matching[0]
				} else if len(matching) == 0 && len(candidates) == 1 {
					primary = candidates[0]
				} else {
					return nil, nil
				}
			}
			r := receipts[primary.Output.ID]
			chosen = &r
			break
		}
		if parent == "." {
			break
		}
		parent = filepath.Dir(parent)
	}
	if chosen == nil {
		return nil, nil
	}
	if e := verifyArchiveReceipt(*chosen); e != nil {
		return nil, e
	}
	if e := validateArchiveSupport(source, kind); e != nil {
		return nil, nil
	}
	dir := filepath.Dir(chosen.Path)
	name := filepath.Base(o.Path)
	if kind == "art" {
		lower := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		if lower != "cover" && lower != "folder" && lower != "poster" && lower != "fanart" {
			return nil, nil
		}
		if chosen.LibraryType == "music" && strings.HasPrefix(filepath.Base(dir), "Disc ") {
			dir = filepath.Dir(dir)
		}
	} else if kind == "video" {
		dir = filepath.Join(dir, "Trailers")
	} else {
		stem := strings.TrimSuffix(filepath.Base(primary.Output.Path), filepath.Ext(primary.Output.Path))
		suffix := "." + name
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(stem)+".") {
			suffix = name[len(stem):]
		}
		name = strings.TrimSuffix(filepath.Base(chosen.Path), filepath.Ext(chosen.Path)) + suffix
	}
	if e := musicMkdirAll(dir); e != nil {
		return nil, e
	}
	digest, e := musicFileDigest(source)
	if e != nil {
		return nil, e
	}
	target, e := publishMusicFile(source, filepath.Join(dir, name), digest)
	if e != nil {
		return nil, e
	}
	receipt, e := archiveReceiptAt(target, digest, chosen.LibraryType)
	return &receipt, e
}
func validateArchiveSupport(path, kind string) error {
	if kind == "video" {
		return probeVideo(path)
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 65536))
	if e != nil {
		return e
	}
	if kind == "art" {
		if !strings.HasPrefix(http.DetectContentType(b), "image/") {
			return fmt.Errorf("not artwork")
		}
		return nil
	}
	text := strings.ToLower(strings.TrimSpace(string(b)))
	if len(b) == 0 || strings.HasPrefix(text, "mz") || strings.HasPrefix(text, "<html") || strings.HasPrefix(text, "<!doctype") || strings.Contains(text, "\"error_type\"") {
		return fmt.Errorf("invalid supporting media")
	}
	return nil
}
