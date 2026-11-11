package sessions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// A folder a Sessions poll looks at again, unchanged, costs its listing
// nothing and its stat no FileInfo: os.Stat's was ~4.5 MB of garbage per
// 40 polls on a real history (#2278). appdir.DirStat says what os.Stat does.
func TestDirStatAsOSStat(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	os.Chtimes(dir, old, old)
	for _, p := range []string{dir, file} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		mod, size, isDir, err := appdir.DirStat(p)
		if err != nil || !mod.Equal(st.ModTime()) || size != st.Size() || isDir != st.IsDir() {
			t.Fatalf("%s: %v %d %t %v, os.Stat %v %d %t", p, mod, size, isDir, err, st.ModTime(), st.Size(), st.IsDir())
		}
	}
	if _, _, _, err := appdir.DirStat(filepath.Join(dir, "gone")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a folder gone: %v", err)
	}
	if got := readDirectory(dir); len(got) != 1 {
		t.Fatalf("listed %d", len(got))
	}
	if runtime.GOOS == "windows" {
		return // os.Stat there
	}
	// the path's C string is all an unchanged folder's look allocates
	if n := testing.AllocsPerRun(50, func() { readDirectory(dir) }); n > 1 {
		t.Fatalf("an unchanged folder's look allocated %.0f times", n)
	}
}
