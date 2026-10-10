package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Codex 0.162's TUI runs on a background app-server it starts once and
// leaves running, which built its model list when it started: after magpie
// changes Codex's list, a new codex session still shows Codex's own models
// until that app-server is restarted, while the desktop app, restarted,
// shows magpie's (TJHHHH, luci on Discord). Restarting the app and the open
// sessions, as the advice said, leaves it; the advice after a change names
// it and the command that restarts it, here and in WSL, where the Agents
// row can't tell a stale copy.
func TestCodexAdviceNamesTheSharedAppServer(t *testing.T) {
	home, _ := codexHome(t, "", "")
	was, wasKeep := codexRunning, keepCodexDaemon
	codexRunning = func() bool { return true }
	keepCodexDaemon = func(context.Context, string, time.Time) provider.CodexDaemonCheck { return provider.CodexDaemonCheck{} }
	t.Cleanup(func() { codexRunning, keepCodexDaemon = was, wasKeep })
	if n := codex(home).Notice(); !strings.Contains(n, provider.CodexDaemonRestart) {
		t.Errorf("Codex's advice leaves out its app-server: %q", n)
	}
	for _, k := range wslKinds {
		if n := k.restart.say("command", provider.CodexDaemonRestart); k.id == "codex" && !strings.Contains(n, provider.CodexDaemonRestart) {
			t.Errorf("Codex in WSL's advice leaves out its app-server: %q", n)
		}
	}
	// nothing running, nothing to restart
	codexRunning = func() bool { return false }
	if n := codex(home).Notice(); strings.Contains(n, provider.CodexDaemonRestart) {
		t.Errorf("advice with no Codex running: %q", n)
	}
}

// luci (Discord, Codex 0.162): after Codex was wired to magpie every new
// codex showed Codex's own models, since the background app-server they
// attach to was started before, on the official config. Said after a
// change (the GUI's and the CLI's), the advice first checks that
// app-server against magpie's change in this Codex's home: restarted by
// magpie when no session is on it, named with the Agents page's Restart
// when one is.
func TestCodexNoticeKeepsTheDaemonCurrent(t *testing.T) {
	home, _ := codexHome(t, "", "")
	dir := filepath.Join(home, ".codex")
	cfg, list := filepath.Join(dir, "config.toml"), filepath.Join(dir, "magpie-models.json")
	os.WriteFile(cfg, []byte("model_provider = \"magpie\"\nmodel_catalog_json = \""+list+"\"\n"), 0o600)
	os.WriteFile(list, []byte(`{"models":[{"slug":"magpie/a"}]}`), 0o644)
	then := time.Now().Add(-2 * time.Hour)
	os.Chtimes(cfg, then, then)
	os.Chtimes(list, then, then)
	wasRunning, wasKeep := codexRunning, keepCodexDaemon
	t.Cleanup(func() { codexRunning, keepCodexDaemon = wasRunning, wasKeep })
	codexRunning = func() bool { return true }
	var asked []string
	var changed time.Time
	answer := provider.CodexDaemonCheck{}
	keepCodexDaemon = func(_ context.Context, h string, c time.Time) provider.CodexDaemonCheck {
		asked, changed = append(asked, h), c
		return answer
	}

	// nothing behind: the advice as before
	if n := codex(home).Notice(); n != codexRestartAll {
		t.Fatalf("no daemon behind: %q", n)
	}
	if len(asked) != 1 || asked[0] != dir || changed.Sub(then).Abs() > time.Second {
		t.Fatalf("asked %v at %v, want %s at %v", asked, changed, dir, then)
	}
	// magpie's list changes: the daemon is checked against now
	os.WriteFile(list, []byte(`{"models":[{"slug":"magpie/a"},{"slug":"magpie/b"}]}`), 0o644)
	answer = provider.CodexDaemonCheck{Behind: true, Restarted: true, Since: then}
	n := codex(home).Notice()
	if time.Since(changed) > time.Minute {
		t.Fatalf("checked against %v, not magpie's change", changed)
	}
	if !strings.Contains(n, "magpie restarted") {
		t.Fatalf("restarted: %q", n)
	}
	// a session on it: the user is told where to restart it
	answer = provider.CodexDaemonCheck{Behind: true, Attached: 1, Since: then}
	if n := codex(home).Notice(); !strings.Contains(n, "Agents page") || !strings.Contains(n, provider.CodexDaemonRestart) {
		t.Fatalf("session attached: %q", n)
	}
	// as last checked, for the row on Windows
	if got := codexDaemonBehind(); len(got) != 1 || got[0].Kind != "daemon" || !got[0].Since.Equal(then) {
		t.Fatalf("behind: %+v", got)
	}
	CodexDaemonRestarted()
	if got := codexDaemonBehind(); got != nil {
		t.Fatalf("after a restart: %+v", got)
	}
}
