package backup

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/memory"
)

func saveMemory(t *testing.T, text string) *memory.Memory {
	t.Helper()
	k, s := memory.Preference, memory.Global
	m, _, err := memory.Save(memory.Input{Text: &text, Kind: &k, Scope: &s}, memory.FromUser, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func memoryTexts(t *testing.T) []string {
	t.Helper()
	all, _, err := memory.All()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range all {
		out = append(out, m.Text)
	}
	return out
}

// A backup carries memory, and putting it back merges it with what is
// here: a memory only this computer has stays.
func TestBackupMergesMemory(t *testing.T) {
	home(t)
	saveMemory(t, "Replies in Chinese")
	b, err := Collect(false, "test") // a backup without keys still has it
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(b, "pass")
	if err != nil {
		t.Fatal(err)
	}

	home(t) // another computer
	saveMemory(t, "Builds run on mote")
	opened, err := Open(sealed, "pass")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(opened, Parts{Memory: true})
	if err != nil || !r.Memory {
		t.Fatalf("restore: %+v, %v", r, err)
	}
	if got := strings.Join(memoryTexts(t), "|"); !strings.Contains(got, "Replies in Chinese") || !strings.Contains(got, "Builds run on mote") {
		t.Fatalf("memory after the restore: %s", got)
	}
	// put back again: nothing changes
	if r, err := Restore(opened, Parts{Memory: true}); err != nil || r.Memory {
		t.Fatalf("a second restore changed memory: %+v, %v", r, err)
	}
	// left out when not asked for
	home(t)
	if _, err := Restore(opened, Parts{Library: true}); err != nil {
		t.Fatal(err)
	}
	if got := memoryTexts(t); len(got) != 0 {
		t.Fatalf("memory put in unasked: %v", got)
	}
}

// A memory.json that can't be read stops the backup: carried as no
// memory, it would say there was none.
func TestCollectStopsOnUnreadableMemory(t *testing.T) {
	home(t)
	saveMemory(t, "kept")
	if b, err := Collect(true, "test"); err != nil || b.Memory == nil || len(b.Memory.Memories) != 1 {
		t.Fatalf("collect: %+v, %v", b.Memory, err)
	}
	if err := os.WriteFile(memory.Path(), []byte(`{"memories":[{"id":"m_1`), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(lastgood.Bak(memory.Path()))
	if _, err := Collect(true, "test"); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("Collect read a memory.json that can't be read: %v", err)
	}
	// and no memory.json is no memory, not an error
	os.Remove(memory.Path())
	if b, err := Collect(true, "test"); err != nil || b.Memory != nil {
		t.Fatalf("no memory.json: %+v, %v", b.Memory, err)
	}
}
