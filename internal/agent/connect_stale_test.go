package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Codex writes config.toml itself (a project's trust, a notice seen), so a
// copy reopened after magpie's change and then writing there was still
// told to reopen; an app-server daemon left running for another
// CODEX_HOME was counted too (#729). Only magpie's own change counts, for
// copies of this home's Codex.
func TestCodexStaleOnlyForMagpiesChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	cfg := filepath.Join(dir, "config.toml")
	list := filepath.Join(dir, "magpie-models.json")
	conf := "model = \"magpie/gpt-6-luna\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = \"" + list + "\"\n\n" +
		"[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:3425/v1\"\n"
	os.WriteFile(cfg, []byte(conf), 0o600)
	os.WriteFile(list, []byte(`{"models":[{"slug":"magpie/gpt-6-luna"}]}`), 0o644)
	then := time.Now().Add(-2 * time.Hour)
	os.Chtimes(cfg, then, then)
	os.Chtimes(list, then, then)

	was := running
	t.Cleanup(func() { running = was })
	running = func(string) []runningProc {
		return []runningProc{
			// the ChatGPT app's Codex, reopened an hour ago
			{time.Hour, "/Applications/ChatGPT.app/Contents/Resources/codex app-server --listen stdio://"},
			// a daemon another CODEX_HOME's Codex left running
			{3 * time.Hour, "/tmp/sb/.codex/packages/app-server-daemon/releases/0.160.0/bin/codex app-server --listen unix:// --managed-daemon"},
		}
	}
	a := codex(home)
	if n := a.Stale(); n != 0 {
		t.Fatalf("reopened after magpie's change, other home's daemon: %d stale, want 0", n)
	}

	// Codex trusts a project: config.toml written now, magpie's part as it was
	f, _ := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n[projects.\"/Users/x/work\"]\ntrust_level = \"trusted\"\n")
	f.Close()
	if n := a.Stale(); n != 0 {
		t.Fatalf("Codex's own write: %d stale, want 0", n)
	}

	// magpie's list changes: the copy running from before has the old one
	os.WriteFile(list, []byte(`{"models":[{"slug":"magpie/gpt-6-luna"},{"slug":"magpie/new"}]}`), 0o644)
	if n := a.Stale(); n != 1 {
		t.Fatalf("magpie's list changed: %d stale, want 1", n)
	}
	// and stays so while nothing else changes, magpie restarted or not
	if n := codex(home).Stale(); n != 1 {
		t.Fatalf("asked again: %d stale, want 1", n)
	}
}

// miaopasi on Discord: the Codex row stayed "已接入 · 重开 Codex 后生效"
// after Codex was restarted. On macOS closing the Codex app's window
// leaves the app running, its app-server with it; an editor's Codex and
// the CLI's managed daemon run on through a reopened app too. Each copy
// left is told apart, so the row can say which one and how it is reopened,
// and one app's processes are one copy, not one per process.
func TestCodexStaleCopiesSayWhichIsLeft(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte("model = \"gpt-6.1-sol\"\nopenai_base_url = \"http://127.0.0.1:3425/backend-api/codex\"\n"), 0o600)
	then := time.Now().Add(-2 * time.Hour)
	os.Chtimes(cfg, then, then)

	was := running
	t.Cleanup(func() { running = was })
	app := "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"
	running = func(string) []runningProc {
		return []runningProc{
			// the Codex app, its window closed and opened again: the app
			// and its processes from three days ago
			{72 * time.Hour, app + " -c features.code_mode_host=true app-server --analytics-default-enabled"},
			{72*time.Hour - 10*time.Second, app + " exec-server --remote https://codex-cloud-environments.chatgpt.com/api"},
			// VS Code's Codex
			{5 * time.Hour, home + "/.vscode/extensions/openai.chatgpt-26.1003.0-darwin-arm64/bin/macos-aarch64/codex app-server --analytics-default-enabled"},
			// the CLI's daemon for this home
			{4 * time.Hour, dir + "/packages/app-server-daemon/releases/0.160.0-aarch64-apple-darwin/bin/codex app-server --listen unix:// --managed-daemon"},
			// another app's own Codex (miaopasi: Agents Anywhere's
			// connector), its app-server talked to over stdio
			{20 * time.Hour, home + "/Library/Application Support/Agents Anywhere/connector/.venv/lib/python3.12/site-packages/codex_cli_bin/bin/codex app-server --listen stdio://"},
			// a codex fnm installed, run in a terminal: no app's
			{20 * time.Hour, home + "/Library/Application Support/fnm/node-versions/v24.1.0/installation/bin/codex"},
			// a codex in a terminal from before, and one opened since
			{3 * time.Hour, "/opt/homebrew/bin/codex"},
			{time.Minute, "/opt/homebrew/bin/codex"},
		}
	}
	a := codex(home)
	got := a.StaleCopies()
	var kinds []string
	for _, c := range got {
		kinds = append(kinds, c.Kind)
	}
	if want := []string{"app", "ide", "daemon", "embedded", "cli", "cli"}; strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("copies: %v, want %v", kinds, want)
	}
	if d := time.Since(got[0].Since); d < 72*time.Hour-time.Minute || d > 72*time.Hour+time.Minute {
		t.Fatalf("the app's copy started %v ago, want the app-server's 72h", d)
	}
	if got[3].App != "Agents Anywhere" {
		t.Fatalf("the embedded copy's app: %q, want Agents Anywhere", got[3].App)
	}
	if n := a.Stale(); n != 6 {
		t.Fatalf("Stale: %d, want 6 (one app, not one per process)", n)
	}
}
