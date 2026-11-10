package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// plugins#935 (mk311): the Antigravity plugin groups its models' windows
// into the pools they share ("Gemini", "Claude & GPT"), as the built-in
// did, but host.js passed neither a window's family nor its pool on (the
// Go side, quotaOfPlugin, already read them: TestPluginWindowsKeepPoolAndFamily
// hands it a plugin.Usage past the host), so the card showed every
// model's window and two "7 days" with no pool named. testdata/antigravity-plugin-usage-935.json is what the plugin
// (0.1.0) sends for the reporter's figures: each Gemini model 85.1% left
// resetting at 20:31 +08, Claude and GPT-OSS 100% resetting 11-11 00:24:47
// +08, and a 7 days at 91.8% left. Through the host, the card is each
// pool's 5 hours and 7 days in place of its models'.
func TestPluginAntigravityPoolsReachTheCard(t *testing.T) {
	ws, err := os.ReadFile("testdata/antigravity-plugin-usage-935.json")
	if err != nil {
		t.Fatal(err)
	}
	windows := filepath.Join(t.TempDir(), "windows.json")
	if err := os.WriteFile(windows, []byte(`{"a@fake": `+string(ws)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	q := fakePluginUsage(t, windows)["a@fake"]
	if len(q.Windows) != 17 {
		t.Fatalf("windows: %d, want 17: %+v", len(q.Windows), q.Windows)
	}
	for _, w := range q.Windows {
		if w.Pool == "" {
			t.Errorf("%q has no pool", w.Name)
		}
		if (w.Model != "") != (w.Family != "") {
			t.Errorf("%q: model %q, family %q", w.Name, w.Model, w.Family)
		}
	}
	var lines []string
	for _, w := range PooledWindows(q.Windows) {
		lines = append(lines, w.Name)
	}
	want := []string{"Gemini · 7 days", "Gemini · 5 hours", "Claude & GPT · 7 days", "Claude & GPT · 5 hours"}
	if !slices.Equal(lines, want) {
		t.Errorf("the card's lines: %q, want %q", lines, want)
	}
	// the Claude & GPT pool's windows hold its models, and only them
	for _, w := range q.Windows {
		if w.PoolWindow() && w.Pool == "Claude & GPT" {
			if !w.CountsIn(q.Windows, "gpt-oss-120b-medium") || !w.CountsIn(q.Windows, "claude-opus-4-6-thinking") || w.CountsIn(q.Windows, "gemini-3.8-flash") {
				t.Errorf("%s counts the wrong models", w.LineName())
			}
		}
	}
}
