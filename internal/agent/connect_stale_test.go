package agent

import (
	"os"
	"path/filepath"
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
