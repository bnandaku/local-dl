package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVideoPolicyRejectsActualCodecRegardlessOfFilename(t *testing.T) {
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg required")
	}
	path := filepath.Join(t.TempDir(), "Movie.H264.mp4")
	if out, e := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=size=16x16:rate=1", "-t", "1", "-c:v", "mpeg4", path).CombinedOutput(); e != nil {
		t.Fatalf("%s %v", out, e)
	}
	t.Setenv("VIDEO_EXCLUDED_CODECS", "mpeg4")
	if e := probeVideo(path); e == nil {
		t.Fatal("excluded actual codec accepted because filename says H264")
	}
	t.Setenv("VIDEO_EXCLUDED_CODECS", "none")
	if e := probeVideo(path); e != nil {
		t.Fatalf("valid permitted video rejected: %v", e)
	}
}
func TestUnknownVideoPolicyIsOperational(t *testing.T) {
	t.Setenv("VIDEO_EXCLUDED_CODECS", "typo")
	e := probeVideo("/missing/video.mp4")
	var bad badMediaError
	if e == nil || errors.As(e, &bad) {
		t.Fatal("invalid policy must stop validation without blacklisting")
	}
}

func TestDolbyVisionSideDataAndSafeAudioFallback(t *testing.T) {
	var p videoProbe
	raw := `{"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","width":1920,"height":1080,"side_data_list":[{"side_data_type":"DOVI configuration record"}]},{"index":1,"codec_type":"audio","codec_name":"truehd","disposition":{"default":1}},{"index":2,"codec_type":"audio","codec_name":"aac"}]}`
	if e := json.Unmarshal([]byte(raw), &p); e != nil {
		t.Fatal(e)
	}
	_, _, e := selectVideoStreams(p, map[string]bool{"dolby_vision": true})
	if codec, ok := codecRejection(e); !ok || codec != "dolby_vision" {
		t.Fatalf("Dolby Vision missed: %v", e)
	}
	_, audio, e := selectVideoStreams(p, map[string]bool{"truehd": true})
	if e != nil || audio == nil || audio.Index != 2 {
		t.Fatalf("safe alternate audio rejected: %v %v", audio, e)
	}
	p.Streams = p.Streams[:2]
	_, _, e = selectVideoStreams(p, map[string]bool{"truehd": true})
	if codec, ok := codecRejection(e); !ok || codec != "truehd" {
		t.Fatalf("excluded-only audio accepted: %v", e)
	}
}
func TestDecoderCorruptionRequeuesButMissingDecoderDoesNotBlacklist(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "ffmpeg")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		diagnostic string
		bad        bool
	}{
		{"Error while decoding stream #0:0: Invalid data found when processing input", true},
		{"Decoder not found", false},
		{"Input/output error", false},
	} {
		if e := os.WriteFile(tool, []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.diagnostic+"' >&2\nexit 1\n"), 0700); e != nil {
			t.Fatal(e)
		}
		e := decodeVideoSample(context.Background(), "unused", videoStream{}, nil, 0)
		var bad badMediaError
		if e == nil || errors.As(e, &bad) != tc.bad {
			t.Fatalf("%s misclassified: %v", tc.diagnostic, e)
		}
	}
}

func TestUnknownDurationCannotPassOnlyOpeningSample(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VIDEO_EXCLUDED_CODECS", "none")
	probe := `#!/bin/sh
printf '%s\n' '{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"duration":"N/A"}}'
`
	if e := os.WriteFile(filepath.Join(root, "ffprobe"), []byte(probe), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "ffmpeg"), []byte("#!/bin/sh\nprintf 'frame=1\\n'\n"), 0700); e != nil {
		t.Fatal(e)
	}
	e := probeVideo("unused.mp4")
	var bad badMediaError
	if e == nil || errors.As(e, &bad) {
		t.Fatalf("unknown duration must hold, not publish or blacklist: %v", e)
	}
}
