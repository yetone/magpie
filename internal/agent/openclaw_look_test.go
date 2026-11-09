package agent

import (
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// The model's Get and Check are asked on every drift poll: an unchanged
// openclaw.json is made plain JSON and checked once, and a write, magpie's
// or another program's, is read at once.
func TestOpenClawLookReadsAnUnchangedFileOnce(t *testing.T) {
	home, path := openClawHomeWith(t, openClawConfig)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(path, old, old)
	a := openClaw(home)
	f := a.Field("model")
	if got := f.Get(); got != "deepseek/deepseek-v4-pro" {
		t.Fatalf("model = %q", got)
	}
	release := filememo.Hold()
	defer release()
	openClawLook(path)
	if n := testing.AllocsPerRun(20, func() { openClawLook(path) }); n > 0 {
		t.Fatalf("%v allocations for an unchanged file", n)
	}
	if err := os.WriteFile(path, []byte(`{"agents":{"defaults":{"model":"kimi/k2"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	filememo.Look(path)
	if got := f.Get(); got != "kimi/k2" {
		t.Fatalf("after another program's write, model = %q", got)
	}
	os.WriteFile(path, []byte(`{"agents":`), 0o600)
	filememo.Look(path)
	if a.Check() == "" {
		t.Fatal("a file that isn't JSON is checked as fine")
	}
	os.Remove(path)
	filememo.Look(path)
	if s, err := openClawLook(path); s != nil || err != nil {
		t.Fatalf("gone file: %q %v", s, err)
	}
}
