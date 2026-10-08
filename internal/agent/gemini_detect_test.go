package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// TestGeminiNotAntigravity: a ~/.gemini holding only what Antigravity keeps
// there is no Gemini CLI (#330), nor one holding what Gemini CLI left when
// it was uninstalled (#230); its binary is.
func TestGeminiNotAntigravity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(home, ".gemini")
	for _, d := range []string{"antigravity", "config", "antigravity-cli"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	// Antigravity reads its global rules from ~/.gemini/GEMINI.md too
	os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte("rules"), 0o644)
	if gemini(home).Detected() {
		t.Fatal("Antigravity's folders taken for Gemini CLI")
	}
	if !agy(home).Detected() {
		t.Fatal("agy not detected from ~/.gemini/antigravity-cli")
	}

	// what Gemini CLI leaves behind when it is uninstalled (#230): its
	// settings, its sign-in, its ids, all of it
	os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(`{"refresh_token":"r"}`), 0o600)
	for _, f := range []string{"settings.json", ".env", "google_accounts.json", "installation_id", "trustedFolders.json", "mcp-oauth-tokens.json"} {
		os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644)
	}
	if gemini(home).Detected() {
		t.Fatal("an uninstalled Gemini CLI's leftovers taken for it")
	}

	if runtime.GOOS == "windows" {
		return
	}
	bin := t.TempDir()
	testenv.Program(t, filepath.Join(bin, "gemini"), "#!/bin/sh\n")
	t.Setenv("PATH", bin)
	if !gemini(home).Detected() {
		t.Fatal("gemini on PATH not detected")
	}
}
