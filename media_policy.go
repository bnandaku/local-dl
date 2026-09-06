package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

func mediaKind(name string) string {
	if isAudioFilename(name) {
		return "audio"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".mpg", ".mpeg", ".ts", ".m2ts", ".mts", ".webm", ".vob", ".ogv", ".3gp":
		return "video"
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".tif", ".tiff", ".bmp":
		return "art"
	case ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".smi", ".sup":
		return "subtitle"
	}
	return ""
}
func safeMediaName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}
func safeMediaRelative(name string) bool {
	if name == "" {
		return true
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	for _, p := range strings.Split(name, "/") {
		if p == ".." || p == "." || p == "" {
			return false
		}
	}
	return true
}

// The actual file format wins over a remote label. Supporting files require an
// explicit primary association, so an album cover cannot become a movie.
func routeMedia(i *Item) error {
	if !safeMediaName(i.Name) || !safeMediaRelative(i.RelativePath) {
		return fmt.Errorf("unsafe media path")
	}
	kind := mediaKind(i.Name)
	switch kind {
	case "audio":
		i.Type = Music
	case "video":
		if tvShowPattern.MatchString(i.Name+"/"+i.RelativePath) || tvShowPatternSpelled.MatchString(i.Name+"/"+i.RelativePath) || i.Type == TVShow || i.Type == Anime {
			i.Type = TVShow
		} else {
			i.Type = Movies
		}
	case "art", "subtitle":
		if !safeMediaName(i.MediaName) {
			return fmt.Errorf("supporting file has no safe primary association")
		}
		primary := Item{Name: i.MediaName, Type: i.Type, RelativePath: i.RelativePath}
		if mediaKind(primary.Name) != "audio" && mediaKind(primary.Name) != "video" {
			return fmt.Errorf("invalid primary media")
		}
		if err := routeMedia(&primary); err != nil {
			return err
		}
		i.Type = primary.Type
		if i.Type == Music && (kind != "art" || i.MediaFileID <= 0) {
			return fmt.Errorf("music support needs an audio source identity")
		}
	default:
		return fmt.Errorf("excluded non-media file")
	}
	return nil
}
