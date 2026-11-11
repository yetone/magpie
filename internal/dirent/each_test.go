package dirent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Each gives every entry of a folder as os.Lstat has it, stops when told,
// and makes nothing for an entry: Codex's idle check reads every session
// folder with it on each look.
func TestEachIsLstatWithNothingMade(t *testing.T) {
	dir := t.TempDir()
	for i := range 300 {
		p := filepath.Join(dir, "rollout-"+string(rune('a'+i%26))+string(rune('a'+i/26))+".jsonl")
		if err := os.WriteFile(p, make([]byte, i), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := 0
	ok := Each(dir, func(name string, s Stat) bool {
		seen++
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Size != fi.Size() && !fi.IsDir() || s.Mod != fi.ModTime().UnixNano() || fi.IsDir() != (s.Mode == os.ModeDir) {
			t.Fatalf("%s: %+v, lstat %d %v %v", name, s, fi.Size(), fi.ModTime(), fi.Mode())
		}
		return true
	})
	if runtime.GOOS != "darwin" {
		if ok {
			t.Fatal("a listing where the system has none")
		}
		return
	}
	if !ok || seen != 301 {
		t.Fatalf("ok %v, %d entries, want 301", ok, seen)
	}
	n := 0
	if !Each(dir, func(string, Stat) bool { n++; return n < 3 }) || n != 3 {
		t.Fatalf("told to stop at the third, gave %d", n)
	}
	var mods int64
	allocs := testing.AllocsPerRun(20, func() {
		Each(dir, func(name string, s Stat) bool { mods += s.Mod + int64(len(name)); return true })
	})
	if allocs > 4 {
		t.Fatalf("%v allocations a listing of 301 entries; want none a entry", allocs)
	}
	if Each(filepath.Join(dir, "gone"), func(string, Stat) bool { return true }) {
		t.Fatal("a folder that isn't there listed")
	}
}
