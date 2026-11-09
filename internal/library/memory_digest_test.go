package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/memory"
)

func memSave(t *testing.T, text, kind, scope string) {
	t.Helper()
	if _, _, err := memory.Save(memory.Input{Text: &text, Kind: &kind, Scope: &scope}, memory.FromUser, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// Memory's published digest is written into a ticked agent's part after
// the shared text, with what is kept for that agent alone; an agent not
// ticked gets none. A save shows only once published, and a memory.json
// that can't be read leaves the part as it was (unknown, not empty).
// NoMemory leaves the digest out.
func TestMemoryDigestInInstructions(t *testing.T) {
	h := sandbox(t)
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex"}}))
	codex := filepath.Join(h, ".codex/AGENTS.md")
	if res, err := PublishMemory(true); err != nil || res != nil {
		t.Fatalf("nothing kept, but published: %+v %v", res, err)
	}
	if _, err := os.Stat(memory.Path()); err == nil {
		t.Fatal("a due publish with nothing kept wrote memory.json")
	}

	memSave(t, "Answer in Chinese.", memory.Preference, memory.Global)
	memSave(t, "Codex runs tests on mote.", memory.Rule, "agent:codex")
	memSave(t, "Claude keeps notes short.", memory.Rule, "agent:claude")
	if s := read(t, codex); strings.Contains(s, "Answer in Chinese.") {
		t.Fatalf("written before it was published:\n%s", s)
	}
	ok(t)(PublishMemory(false))
	s := read(t, codex)
	if !strings.Contains(s, "Be brief.") || !strings.Contains(s, "Answer in Chinese.") || !strings.Contains(s, "Codex runs tests on mote.") || strings.Contains(s, "Claude keeps notes") {
		t.Fatalf("codex's instructions:\n%s", s)
	}
	if strings.Index(s, "Be brief.") > strings.Index(s, "Answer in Chinese.") {
		t.Errorf("the digest before the shared text:\n%s", s)
	}
	if c := filepath.Join(h, ".claude/CLAUDE.md"); strings.Contains(readMaybe(c), "Answer in Chinese.") {
		t.Errorf("claude isn't ticked, but was given memory:\n%s", readMaybe(c))
	}

	// memory.json unreadable: the part is left as it is
	memSave(t, "Use tabs.", memory.Preference, memory.Global)
	if _, err := memory.Publish(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memory.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(memory.Path() + ".bak")
	res, err := changeInstructions(func(*Library) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, codex); got != s {
		t.Fatalf("an unreadable memory.json changed the part:\n%s", got)
	}
	if len(res.Problems) == 0 {
		t.Errorf("an unreadable memory.json wasn't said: %+v", res)
	}
	if err := os.Remove(memory.Path()); err != nil {
		t.Fatal(err)
	}

	// readable again: the part is back, then NoMemory takes it out
	memSave(t, "Answer in Chinese.", memory.Preference, memory.Global)
	ok(t)(PublishMemory(false))
	if got := read(t, codex); !strings.Contains(got, "## Memory") {
		t.Fatalf("memory readable again, but no digest:\n%s", got)
	}
	ok(t)(changeInstructions(func(l *Library) error { l.Instructions.NoMemory = true; return nil }))
	if got := read(t, codex); !strings.Contains(got, "Be brief.") || strings.Contains(got, "## Memory") {
		t.Fatalf("NoMemory:\n%s", got)
	}
}

func readMaybe(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}
