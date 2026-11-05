package davsync

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/library"
)

// A hook made on one computer is written into the other's agents by its
// next sync, and taken out of them when the first takes it out.
func TestSyncHooks(t *testing.T) {
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
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	os.MkdirAll(filepath.Join(string(a), ".claude"), 0o755)
	if _, err := library.SaveHook("", library.Hook{Name: "done", Event: "turn-end", Command: "echo done", Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	Configure(cfg)
	now(t)

	b.use(t)
	os.MkdirAll(filepath.Join(string(b), ".claude"), 0o755)
	Configure(cfg)
	now(t)
	settings := filepath.Join(string(b), ".claude", "settings.json")
	if s, _ := os.ReadFile(settings); !strings.Contains(string(s), "echo done") {
		t.Fatalf("b's Claude Code wasn't given the hook: %q", s)
	}
	puts := fake.puts
	now(t)
	if fake.puts != puts {
		t.Fatalf("b pushed back the hooks it brought in (%d puts)", fake.puts-puts)
	}

	a.use(t)
	if _, err := library.RemoveHooks([]string{"done"}); err != nil {
		t.Fatal(err)
	}
	now(t)
	b.use(t)
	now(t)
	if s, _ := os.ReadFile(settings); strings.Contains(string(s), "echo done") {
		t.Errorf("the hook is still in b's Claude Code after a took it out: %q", s)
	}
}

// A library with no hooks hashes as it did before they were carried, so a
// magpie that starts carrying them doesn't see a change to sync.
func TestNoHooksHashAsBefore(t *testing.T) {
	none := []*library.Hook{}
	with := backup.Bundle{Library: &library.Bundle{MCP: []*library.Server{}, Skills: []*library.CarriedSkill{}, Hooks: &none}}
	before := backup.Bundle{Library: &library.Bundle{MCP: []*library.Server{}, Skills: []*library.CarriedSkill{}}}
	if hashes(with)["library"] != hashes(before)["library"] {
		t.Error("an empty list of hooks changed the library's hash")
	}
	one := []*library.Hook{{Name: "done", Event: "turn-end", Command: "echo done"}}
	with.Library.Hooks = &one
	if hashes(with)["library"] == hashes(before)["library"] {
		t.Error("a hook didn't change the library's hash")
	}
}
