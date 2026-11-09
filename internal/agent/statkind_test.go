package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// isDir, isFile and Taken answer as os.Stat would, and on unix make no
// os.FileInfo: detection asks them of some thousand paths a drift ask.
func TestStatKindAnswersWithoutAFileInfo(t *testing.T) {
	d := t.TempDir()
	f := filepath.Join(d, "f")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "to-dir")
	linked := os.Symlink(d, link) == nil
	gone := filepath.Join(d, "gone")
	for _, c := range []struct {
		p         string
		dir, file bool
		taken     bool
	}{
		{d, true, false, false},
		{f, false, true, true},
		{gone, false, false, false},
		{filepath.Join(f, "under"), false, false, true},
		{"", false, false, false},
	} {
		if isDir(c.p) != c.dir || isFile(c.p) != c.file || Taken(c.p) != c.taken {
			t.Errorf("%q: isDir %v isFile %v Taken %v", c.p, isDir(c.p), isFile(c.p), Taken(c.p))
		}
	}
	if linked && !isDir(link) {
		t.Error("a link to a folder is a folder, as os.Stat follows it")
	}
	if runtime.GOOS == "windows" {
		return
	}
	// the path made a C string is all there is to it
	if n := testing.AllocsPerRun(50, func() { isDir(d); isFile(f) }); n > 2 {
		t.Fatalf("%v allocations for two looks", n)
	}
}
