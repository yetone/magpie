package provider

import (
	"strings"
	"testing"
)

// TestNotProbedSetMatchesThePresets: notProbed answers from a set made
// once of the vendors' and local servers' preset hosts, as the walk over
// every preset it replaced did, and with no garbage per ask.
func TestNotProbedSetMatchesThePresets(t *testing.T) {
	for _, d := range presets {
		bases := []string{d.Chat, d.Responses, d.Anthropic}
		for _, r := range d.Regions {
			bases = append(bases, r.Chat, r.Responses, r.Anthropic)
		}
		for _, b := range bases {
			h := hostnameOf(b)
			if b == "" || h == "" {
				continue
			}
			want := d.Kind == KindVendor || d.Kind == KindLocal
			if want && !notProbed(strings.ToUpper(h)) {
				t.Errorf("%s (%s): %s is probed, want left alone", d.ID, d.Kind, h)
			}
		}
	}
	if notProbed("relay.example.com") {
		t.Error("a relay's host is not probed")
	}
	if got := testing.AllocsPerRun(100, func() { notProbed("api.deepseek.com") }); got > 0 {
		t.Errorf("notProbed allocates %v times an ask, want 0", got)
	}
}
