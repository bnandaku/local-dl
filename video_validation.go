package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type videoCodecError struct{ Codec string }

func (e videoCodecError) Error() string { return "video codec excluded by playback policy: " + e.Codec }

type videoStream struct {
	Index       int    `json:"index"`
	Type        string `json:"codec_type"`
	Codec       string `json:"codec_name"`
	Tag         string `json:"codec_tag_string"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Disposition struct {
		Attached int `json:"attached_pic"`
		Default  int `json:"default"`
	} `json:"disposition"`
	SideData []struct {
		Type string `json:"side_data_type"`
	} `json:"side_data_list"`
}
type videoProbe struct {
	Streams []videoStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

var supportedVideoExclusions = map[string]bool{"dolby_vision": true, "av1": true, "hevc": true, "h264": true, "vp9": true, "mpeg4": true, "truehd": true, "dts": true, "eac3": true}

func videoExclusions() (map[string]bool, error) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("VIDEO_EXCLUDED_CODECS")))
	if value == "" {
		value = "dolby_vision"
	}
	policy := map[string]bool{}
	if value == "none" {
		return policy, nil
	}
	for _, key := range strings.Split(value, ",") {
		key = strings.TrimSpace(key)
		if !supportedVideoExclusions[key] {
			return nil, fmt.Errorf("invalid VIDEO_EXCLUDED_CODECS setting")
		}
		policy[key] = true
	}
	return policy, nil
}
func excludedStreamCodec(s videoStream, policy map[string]bool) string {
	if s.Type == "video" && policy["dolby_vision"] {
		if s.Tag == "dvhe" || s.Tag == "dvh1" {
			return "dolby_vision"
		}
		for _, side := range s.SideData {
			if strings.Contains(strings.ToLower(side.Type), "dovi") || strings.Contains(strings.ToLower(side.Type), "dolby vision") {
				return "dolby_vision"
			}
		}
	}
	if policy[s.Codec] {
		return s.Codec
	}
	return ""
}
func selectVideoStreams(p videoProbe, policy map[string]bool) (videoStream, *videoStream, error) {
	var v *videoStream
	for i := range p.Streams {
		s := &p.Streams[i]
		if s.Type == "video" && s.Disposition.Attached == 0 && (v == nil || s.Disposition.Default > v.Disposition.Default) {
			v = s
		}
	}
	if v == nil {
		return videoStream{}, nil, badMediaError{"no_media"}
	}
	if v.Codec == "" || v.Codec == "unknown" || v.Width <= 0 || v.Height <= 0 {
		return videoStream{}, nil, fmt.Errorf("video codec or dimensions could not be determined")
	}
	if codec := excludedStreamCodec(*v, policy); codec != "" {
		return *v, nil, videoCodecError{codec}
	}
	var audio *videoStream
	excludedAudio := ""
	audioStreams := 0
	for i := range p.Streams {
		s := &p.Streams[i]
		if s.Type != "audio" {
			continue
		}
		audioStreams++
		if codec := excludedStreamCodec(*s, policy); codec != "" {
			excludedAudio = codec
			continue
		}
		if s.Codec == "" || s.Codec == "unknown" {
			continue
		}
		if audio == nil || s.Disposition.Default > audio.Disposition.Default {
			audio = s
		}
	}
	if audio == nil && excludedAudio != "" {
		return *v, nil, videoCodecError{excludedAudio}
	}
	if audio == nil && audioStreams > 0 {
		return *v, nil, fmt.Errorf("audio codec could not be determined")
	}
	return *v, audio, nil
}

// Metadata establishes codec policy; decode samples establish actual decoded frames.
// This is not a complete-file integrity scan or a Plex client Direct Play guarantee.
func probeVideo(path string) error {
	policy, e := videoExclusions()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-show_streams", "-show_format", "-of", "json", path)
	out := &limitedMusicOutput{max: 1 << 20}
	diagnostic := &limitedMusicOutput{max: 65536}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	if e := cmd.Run(); e != nil {
		if ctx.Err() == nil && invalidProbeInput(string(diagnostic.data)) {
			return badMediaError{"invalid_media"}
		}
		return fmt.Errorf("ffprobe unavailable, timed out or unable to read video")
	}
	var p videoProbe
	if e := json.Unmarshal(out.data, &p); e != nil {
		return fmt.Errorf("invalid ffprobe response")
	}
	video, audio, e := selectVideoStreams(p, policy)
	if e != nil {
		return e
	}
	duration, durationErr := strconv.ParseFloat(p.Format.Duration, 64)
	if durationErr != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return fmt.Errorf("invalid video duration")
	}
	offsets := []float64{0}
	if duration > 45 {
		offsets = append(offsets, duration/2, math.Max(0, duration-15))
	}
	decodeCtx, stop := context.WithTimeout(context.Background(), 3*time.Minute)
	defer stop()
	for _, offset := range offsets {
		if e := decodeVideoSample(decodeCtx, path, video, audio, offset); e != nil {
			return e
		}
	}
	return nil
}
func decodeVideoSample(ctx context.Context, path string, video videoStream, audio *videoStream, offset float64) error {
	args := []string{"-nostdin", "-v", "error", "-xerror", "-err_detect", "explode", "-threads", "2", "-protocol_whitelist", "file", "-ss", strconv.FormatFloat(offset, 'f', 3, 64), "-i", path, "-t", "15", "-map", fmt.Sprintf("0:%d", video.Index)}
	if audio != nil {
		args = append(args, "-map", fmt.Sprintf("0:%d", audio.Index))
	}
	args = append(args, "-sn", "-dn", "-threads", "2", "-progress", "pipe:1", "-nostats", "-f", "null", "-")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	output := &limitedMusicOutput{max: 65536}
	diagnostic := &limitedMusicOutput{max: 65536}
	cmd.Stdout, cmd.Stderr = output, diagnostic
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("video decode check timed out; retained for retry")
	}
	if err != nil {
		message := strings.ToLower(string(diagnostic.data))
		for _, text := range []string{"invalid data found when processing input", "error while decoding", "corrupt decoded frame", "error submitting packet to decoder", "invalid nal unit"} {
			if strings.Contains(message, text) {
				return badMediaError{"invalid_media"}
			}
		}
		return fmt.Errorf("video decode unavailable or failed; retained for operational repair")
	}
	frames := 0
	for _, line := range strings.Split(string(output.data), "\n") {
		if strings.HasPrefix(line, "frame=") {
			n, e := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "frame=")))
			if e == nil && n > frames {
				frames = n
			}
		}
	}
	if frames == 0 {
		return badMediaError{"invalid_media"}
	}
	return nil
}

func codecRejection(err error) (string, bool) {
	var e videoCodecError
	ok := errors.As(err, &e)
	return e.Codec, ok
}
