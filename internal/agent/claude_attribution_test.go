package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeAttributionSet (9_bishi on X): Claude Code's commit trailer is
// switched off by attribution.commit = "" in its settings.json, the user's
// other keys kept; On takes back what hid it; text the user wrote as the
// trailer is never written over; the pull request text is left alone.
func TestClaudeAttributionSet(t *testing.T) {
	home := tuneSandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	flat := func() string { return strings.Join(strings.Fields(tuneFile(t, path)), "") }
	tuneWrite(t, path, `{"theme": "dark", "attribution": {"pr": "made with x"}}`)
	if got := ClaudeAttribution(home); got != AttributionOn {
		t.Fatalf("read %s; want on", got)
	}
	if err := ClaudeAttributionSet(home, false); err != nil {
		t.Fatal(err)
	}
	if got := flat(); got != `{"theme":"dark","attribution":{"commit":"","pr":"madewithx"}}` {
		t.Fatalf("after off: %s", got)
	}
	if got := ClaudeAttribution(home); got != AttributionOff {
		t.Fatalf("read %s; want off", got)
	}
	if err := ClaudeAttributionSet(home, true); err != nil {
		t.Fatal(err)
	}
	if got := flat(); got != `{"theme":"dark","attribution":{"pr":"madewithx"}}` {
		t.Fatalf("after on: %s", got)
	}

	// the legacy key and `attribution: false` read as off, and On clears them
	for _, c := range []string{`{"includeCoAuthoredBy": false}`, `{"attribution": false}`} {
		tuneWrite(t, path, c)
		if got := ClaudeAttribution(home); got != AttributionOff {
			t.Fatalf("%s read %s; want off", c, got)
		}
		if err := ClaudeAttributionSet(home, true); err != nil {
			t.Fatal(err)
		}
		if got := ClaudeAttribution(home); got != AttributionOn || flat() != `{}` {
			t.Fatalf("%s: after on %s %s", c, got, flat())
		}
	}

	// the user's own trailer is theirs
	own := `{"attribution": {"commit": "Assisted-by: me"}}`
	tuneWrite(t, path, own)
	if got := ClaudeAttribution(home); got != AttributionCustom {
		t.Fatalf("read %s; want custom", got)
	}
	for _, on := range []bool{false, true} {
		if err := ClaudeAttributionSet(home, on); err == nil {
			t.Fatalf("on=%v wrote over the user's text", on)
		}
	}
	if got := tuneFile(t, path); got != own {
		t.Fatalf("the user's text changed: %s", got)
	}
}
