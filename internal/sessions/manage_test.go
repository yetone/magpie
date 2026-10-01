package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// manageSetup is setup with magpie's own folder in the temp dir too, and
// every session file a day old, so none counts as running.
func manageSetup(t *testing.T) (claude, codex string) {
	claude, codex = setup(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	old := time.Now().Add(-24 * time.Hour)
	for _, root := range []string{claude, codex} {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil {
				os.Chtimes(p, old, old)
			}
			return nil
		})
	}
	return
}

const mgCC = "11111111-2222-3333-4444-555555555555"
const mgCX = "01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd"

func findManaged(list []Managed, id string) (Managed, bool) {
	for _, m := range list {
		if m.ID == id {
			return m, true
		}
	}
	return Managed{}, false
}

func TestListAgent(t *testing.T) {
	claude, _ := manageSetup(t)
	cc := ListAgent("claude")
	if len(cc) != 2 {
		t.Fatalf("claude sessions: %d, want 2", len(cc))
	}
	m, ok := findManaged(cc, mgCC)
	if !ok {
		t.Fatal("the session with subagents isn't listed")
	}
	if m.Cwd == "" || m.Title == "" || !m.Deletable || m.Files != 2 || m.Messages == 0 {
		t.Fatalf("listed as %+v", m)
	}
	fi1, _ := os.Stat(filepath.Join(claude, "projects", "-work-app", mgCC+".jsonl"))
	fi2, _ := os.Stat(filepath.Join(claude, "projects", "-work-app", mgCC, "subagents", "agent-a1.jsonl"))
	if m.Size != fi1.Size()+fi2.Size() {
		t.Fatalf("size %d, want %d", m.Size, fi1.Size()+fi2.Size())
	}
	if !strings.Contains(m.Resume, "claude --resume "+mgCC) {
		t.Fatalf("resume %q", m.Resume)
	}
	cx := ListAgent("codex")
	if len(cx) != 1 || cx[0].ID != mgCX || cx[0].Files != 2 || !strings.Contains(cx[0].Resume, "codex resume "+mgCX) {
		t.Fatalf("codex: %+v", cx)
	}
	agents := Agents()
	got := map[string]int{}
	for _, a := range agents {
		got[a.Agent] = a.Count
	}
	if got["claude"] != 2 || got["codex"] != 1 {
		t.Fatalf("agents %+v", agents)
	}
}

func TestDeleteRestoreClaude(t *testing.T) {
	claude, _ := manageSetup(t)
	proj := filepath.Join(claude, "projects", "-work-app")
	// what Claude Code keeps by the session's id elsewhere
	for _, p := range []string{
		filepath.Join(claude, "file-history", mgCC, "a@v1"),
		filepath.Join(claude, "todos", mgCC+"-agent-"+mgCC+".json"),
		filepath.Join(claude, "session-env", mgCC, "env"),
		filepath.Join(claude, "todos", "99999999-agent-x.json"), // another session's
	} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		old := time.Now().Add(-time.Hour)
		os.Chtimes(p, old, old)
		os.Chtimes(filepath.Dir(p), old, old)
	}
	tr, err := Delete("claude", mgCC)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(proj, mgCC+".jsonl"), filepath.Join(proj, mgCC), filepath.Join(claude, "file-history", mgCC),
		filepath.Join(claude, "session-env", mgCC), filepath.Join(claude, "todos", mgCC+"-agent-"+mgCC+".json")} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s is still there", p)
		}
	}
	for _, p := range []string{filepath.Join(proj, "22222222-2222-3333-4444-555555555555.jsonl"), filepath.Join(claude, "todos", "99999999-agent-x.json")} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("%s was moved too", p)
		}
	}
	if _, ok := findManaged(ListAgent("claude"), mgCC); ok {
		t.Fatal("still listed")
	}
	// it is in magpie's trash, not gone
	trash := Trash()
	if len(trash) != 1 || trash[0].Key != tr.Key || trash[0].ID != mgCC || trash[0].Size == 0 {
		t.Fatalf("trash %+v", trash)
	}
	if !strings.HasPrefix(filepath.Join(TrashDir(), tr.Key), os.Getenv("XDG_CONFIG_HOME")) {
		t.Fatalf("trash in %s", TrashDir())
	}
	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(proj, mgCC+".jsonl"), filepath.Join(proj, mgCC, "subagents", "agent-a1.jsonl"),
		filepath.Join(claude, "file-history", mgCC, "a@v1"), filepath.Join(claude, "todos", mgCC+"-agent-"+mgCC+".json")} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("%s wasn't restored", p)
		}
	}
	if m, ok := findManaged(ListAgent("claude"), mgCC); !ok || m.Files != 2 {
		t.Fatalf("restored session listed as %+v, %v", m, ok)
	}
	if len(Trash()) != 0 {
		t.Fatal("the trash still has it")
	}
	if _, err := Restore(tr.Key); err == nil {
		t.Fatal("restored twice")
	}
	if _, err := Restore("claude/../../x"); err == nil {
		t.Fatal("a key out of the trash")
	}
}

func TestDeleteRestoreCodex(t *testing.T) {
	_, codex := manageSetup(t)
	idx := filepath.Join(codex, "session_index.jsonl")
	os.WriteFile(idx, []byte(`{"id":"`+mgCX+`","thread_name":"x"}`+"\n"), 0o644)
	tr, err := Delete("codex", mgCX)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Items) != 2 {
		t.Fatalf("moved %+v, want both segments", tr.Items)
	}
	if left, _ := filepath.Glob(filepath.Join(codex, "sessions", "*", "*", "*", "*.jsonl")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	if b, _ := os.ReadFile(idx); !strings.Contains(string(b), mgCX) {
		t.Fatal("Codex's own index was changed")
	}
	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	if len(ListAgent("codex")) != 1 {
		t.Fatal("not restored")
	}
}

// A session written to within the last minute may still be running: it is
// left as it is.
func TestDeleteActiveRefused(t *testing.T) {
	claude, _ := manageSetup(t)
	sub := filepath.Join(claude, "projects", "-work-app", mgCC, "subagents", "agent-a1.jsonl")
	now := time.Now()
	os.Chtimes(sub, now, now)
	if _, err := Delete("claude", mgCC); !errors.Is(err, ErrActive) {
		t.Fatalf("err %v, want ErrActive", err)
	}
	if _, err := os.Stat(filepath.Join(claude, "projects", "-work-app", mgCC+".jsonl")); err != nil {
		t.Fatal("the running session was moved")
	}
	if len(Trash()) != 0 {
		t.Fatal("something went to the trash")
	}
	// a minute later it can go
	old := now.Add(-2 * time.Minute)
	os.Chtimes(sub, old, old)
	if _, err := Delete("claude", mgCC); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteOnlyKnownAgents(t *testing.T) {
	manageSetup(t)
	if _, err := Delete("opencode", "x"); err == nil {
		t.Fatal("deleted from a database magpie doesn't write")
	}
	if _, err := Delete("claude", "../x"); err == nil {
		t.Fatal("an id that isn't one")
	}
	if _, err := Delete("claude", "nope"); err == nil {
		t.Fatal("no such session")
	}
}
