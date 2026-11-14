package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// claudePluginsSandbox is a sandbox whose Claude Code folder is moved by
// CLAUDE_CONFIG_DIR, as a user may have it.
func claudePluginsSandbox(t *testing.T) string {
	t.Helper()
	h := sandbox(t)
	d := filepath.Join(h, "cc-config")
	t.Setenv("CLAUDE_CONFIG_DIR", d)
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	return d
}

// The files as Claude Code 2.1.319 wrote them in a sandbox after
// `/plugin marketplace add` and `/plugin install`: absolute paths of this
// computer's, a marketplace from a local folder, one from a git URL with
// a token in it, and one from GitHub.
const (
	ccKnownFixture = `{
  "claude-plugins-official": {"source": {"source": "github", "repo": "anthropics/claude-plugins-official"}, "installLocation": "/Users/a/.claude/plugins/marketplaces/claude-plugins-official", "lastUpdated": "2026-11-14T10:00:00.000Z"},
  "claude-code-plugins": {"source": {"source": "github", "repo": "anthropics/claude-code"}, "installLocation": "/Users/a/.claude/plugins/marketplaces/claude-code-plugins", "lastUpdated": "2026-11-14T10:00:00.000Z"},
  "mine": {"source": {"source": "directory", "path": "/Users/a/src/my-market"}, "installLocation": "/Users/a/src/my-market", "lastUpdated": "2026-11-14T10:00:00.000Z"},
  "corp": {"source": {"source": "git", "url": "https://x-token:ghp_SECRET123@git.corp.example/plugins.git"}, "installLocation": "/Users/a/.claude/plugins/marketplaces/corp", "lastUpdated": "2026-11-14T10:00:00.000Z"}
}`
	ccInstalledFixture = `{"version": 2, "plugins": {
  "commit-commands@claude-code-plugins": [{"scope": "user", "installPath": "/Users/a/.claude/plugins/cache/claude-code-plugins/commit-commands/1.0.0", "version": "1.0.0", "installedAt": "2026-11-14T10:00:00.000Z", "lastUpdated": "2026-11-14T10:00:00.000Z"}]
}}`
	ccSettingsFixture = `{
  "model": "opus",
  "enabledPlugins": {
    "commit-commands@claude-code-plugins": true,
    "pr-review-toolkit@claude-code-plugins": false,
    "frontend-design@claude-plugins-official": true,
    "local-tool@mine": true,
    "secret-tool@corp": true
  },
  "extraKnownMarketplaces": {
    "team": {"source": {"source": "github", "repo": "acme/claude-plugins"}, "autoUpdate": true}
  }
}`
)

// TestClaudePluginsCollected (AXmake110 on X): a sync carries the
// marketplaces Claude Code knows and the plugins turned on, leaving out
// this computer's paths, a local folder's marketplace and a source with a
// sign-in in it.
func TestClaudePluginsCollected(t *testing.T) {
	d := claudePluginsSandbox(t)
	write(t, filepath.Join(d, "plugins", "known_marketplaces.json"), ccKnownFixture)
	write(t, filepath.Join(d, "plugins", "installed_plugins.json"), ccInstalledFixture)
	write(t, filepath.Join(d, "settings.json"), ccSettingsFixture)
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if b.ClaudePlugins == nil {
		t.Fatal("Claude Code's plugins not carried")
	}
	j := mustJSON(t, b)
	for _, bad := range []string{"/Users/a", "installLocation", "installPath", "ghp_SECRET123", "my-market", "lastUpdated"} {
		if strings.Contains(j, bad) {
			t.Errorf("carried %q:\n%s", bad, j)
		}
	}
	cp := b.ClaudePlugins
	var names []string
	for n := range cp.Marketplaces {
		names = append(names, n)
	}
	if len(cp.Marketplaces) != 2 || cp.Marketplaces["claude-code-plugins"] == nil || cp.Marketplaces["team"] == nil {
		t.Errorf("marketplaces carried: %v", names)
	}
	if r := gjson.GetBytes(cp.Marketplaces["claude-code-plugins"], "source.repo").String(); r != "anthropics/claude-code" {
		t.Errorf("claude-code-plugins carried as %s", cp.Marketplaces["claude-code-plugins"])
	}
	if !gjson.GetBytes(cp.Marketplaces["team"], "autoUpdate").Bool() {
		t.Errorf("team's autoUpdate lost: %s", cp.Marketplaces["team"])
	}
	want := []string{"commit-commands@claude-code-plugins", "frontend-design@claude-plugins-official"}
	if strings.Join(cp.Enabled, ",") != strings.Join(want, ",") {
		t.Errorf("enabled carried: %v, want %v", cp.Enabled, want)
	}
}

// TestClaudePluginsNoneOrUnknown: a computer with no Claude Code plugins
// carries none (its library part is as before), and one whose settings
// can't be read carries nothing rather than "none".
func TestClaudePluginsNoneOrUnknown(t *testing.T) {
	d := claudePluginsSandbox(t)
	write(t, filepath.Join(d, "settings.json"), `{"model": "opus"}`)
	write(t, filepath.Join(d, "plugins", "known_marketplaces.json"), `{"claude-plugins-official": {"source": {"source": "github", "repo": "anthropics/claude-plugins-official"}}}`)
	if b, err := Collect(); err != nil || b.ClaudePlugins != nil {
		t.Fatalf("nothing of the user's carried as %+v (%v)", b.ClaudePlugins, err)
	}
	write(t, filepath.Join(d, "settings.json"), `{"enabledPlugins": {"commit-commands@claude-code-plugins": tr`)
	if b, err := Collect(); err != nil || b.ClaudePlugins != nil {
		t.Fatalf("unreadable settings carried as %+v (%v)", b.ClaudePlugins, err)
	}
}

// TestClaudePluginsPut: on the other computer the marketplaces and
// plugins are added to settings.json, where Claude Code installs them
// from at its next start. What the user has there stays: their other
// settings, a plugin they turned off, a marketplace they know under that
// name. Claude Code's own files are not written.
func TestClaudePluginsPut(t *testing.T) {
	claudePluginsSandbox(t)
	in := Bundle{ClaudePlugins: &ClaudePlugins{
		Marketplaces: map[string]json.RawMessage{
			"claude-code-plugins": json.RawMessage(`{"source":{"source":"github","repo":"anthropics/claude-code"}}`),
			"team":                json.RawMessage(`{"source":{"source":"github","repo":"acme/claude-plugins"},"autoUpdate":true}`),
			"evil":                json.RawMessage(`{"source":{"source":"directory","path":"/Users/a/x"}}`),
		},
		Enabled: []string{"commit-commands@claude-code-plugins", "frontend-design@claude-plugins-official", "off-here@team", "x@evil", "y@nowhere"},
	}}
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	in.Instructions = b.Instructions
	in.Skills = b.Skills

	d := os.Getenv("CLAUDE_CONFIG_DIR")
	mine := "{\n  // mine\n  \"model\": \"sonnet\",\n  \"enabledPlugins\": {\"off-here@team\": false},\n  \"extraKnownMarketplaces\": {\"team\": {\"source\": {\"source\": \"github\", \"repo\": \"me/my-team\"}}}\n}\n"
	write(t, filepath.Join(d, "settings.json"), mine)
	ok(t)(Put(&in))
	got := read(t, filepath.Join(d, "settings.json"))
	if !strings.Contains(got, "// mine") {
		t.Errorf("settings.json rewritten whole:\n%s", got)
	}
	j := settingsOf(t, got)
	checks := map[string]string{
		"model":                        "sonnet",
		"enabledPlugins.off-here@team": "false",
		"enabledPlugins.commit-commands@claude-code-plugins":     "true",
		"enabledPlugins.frontend-design@claude-plugins-official": "true",
		"extraKnownMarketplaces.team.source.repo":                "me/my-team",
		"extraKnownMarketplaces.claude-code-plugins.source.repo": "anthropics/claude-code",
	}
	for path, want := range checks {
		if v := j.Get(gjsonPath(path)).String(); v != want {
			t.Errorf("%s = %q, want %q:\n%s", path, v, want, got)
		}
	}
	for _, gone := range []string{"enabledPlugins.x@evil", "enabledPlugins.y@nowhere", "extraKnownMarketplaces.evil"} {
		if j.Get(gjsonPath(gone)).Exists() {
			t.Errorf("%s added:\n%s", gone, got)
		}
	}
	for _, f := range []string{"known_marketplaces.json", "installed_plugins.json"} {
		if _, err := os.Stat(filepath.Join(d, "plugins", f)); err == nil {
			t.Errorf("Claude Code's own %s written", f)
		}
	}

	// again: nothing more to add, the file is left as it is
	before := got
	ok(t)(Put(&in))
	if after := read(t, filepath.Join(d, "settings.json")); after != before {
		t.Errorf("a second sync changed settings.json:\n%s", after)
	}

	// a settings.json that can't be read is not written over
	broken := `{"model": "sonnet", "enabledPlugins": {`
	write(t, filepath.Join(d, "settings.json"), broken)
	ok(t)(Put(&in))
	if after := read(t, filepath.Join(d, "settings.json")); after != broken {
		t.Errorf("unreadable settings.json written over:\n%s", after)
	}
}

func settingsOf(t *testing.T, s string) gjson.Result {
	t.Helper()
	var v any
	clean := strings.ReplaceAll(s, "// mine", "")
	if err := json.Unmarshal([]byte(clean), &v); err != nil {
		t.Fatalf("%v:\n%s", err, s)
	}
	return gjson.Parse(clean)
}

// gjsonPath escapes the @ and dots of a plugin id inside a dotted path
// written plainly in a test: only the first segment is a key of its own.
func gjsonPath(p string) string {
	head, rest, ok := strings.Cut(p, ".")
	if !ok {
		return gjson.Escape(p)
	}
	if head == "enabledPlugins" {
		return head + "." + gjson.Escape(rest)
	}
	return p
}
