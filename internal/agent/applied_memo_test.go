package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// appliedOf is asked for every agent on every drift poll: an unchanged
// applied.json is parsed once, and magpie's own record is read at once.
func TestAppliedOfParsesAnUnchangedFileOnce(t *testing.T) {
	stashHome(t)
	p := appliedPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(`{"codex":{"fields":{"model":"magpie/a"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(p, old, old)
	if got := appliedOf("codex").Fields["model"]; got != "magpie/a" {
		t.Fatalf("model = %q", got)
	}
	release := filememo.Hold()
	defer release()
	appliedOf("codex")
	if n := testing.AllocsPerRun(20, func() { appliedOf("codex") }); n > 2 {
		t.Fatalf("%v allocations for an unchanged file", n)
	}
	record("codex", "model", "magpie/b")
	if got := appliedOf("codex").Fields["model"]; got != "magpie/b" {
		t.Fatalf("after record, model = %q", got)
	}
	os.Remove(p)
	filememo.Look(p)
	if got := appliedOf("codex"); got.Fields != nil {
		t.Fatalf("gone file: %+v", got)
	}
}
