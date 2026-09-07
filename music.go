package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// MusicMetadata comes from the audio file, never from a guessed release name.
type MusicMetadata struct {
	Title       string   `json:"title"`
	Artist      string   `json:"artist"`
	AlbumArtist string   `json:"album_artist"`
	Album       string   `json:"album"`
	Genres      []string `json:"genres"`
	Track       int      `json:"track"`
	Disc        int      `json:"disc"`
	Discs       int      `json:"discs"`
	ISRC        string   `json:"isrc,omitempty"`
}

func isAudioFilename(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3", ".flac", ".m4a", ".aac", ".ogg", ".opus", ".wav", ".aif", ".aiff", ".alac", ".wma":
		return true
	}
	return false
}

func readMusicMetadata(path string) (MusicMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tool, err := findMediaTool("ffprobe")
	if err != nil {
		return MusicMetadata{}, err
	}
	cmd := exec.CommandContext(ctx, tool, "-v", "error", "-protocol_whitelist", "file", "-show_entries", "format_tags:stream=codec_type:stream_tags:stream_disposition=attached_pic", "-of", "json", path)
	output := &limitedMusicOutput{max: 1024 * 1024}
	probeErrors := &limitedMusicOutput{max: 65536}
	cmd.Stderr = probeErrors
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		if ctx.Err() == nil && invalidProbeInput(string(probeErrors.data)) {
			return MusicMetadata{}, badMediaError{"invalid_media"}
		}
		return MusicMetadata{}, fmt.Errorf("audio metadata probe failed: %w", err)
	}
	var probe struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Type        string `json:"codec_type"`
			Disposition struct {
				Attached int `json:"attached_pic"`
			} `json:"disposition"`
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output.data, &probe); err != nil {
		return MusicMetadata{}, fmt.Errorf("invalid audio metadata: %w", err)
	}
	tags := make(map[string]string)
	audio := false
	for _, stream := range probe.Streams {
		if stream.Type == "video" && stream.Disposition.Attached == 0 {
			return MusicMetadata{}, badMediaError{"wrong_content"}
		}
		if stream.Type == "audio" {
			audio = true
			for key, value := range stream.Tags {
				tags[strings.ToLower(key)] = strings.TrimSpace(value)
			}
		}
	}
	if !audio {
		return MusicMetadata{}, badMediaError{"no_media"}
	}
	for key, value := range probe.Format.Tags {
		tags[strings.ToLower(key)] = strings.TrimSpace(value)
	}
	m := MusicMetadata{Title: tags["title"], Artist: tags["artist"], AlbumArtist: tags["album_artist"], Album: tags["album"], ISRC: tags["isrc"]}
	if m.AlbumArtist == "" {
		m.AlbumArtist = tags["albumartist"]
	}
	if m.AlbumArtist == "" {
		m.AlbumArtist = m.Artist
	}
	m.Track, _ = musicNumber(tags["track"])
	m.Disc, m.Discs = musicNumber(tags["disc"])
	for _, genre := range strings.FieldsFunc(tags["genre"], func(r rune) bool { return r == ';' || r == ',' }) {
		if genre = strings.TrimSpace(genre); genre != "" {
			m.Genres = append(m.Genres, genre)
		}
	}
	return m, nil
}

type limitedMusicOutput struct {
	data []byte
	max  int
}

func (b *limitedMusicOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > b.max {
		return 0, fmt.Errorf("audio metadata exceeds size limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func musicNumber(value string) (int, int) {
	parts := strings.SplitN(value, "/", 2)
	n, _ := strconv.Atoi(parts[0])
	total := 0
	if len(parts) == 2 {
		total, _ = strconv.Atoi(parts[1])
	}
	if n < 0 || n > 9999 {
		n = 0
	}
	if total < 0 || total > 9999 {
		total = 0
	}
	return n, total
}

func musicComponent(value, fallback string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, value)
	value = strings.Trim(value, " .")
	if value == "" {
		return fallback
	}
	runes := []rune(value)
	if len(runes) > 100 {
		value = string(runes[:100])
	}
	return value
}

func primaryMusicGenre(m MusicMetadata) string {
	if len(m.Genres) == 0 {
		return "Unsorted"
	}
	genre := strings.TrimSpace(m.Genres[0])
	switch strings.ToLower(genre) {
	case "rock":
		genre = "Rock"
	case "jazz":
		genre = "Jazz"
	case "blues":
		genre = "Blues"
	case "pop":
		genre = "Pop"
	case "classical":
		genre = "Classical"
	case "electronic":
		genre = "Electronic"
	case "hip-hop", "hip hop", "hiphop":
		genre = "Hip-Hop"
	case "r&b", "rnb":
		genre = "R&B"
	}
	return musicComponent(genre, "Unsorted")
}

func musicRelativePath(m MusicMetadata, original, genre string) string {
	artist := musicComponent(m.AlbumArtist, "Unknown Artist")
	// Each untagged file gets its own album directory rather than mixing albums.
	album := musicComponent(m.Album, musicComponent(strings.TrimSuffix(filepath.Base(original), filepath.Ext(original)), "Unknown Album"))
	if m.Album == "" || m.AlbumArtist == "" {
		genre = "Unsorted"
	}
	dir := filepath.Join(musicComponent(genre, "Unsorted"), artist, album)
	if m.Disc > 0 {
		dir = filepath.Join(dir, fmt.Sprintf("Disc %02d", m.Disc))
	}
	name := musicComponent(m.Title, musicComponent(strings.TrimSuffix(filepath.Base(original), filepath.Ext(original)), "Unknown Track"))
	if m.Track > 0 {
		name = fmt.Sprintf("%02d - %s", m.Track, name)
	}
	return filepath.Join(dir, name+strings.ToLower(filepath.Ext(original)))
}
