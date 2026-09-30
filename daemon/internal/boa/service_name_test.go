package boa

import (
	"strings"
	"testing"
)

// A service name is logged when a sweep starts and becomes a ladder's key, so
// one carrying a newline forges a journal line (CodeQL go/log-injection, alert
// #2). The check refuses control characters and nothing an operator would type.
func TestValidServiceTakesWhatAnOperatorTypes(t *testing.T) {
	for _, s := range []string{
		"netflix", "Disney+", "Apple TV", "BBC iPlayer", "prime-video_4k",
		"Crunchyroll (JP)", "Ça marche", "動画", strings.Repeat("x", 64),
	} {
		if err := validService(s); err != nil {
			t.Errorf("validService(%q) refused a plausible name: %v", s, err)
		}
	}
}

func TestValidServiceRefusesWhatWouldForgeALogLine(t *testing.T) {
	for _, s := range []string{
		"",
		"netflix\ninfinite-streaming-boa: sweep aa:bb: done",
		"netflix\r",
		"tab\there",
		"nul\x00",
		"esc\x1b[31m",
		"bidi\u202eoverride",    // a format character, not a control one
		strings.Repeat("x", 65), // one over the limit
	} {
		if err := validService(s); err == nil {
			t.Errorf("validService(%q) accepted it", s)
		}
	}
}
