package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Codex app's work mode on a WSL project runs its app-server on the
// distro's ~/.codex: Codex connected on Windows connects the running
// distro's Codex too, once (@not_4O4 on X).
func TestConnectCodexTwins(t *testing.T) {
	_, _ = codexHome(t, "", "")
	root := t.TempDir()
	cfgPath := filepath.Join(root, "home", "me", ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	undo := FakeWSL(map[string]string{"Ubuntu": "home:/home/me\ndir:.codex\n"}, map[string]string{"Ubuntu": root})
	defer undo()
	read := func() string { b, _ := os.ReadFile(cfgPath); return string(b) }
	win := func() *Agent {
		for _, a := range All() {
			if a.ID == "codex" {
				return a
			}
		}
		t.Fatal("no codex")
		return nil
	}

	// Windows' Codex not connected: the distro's is left alone
	ConnectCodexTwins()
	if strings.Contains(read(), "magpie") {
		t.Fatalf("connected before Windows' Codex:\n%s", read())
	}

	if err := win().Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	ConnectCodexTwins()
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || !strings.Contains(cfg, "magpie-models.json") {
		t.Fatalf("distro's Codex not connected:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfgPath), "magpie-models.json")); err != nil {
		t.Fatal(err)
	}

	// the user disconnects it: it stays so
	var tw *Agent
	for _, a := range All() {
		if a.ID == "codex@wsl:Ubuntu" {
			tw = a
		}
	}
	if tw == nil {
		t.Fatal("no twin")
	}
	if err := tw.Disconnect(); err != nil {
		t.Fatal(err)
	}
	ConnectCodexTwins()
	if strings.Contains(read(), `model_provider = "magpie"`) {
		t.Fatalf("connected again after Disconnect:\n%s", read())
	}
}

// A distro's Codex on a provider of the user's own is not taken over.
func TestConnectCodexTwinsKeepsOwnProvider(t *testing.T) {
	_, _ = codexHome(t, "", "")
	root := t.TempDir()
	cfgPath := filepath.Join(root, "home", "me", ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	own := "model_provider = \"mine\"\n[model_providers.mine]\nbase_url = \"https://x.example/v1\"\n"
	if err := os.WriteFile(cfgPath, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	undo := FakeWSL(map[string]string{"Ubuntu": "home:/home/me\ndir:.codex\n"}, map[string]string{"Ubuntu": root})
	defer undo()
	for _, a := range All() {
		if a.ID == "codex" {
			if err := a.Fields[0].Set("fake/m1"); err != nil {
				t.Fatal(err)
			}
		}
	}
	ConnectCodexTwins()
	if b, _ := os.ReadFile(cfgPath); string(b) != own {
		t.Fatalf("changed:\n%s", b)
	}
}
