package profile

import (
	"encoding/json"
	"github.com/yetone/magpie/internal/testenv"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
)

// sandbox is a home with Claude Code and Codex in it and nothing on PATH.
func sandbox(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	// Claude Code's and Codex's CLIs are here: an agent whose CLI is gone
	// is no Library target (#2254), and these tests carry its library
	bin := filepath.Join(h, "bin")
	for _, cli := range []string{"claude", "codex"} {
		testenv.Program(t, filepath.Join(bin, cli), "#!/bin/sh\n")
		write(t, filepath.Join(bin, cli+".exe"), "")
	}
	t.Setenv("PATH", bin)
	for _, k := range agentenv.Vars {
		t.Setenv(k, "")
	}
	t.Setenv("APPDATA", "")
	t.Setenv("LOCALAPPDATA", "")
	for _, f := range []string{".claude/settings.json", ".codex/config.toml"} {
		write(t, filepath.Join(h, f), "")
	}
	return h
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// A profiles file from before profiles kept the library reads, lists and
// writes back as it was, and applying one leaves the library alone.
func TestOldProfiles(t *testing.T) {
	h := sandbox(t)
	old := `{
  "work": {
    "claude.model": "acme/m1",
    "codex.model": "acme/m2"
  }
}
`
	write(t, Path(), old)
	ps := must[map[string]Profile](t)(Load())
	p := ps["work"]
	if p.Library != nil || p.Fields["claude.model"] != "acme/m1" || len(p.Fields) != 2 {
		t.Fatalf("work: %+v", p)
	}
	if s := LongSummary(p); s != "claude acme/m1 · codex acme/m2" {
		t.Errorf("summary %q", s)
	}
	if err := store(ps); err != nil {
		t.Fatal(err)
	}
	if s := read(t, Path()); s != old {
		t.Errorf("written back as\n%s", s)
	}
	shared := "Use tabs."
	must[*library.Result](t)(library.SaveInstructions(library.InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	a := must[Applied](t)(Apply(p))
	if a.Library != nil || len(Report(a)) != 0 {
		t.Errorf("applied %+v", a)
	}
	if !strings.Contains(read(t, filepath.Join(h, ".claude/CLAUDE.md")), "Use tabs.") {
		t.Error("an old profile changed the library")
	}
}

func TestProfileCarriesLibrary(t *testing.T) {
	h := sandbox(t)
	// an empty library: a profile carries none of it
	p := must[Profile](t)(Snapshot())
	if p.Library != nil {
		t.Fatalf("empty library taken: %+v", p.Library)
	}

	must[*library.Result](t)(library.SaveServer("", library.Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"claude"}}))
	must[*library.Result](t)(library.SaveServer("", library.Server{Name: "gone", Transport: "stdio", Command: "gone", Agents: []string{"codex"}}))
	shared := "Use tabs."
	must[*library.Result](t)(library.SaveInstructions(library.InstructionsChange{Shared: &shared, Agents: []string{"claude", "codex"}}))
	if err := Save("a", must[Profile](t)(Snapshot())); err != nil {
		t.Fatal(err)
	}

	other := "Use spaces."
	must[*library.Result](t)(library.SaveInstructions(library.InstructionsChange{Shared: &other, Agents: []string{"codex"}}))
	must[*library.Result](t)(library.ServerAgents("fs", []string{"codex"}))
	if err := Save("b", must[Profile](t)(Snapshot())); err != nil {
		t.Fatal(err)
	}
	must[*library.Result](t)(library.RemoveServer("gone"))

	// the file keeps the fields as before, with the library beside them
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(read(t, Path())), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["a"]["library"]; !ok {
		t.Fatalf("no library in\n%s", read(t, Path()))
	}

	ps := must[map[string]Profile](t)(Load())
	if s := LongSummary(ps["a"]); s != "+ library: 2 servers, instructions" {
		t.Errorf("summary %q", s)
	}
	a := must[Applied](t)(Apply(ps["a"]))
	if a.Library == nil || !slices.Equal(a.Library.Missing, []string{"mcp:gone"}) {
		t.Fatalf("applied %+v", a.Library)
	}
	if r := Report(a); len(r) == 0 || !strings.Contains(strings.Join(r, "\n"), "skipped, no longer in the library: mcp:gone") {
		t.Errorf("report %q", r)
	}
	cl := read(t, filepath.Join(h, ".claude/CLAUDE.md"))
	if !strings.Contains(cl, "Use tabs.") {
		t.Errorf("claude:\n%s", cl)
	}
	if !strings.Contains(read(t, filepath.Join(h, ".claude.json")), `"fs"`) {
		t.Error("claude has no fs again")
	}
	if strings.Contains(read(t, filepath.Join(h, ".codex/config.toml")), "mcp_servers.fs") {
		t.Error("codex kept fs")
	}

	must[Applied](t)(Apply(ps["b"]))
	if s := read(t, filepath.Join(h, ".claude/CLAUDE.md")); strings.Contains(s, "Use") {
		t.Errorf("claude after b:\n%s", s)
	}
	if !strings.Contains(read(t, filepath.Join(h, ".codex/AGENTS.md")), "Use spaces.") {
		t.Error("codex after b")
	}
	if !strings.Contains(read(t, filepath.Join(h, ".codex/config.toml")), "mcp_servers.fs") {
		t.Error("codex has no fs after b")
	}
}

// A profile can name agents this computer doesn't have: profiles sync
// between computers. Applying one leaves those agents alone, so it never
// writes a config that makes an agent look installed here (qtwaiter on X).
func TestApplySkipsUndetectedAgents(t *testing.T) {
	sandbox(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1",
		Key: "synthetic-key", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	p := Profile{Fields: map[string]string{"claude.model": "relay/pro"}}
	for _, a := range agent.All() {
		if a.Detected() || a.Field("model") == nil {
			continue
		}
		p.Fields[a.ID+".model"] = "relay/pro"
	}
	if len(p.Fields) < 2 {
		t.Fatal("no undetected agent with a model field")
	}
	_, err := Apply(p)
	if got := must[*agent.Agent](t)(agent.Find("claude")).Field("model").Get(); got != "relay/pro" {
		t.Errorf("claude's model is %q", got)
	}
	for k := range p.Fields {
		a := must[*agent.Agent](t)(agent.Find(strings.TrimSuffix(k, ".model")))
		if a.ID != "claude" && a.Detected() {
			t.Errorf("applying the profile made %s look installed", a.ID)
		}
	}
	if err != nil {
		t.Error(err)
	}
}

// Rename moves a profile's key without snapshotting it again: what it held
// is the same under the new name, a name differing only in case is a rename,
// and each refusal writes nothing (#2721, erlinerd).
func TestRename(t *testing.T) {
	sandbox(t)
	work := Profile{Fields: map[string]string{"codex.model": "gpt-5"}, Library: &library.Setup{}}
	if err := Save("work", work); err != nil {
		t.Fatal(err)
	}
	if err := Save("taken", Profile{Fields: map[string]string{"claude.model": "acme/m1"}}); err != nil {
		t.Fatal(err)
	}
	keys := func() []string { return Names(must[map[string]Profile](t)(Load())) }

	if err := Rename("  work ", " home  "); err != nil {
		t.Fatal(err)
	}
	ps := must[map[string]Profile](t)(Load())
	if got := Names(ps); !slices.Equal(got, []string{"home", "taken"}) {
		t.Fatalf("after work → home: %v", got)
	}
	if got := ps["home"]; got.Fields["codex.model"] != "gpt-5" || got.Library == nil {
		t.Errorf("home lost what work held: %+v", got)
	}
	if err := Rename("home", "Home"); err != nil {
		t.Fatal(err)
	}
	if got := keys(); !slices.Equal(got, []string{"Home", "taken"}) {
		t.Errorf("a rename by case only: %v", got)
	}

	before := read(t, Path())
	for _, c := range []struct{ old, to, err string }{
		{"Home", "   ", "profile name is empty"},
		{"Home", "taken", `a profile named "taken" already exists`},
		{"nope", "other", `no profile named "nope"`},
		{"Home", "Home", ""},
	} {
		err := Rename(c.old, c.to)
		if (err == nil) != (c.err == "") || (err != nil && err.Error() != c.err) {
			t.Errorf("Rename(%q, %q) = %v, want %q", c.old, c.to, err, c.err)
		}
		if got := read(t, Path()); got != before {
			t.Errorf("Rename(%q, %q) wrote profiles.json: %s", c.old, c.to, got)
		}
	}

	// profiles.json unreadable (here a folder): an error, nothing written
	sandbox(t)
	if err := os.MkdirAll(Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Rename("Home", "moved"); err == nil {
		t.Error("a rename over an unreadable profiles.json went through")
	}
	if fi, err := os.Stat(Path()); err != nil || !fi.IsDir() {
		t.Errorf("an unreadable profiles.json was written over: %v", err)
	}
}
