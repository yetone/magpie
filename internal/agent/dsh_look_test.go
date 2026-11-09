package agent

import (
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// A look at a patch list (the window's drift poll asks each profile's
// several times) parses it once while it is unchanged, and a change
// another process makes — dsh's own save — is read, not the kept parse.
func TestDshLookParsesAnUnchangedListOnce(t *testing.T) {
	_, dir, web := dshRouteHome(t)
	list := "- id: agent-default-model\n  config:\n    provider: deepseek-official\n    model: deepseek-v4-flash\n    reasoningEffort: low\n"
	if err := os.WriteFile(web, []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute) // past the moment a write is told apart by its bytes
	os.Chtimes(web, old, old)
	if got := dshGet(dir, ""); got != "deepseek-v4-flash" {
		t.Fatalf("model %q", got)
	}
	defer filememo.Hold()()
	if n := testing.AllocsPerRun(20, func() { dshLook(web) }); n != 0 {
		t.Fatalf("an unchanged list was parsed again: %v allocations a look", n)
	}
	// dsh saves another pick: the next look has it
	if err := os.WriteFile(web, []byte(list+"- id: agent-default-model\n  config:\n    provider: deepseek-official\n    model: deepseek-v4-pro\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	filememo.Look(web)
	if got := dshGet(dir, ""); got != "deepseek-v4-pro" {
		t.Fatalf("after dsh's save: model %q", got)
	}
	// a list that is gone reads as empty, as dshRead's
	if err := os.Remove(web); err != nil {
		t.Fatal(err)
	}
	filememo.Look(web)
	if _, items, err := dshLook(web); err != nil || items != nil {
		t.Fatalf("gone: %v %v", items, err)
	}
}
