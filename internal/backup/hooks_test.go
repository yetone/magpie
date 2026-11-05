package backup

import (
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// A backup file carries the library's hooks, and restoring it on another
// computer puts them in its library; an older file, made before hooks were
// carried, leaves the ones there.
func TestBackupFileCarriesHooks(t *testing.T) {
	home(t)
	if _, err := library.SaveHook("", library.Hook{Name: "done", Event: "turn-end", Command: "echo done", Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := Seal(b, "pw")
	old := b
	lib := *b.Library
	lib.Hooks = nil // as a magpie before them sealed it
	old.Library = &lib
	older, _ := Seal(old, "pw")

	home(t)
	if _, err := library.SaveHook("", library.Hook{Name: "mine", Event: "prompt", Command: "echo mine", Agents: []string{}}); err != nil {
		t.Fatal(err)
	}
	got, _ := Open(older, "pw")
	if _, err := Restore(got, All); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		l, _ := library.Collect()
		var n []string
		for _, h := range *l.Hooks {
			n = append(n, h.Name)
		}
		return n
	}
	if n := names(); len(n) != 1 || n[0] != "mine" {
		t.Fatalf("an older file changed the hooks: %v", n)
	}
	got, _ = Open(data, "pw")
	if r, err := Restore(got, All); err != nil || !r.Library {
		t.Fatalf("restore: %+v %v", r, err)
	}
	l, _ := library.Collect()
	if h := *l.Hooks; len(h) != 1 || h[0].Name != "done" || h[0].Command != "echo done" || len(h[0].Agents) != 1 || h[0].Agents[0] != "claude" {
		t.Fatalf("hooks restored: %+v", h)
	}
}
