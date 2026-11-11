//go:build darwin && cgo

package dirent

import (
	"os"
	"path/filepath"
	"testing"
)

// A Sessions poll watches each of its folders in turn, and each Watch
// wrote, waited on and removed a probe (#2278). Watches from one mark take
// one probe between them, and a change made before the mark is still seen
// by the next read; a Watch from a later mark probes again.
func TestWatchesFromOneMarkProbeOnce(t *testing.T) {
	root, day, rollout := dayFolder(t)
	other := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	Watch(other)
	if !quiet(t, root, day) {
		t.Fatal("an unchanged watched folder was read again")
	}
	probes := func() int { watched.Lock(); defer watched.Unlock(); return watched.probe.n }
	for i := range 20 {
		f, err := os.OpenFile(rollout, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(`{"type":"event"}` + "\n")
		f.Close()
		aged(t, rollout)
		fi, _ := os.Stat(rollout)
		mark, before := Mark(), probes()
		WatchSince(root, mark)
		WatchSince(other, mark)
		WatchSince(root, mark)
		if n := probes() - before; n != 1 {
			t.Fatalf("round %d: %d probes from one mark", i, n)
		}
		l, _ := statsOf(t, day)
		if s, _ := l.Get("rollout-a.jsonl"); s.Size != fi.Size() {
			t.Fatalf("round %d: read %d bytes, the file has %d", i, s.Size, fi.Size())
		}
	}
	before := probes()
	WatchSince(other, Mark())
	if probes() == before {
		t.Fatal("a later mark took no probe of its own")
	}
}
