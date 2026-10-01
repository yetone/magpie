package backup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// home gives the test a machine of its own: no agents, no magpie files.
func home(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "DSH_HOME", "GEMINI_CLI_HOME", "OPENCODE_CONFIG",
		"PI_CODING_AGENT_DIR", "OMO_CODING_AGENT_DIR", "SENPI_CODING_AGENT_DIR", "COPILOT_HOME", "CLINE_DIR", "GROK_HOME", "HERMES_HOME", "HANA_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(v, "")
	}
}

func setUp(t *testing.T) {
	t.Helper()
	icon, err := provider.StoreIcon([]byte("\x89PNG\r\n\x1a\n0000000000000000"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []provider.Provider{
		{ID: "acme", Name: "Acme", Chat: "https://acme.example.com/v1", Key: "sk-acme", Icon: icon,
			BalanceToken: "balance-acme",
			Keys:         []provider.KeyAccount{{Name: "second", Key: "sk-acme-2"}},
			Headers:      map[string]string{"X-Team": "a", "X-Api-Key": "hdr-secret"}},
		{ID: "beta", Name: "Beta", Chat: "https://beta.example.com/v1", Key: "sk-beta", BalanceToken: "balance-beta"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("work", profile.Profile{Fields: map[string]string{"claude.model": "acme/m1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	home(t)
	setUp(t)
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Seal(b, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-acme", "balance-acme", "hdr-secret", "acme.example.com", "work"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("%q readable in the file", secret)
		}
	}
	if _, err := Open(data, "wrong"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}

	home(t) // another machine
	got, err := Open(data, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || !r.Settings || r.Profiles != 1 || len(r.NeedKey) != 0 {
		t.Fatalf("result: %+v", r)
	}
	p, err := provider.Find("acme")
	if err != nil || p.Key != "sk-acme" || len(p.Keys) != 1 || p.BalanceToken != "balance-acme" || p.Headers["X-Api-Key"] != "hdr-secret" {
		t.Fatalf("acme: %+v %v", p, err)
	}
	name, _ := strings.CutPrefix(p.Icon, "file:")
	if _, err := os.Stat(provider.IconFile(name)); err != nil {
		t.Fatalf("icon: %v", err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Fatalf("settings: %+v", s)
	}
	if ps, _ := profile.Load(); ps["work"].Fields["claude.model"] != "acme/m1" {
		t.Fatalf("profiles: %+v", ps)
	}
}

// Without keys, nothing secret leaves; restored over a machine that has the
// provider, its keys there stay, and one new there is named as needing a key.
func TestNoKeys(t *testing.T) {
	home(t)
	setUp(t)
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) != 0 || p.BalanceToken != "" {
			t.Fatalf("key in a keyless backup: %+v", p)
		}
		if _, ok := p.Headers["X-Api-Key"]; ok {
			t.Fatalf("auth header in a keyless backup: %+v", p.Headers)
		}
	}
	if p, _ := provider.Find("acme"); p.Key != "sk-acme" || p.BalanceToken != "balance-acme" || p.Headers["X-Api-Key"] != "hdr-secret" {
		t.Fatalf("export changed the stored credentials: %+v", p)
	}
	data, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}

	home(t)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme old", Chat: "https://old.example.com/v1", Key: "sk-here", BalanceToken: "balance-here"}); err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(got, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 1 || !slices.Equal(r.NeedKey, []string{"Beta"}) || r.Settings || r.Profiles != 0 {
		t.Fatalf("result: %+v", r)
	}
	if p, _ := provider.Find("acme"); p.Key != "sk-here" || p.BalanceToken != "balance-here" || p.Chat != "https://acme.example.com/v1" || p.Headers["X-Team"] != "a" {
		t.Fatalf("acme: %+v", p)
	}
	if p, _ := provider.Find("beta"); p.BalanceToken != "" {
		t.Fatalf("balance token reached a new machine: %+v", p)
	}
}

// The library goes too: its sets, servers and skills' files; without keys
// a server's secret-looking values stay behind, and the ones on the
// machine restored to stay. A backup from before the library leaves it.
func TestLibrary(t *testing.T) {
	home(t)
	text := "Be brief."
	if _, err := library.SaveInstructions(library.InstructionsChange{Shared: &text}); err != nil {
		t.Fatal(err)
	}
	srv := library.Server{Name: "gh", Transport: "http", URL: "https://mcp.example.com",
		Headers: map[string]string{"Authorization": "Bearer lib-secret", "X-Org": "acme"}, Agents: []string{}}
	if _, err := library.SaveServer("", srv); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(os.Getenv("HOME"), "skills", "notes")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: notes\ndescription: Notes\n---\n"), 0o644)
	if _, err := library.InstallSkills(dir, []string{""}, nil); err != nil {
		t.Fatal(err)
	}
	full, err := Collect(true, "test")
	if err != nil || full.Library == nil || full.Library.MCP[0].Headers["Authorization"] != "Bearer lib-secret" {
		t.Fatalf("with keys: %+v %v", full.Library, err)
	}
	b, _ := Collect(false, "test")
	if h := b.Library.MCP[0].Headers; h["Authorization"] != "" || h["X-Org"] != "acme" {
		t.Fatalf("without keys: %v", h)
	}
	data, _ := Seal(b, "pw")
	older, _ := Seal(Bundle{Version: 1, Providers: []provider.Provider{}}, "pw")

	home(t) // another machine, with the server and its key already
	srv.Headers = map[string]string{"Authorization": "Bearer here", "X-Org": "old"}
	if _, err := library.SaveServer("", srv); err != nil {
		t.Fatal(err)
	}
	got, _ := Open(older, "pw")
	if r, err := Restore(got, All); err != nil || r.Library {
		t.Fatalf("an older backup: %+v %v", r, err)
	}
	got, err = Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if r, err := Restore(got, Parts{Library: false, Settings: true}); err != nil || r.Library {
		t.Fatalf("library not picked: %+v %v", r, err)
	}
	if l, _ := library.Collect(); l.Texts["default"] != "" {
		t.Fatal("the library came in unpicked")
	}
	r, err := Restore(got, All)
	if err != nil || !r.Library {
		t.Fatalf("restore: %+v %v", r, err)
	}
	l, _ := library.Collect()
	if l.Texts["default"] != "Be brief." || len(l.Skills) != 1 || l.Skills[0].Name != "notes" || len(l.Skills[0].Files) != 1 {
		t.Fatalf("library: %+v", l)
	}
	if h := l.MCP[0].Headers; h["Authorization"] != "Bearer here" || h["X-Org"] != "acme" {
		t.Fatalf("server: %v", h)
	}
}

func TestNotABackup(t *testing.T) {
	for _, data := range []string{"", "{}", `{"format":"magpie-backup","version":9,"kdf":"x"}`} {
		if _, err := Open([]byte(data), "pw"); err == nil || errors.Is(err, ErrPassphrase) {
			t.Fatalf("%q: %v", data, err)
		}
	}
	if _, err := Seal(Bundle{}, ""); err == nil {
		t.Fatal("sealed with no passphrase")
	}
}

func TestTampered(t *testing.T) {
	data, err := Seal(Bundle{Version: 1}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	// lowering the work of the key derivation is noticed
	bad := strings.Replace(string(data), `"iterations": 600000`, `"iterations": 100000`, 1)
	if _, err := Open([]byte(bad), "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("tampered header: %v", err)
	}
}

func TestProfileKeys(t *testing.T) {
	got := profileKeys(map[string]string{"codex.effort": "", "claude.model": "", "codex.provider": "", "codex.model": ""})
	want := []string{"codex.provider", "claude.model", "codex.model", "codex.effort"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}
