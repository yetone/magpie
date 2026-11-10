package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Once nothing has looked at the parsed dsh entries and patch lists for
// dshIdle they are let go, and the next look parses them again with the
// same answer; looked at just now, they stay.
func TestIdleDshParsesLetGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi.patch.yml")
	text := "- id: " + dshPiRow + "\n  name: pi\n  config:\n    model: m1\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	look := func() string {
		_, items, err := dshLook(path)
		if err != nil || len(items) != 1 {
			t.Fatalf("look: %d items, %v", len(items), err)
		}
		return dshConfig(items[0])["model"]
	}
	size := func() int { dshNodes.Lock(); defer dshNodes.Unlock(); return dshNodes.size }
	if got := look(); got != "m1" || size() == 0 {
		t.Fatalf("before: model %q, %d entries parsed", got, size())
	}
	_, before, _ := dshLook(path)
	var armed int
	dshAfter = func(time.Duration, func()) { armed++ }
	t.Cleanup(func() { dshAfter = func(d time.Duration, f func()) { time.AfterFunc(d, f) } })

	dshKept.last.Store(time.Now().UnixNano())
	dshKept.armed.Store(true)
	dshIdleCheck() // looked at just now: kept, looked at again when due
	if size() == 0 || armed != 1 {
		t.Fatalf("looked at just now: %d parsed entries, %d checks armed; want kept and one", size(), armed)
	}

	dshKept.last.Store(time.Now().Add(-2 * dshIdle).UnixNano())
	dshIdleCheck()
	if n := size(); n != 0 || dshKept.armed.Load() {
		t.Fatalf("idle: %d parsed entries kept, armed %v", n, dshKept.armed.Load())
	}
	_, after, _ := dshLook(path)
	if &after[0] == &before[0] {
		t.Error("the patch list kept while idle: the look after gave the same parse")
	}
	if got := look(); got != "m1" {
		t.Errorf("after: model %q, want m1", got)
	}
}
