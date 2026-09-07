package main

import "fmt"

// Never discard a codec-rejected candidate unless replacement search uses the
// same exclusion. Regular bad/wrong_content reports use the existing v2 API.
func requireBotCodecExclusion(codec string) error {
	if !supportedVideoExclusions[codec] {
		return fmt.Errorf("unrecognized observed codec")
	}
	var c struct {
		Service  string   `json:"service"`
		Contract int      `json:"recovery_contract"`
		Features []string `json:"features"`
		Excluded []string `json:"video_excluded_codecs"`
	}
	if _, e := botLinkCall("GET", "/api/v1/capabilities", nil, &c); e != nil {
		return e
	}
	feature := false
	for _, f := range c.Features {
		if f == "video_codec_policy" {
			feature = true
		}
	}
	if c.Service != "putio-go-server" || c.Contract != 2 || !feature {
		return fmt.Errorf("bot codec-aware replacement unavailable; source retained")
	}
	for _, excluded := range c.Excluded {
		if excluded == codec {
			return nil
		}
	}
	return fmt.Errorf("bot search does not exclude observed codec; source retained")
}
