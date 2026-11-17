package davsync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/library"
)

// #6685: a plugin installed through the Library on one computer is on the
// other after its next sync, whole, with its hook naming that computer's
// folder for it; the second computer pushes nothing back, and a plugin
// taken out on the first goes from the second.
func TestSyncLibraryPlugins(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
		if v := Status(); v.Error != "" {
			t.Fatalf("sync: %+v", v)
		}
	}
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	write(filepath.Join(string(a), ".claude", "settings.json"), "{}\n")
	src := filepath.Join(string(a), "work", "kit")
	write(filepath.Join(src, ".claude-plugin", "plugin.json"), `{"name":"kit","version":"1.0.0"}`)
	write(filepath.Join(src, "hooks", "hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/done"}]}]}}`)
	write(filepath.Join(src, "bin", "done"), "#!/bin/sh\n")
	if _, err := library.InstallPlugins(src, nil, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	Configure(cfg)
	now(t)

	b.use(t)
	write(filepath.Join(string(b), ".claude", "settings.json"), "{}\n")
	Configure(cfg)
	now(t)
	v, err := library.Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Plugins) != 1 || v.Plugins[0].Name != "kit" {
		t.Fatalf("b's plugins: %+v", v.Plugins)
	}
	// as the plugin wrote it: ${CLAUDE_PLUGIN_ROOT}/bin/done, on Windows too
	inJSON := func(s string) string { j, _ := json.Marshal(s); return strings.Trim(string(j), `"`) }
	want := inJSON(v.Plugins[0].Dir + "/bin/done")
	s, _ := os.ReadFile(filepath.Join(string(b), ".claude", "settings.json"))
	if !strings.Contains(string(s), want) || strings.Contains(string(s), inJSON(string(a))) {
		t.Fatalf("b's Claude Code hook doesn't name b's folder for the plugin %s: %s", want, s)
	}
	puts := fake.puts
	now(t)
	if fake.puts != puts {
		t.Fatalf("b pushed back the plugin it brought in (%d puts)", fake.puts-puts)
	}

	a.use(t)
	if _, err := library.RemovePlugin("kit"); err != nil {
		t.Fatal(err)
	}
	now(t)
	b.use(t)
	now(t)
	if v, _ := library.Read(nil); len(v.Plugins) != 0 || len(v.Hooks) != 0 {
		t.Errorf("the plugin stays on b after a took it out: %+v", v.Plugins)
	}
	if s, _ := os.ReadFile(filepath.Join(string(b), ".claude", "settings.json")); strings.Contains(string(s), "bin/done") {
		t.Errorf("the hook stays in b's Claude Code: %s", s)
	}
}

// A library with no plugins hashes as it did before they were carried, so
// a magpie that starts carrying them doesn't see a change to sync.
func TestNoPluginsHashAsBefore(t *testing.T) {
	none := []*library.CarriedPlugin{}
	with := backup.Bundle{Library: &library.Bundle{MCP: []*library.Server{}, Skills: []*library.CarriedSkill{}, Plugins: &none}}
	before := backup.Bundle{Library: &library.Bundle{MCP: []*library.Server{}, Skills: []*library.CarriedSkill{}}}
	if hashes(with)["library"] != hashes(before)["library"] {
		t.Error("an empty list of plugins changed the library's hash")
	}
	one := []*library.CarriedPlugin{{Name: "kit", Source: "https://github.com/acme/kit", Agents: []string{"claude"}}}
	with.Library.Plugins = &one
	if hashes(with)["library"] == hashes(before)["library"] {
		t.Error("a plugin didn't change the library's hash")
	}
	items := itemsOf(with, "library")
	if it, ok := items["plugin\x00kit"]; !ok || it.Name != "kit" {
		t.Errorf("the plugin as the preview lists it: %+v", items)
	}
}
