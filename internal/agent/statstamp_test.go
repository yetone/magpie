package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// statStamp and statFile say what os.Stat would, for a file, a folder, a
// link and a gone path, and make no os.FileInfo for it on unix.
func TestStatStampAndFileSayWhatStatSays(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Info.plist")
	if err := os.WriteFile(file, []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.UTC)
	os.Chtimes(file, at, at)
	link := filepath.Join(dir, "link")
	os.Symlink(file, link)
	for _, p := range []string{file, dir, link, filepath.Join(dir, "gone"), filepath.Join(file, "under")} {
		mod, size, ok := statStamp(p)
		st, err := os.Stat(p)
		if ok != (err == nil) {
			t.Fatalf("%s: ok %v, os.Stat err %v", p, ok, err)
		}
		if err == nil && (!mod.Equal(st.ModTime()) || size != st.Size()) {
			t.Fatalf("%s: %v %d, os.Stat %v %d", p, mod, size, st.ModTime(), st.Size())
		}
		if want := err == nil && st.Mode().IsRegular(); statFile(p) != want {
			t.Fatalf("%s: statFile %v, want %v", p, !want, want)
		}
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	// the path's bytes for the system call are all a look allocates
	if n := testing.AllocsPerRun(20, func() { statStamp(file); statFile(file) }); n > 2 {
		t.Fatalf("%v allocations for two looks", n)
	}
}
