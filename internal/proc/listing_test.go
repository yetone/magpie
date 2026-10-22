package proc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// countLooks counts the commands looked for on PATH for real, from here on.
func countLooks(t *testing.T) *int {
	n := 0
	old := execLookPath
	execLookPath = func(name string) (string, error) { n++; return old(name) }
	t.Cleanup(func() { execLookPath = old })
	return &n
}

// A command that isn't installed is answered from each PATH folder's
// listing, read once: before, each was a stat of every name it could have
// in every folder, asked again for about a hundred agents whenever any
// folder changed, and the Library's first change after a start or an idle
// spell took 5–8s on Windows (#1624, Hipple).
func TestMissingCommandsReadEachFolderOnce(t *testing.T) {
	var dirs []string
	for i := 0; i < 40; i++ {
		d := t.TempDir()
		os.WriteFile(filepath.Join(d, fmt.Sprintf("tool%d.exe", i)), []byte("x"), 0o755)
		dirs = append(dirs, d)
	}
	settled(t, dirs...)
	t.Setenv("PATH", joinList(dirs))
	t.Setenv("NoDefaultCurrentDirectoryInExePath", "1")
	lookWindowFor(t, lookFresh)
	looks := countLooks(t)
	read := listingRead
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("magpie-missing-%d", i)
		if p, err := Look(name); err == nil {
			t.Fatalf("%s found at %s", name, p)
		} else if !isNotFound(err) {
			t.Fatalf("%s: %v, want exec.ErrNotFound", name, err)
		}
		if p := FindTool(name); p != "" {
			t.Fatalf("FindTool %s = %s", name, p)
		}
	}
	if *looks != 0 {
		t.Errorf("100 missing commands were looked for on PATH %d times, want 0", *looks)
	}
	if got := listingRead - read; got > len(dirs)+len(findDirs())+1 {
		t.Errorf("read %d folders for 100 commands, want each of the %d once", got, len(dirs))
	}
}

// One that is there, under any of its extensions or another case, is
// looked for as before.
func TestCommandListedIsLookedFor(t *testing.T) {
	d := t.TempDir()
	t.Setenv("PATH", d)
	lookWindowFor(t, 0)
	want := installTool(t, d, "magpie-fake-agent")
	looks := countLooks(t)
	if p, err := Look("magpie-fake-agent"); err != nil || p != want {
		t.Fatalf("Look = %q, %v; want %q", p, err, want)
	}
	if *looks != 1 {
		t.Fatalf("looked %d times, want 1", *looks)
	}
	for _, n := range []string{"magpie-fake-agent.cmd", "Magpie-Fake-Agent.EXE"} {
		os.WriteFile(filepath.Join(d, n), nil, 0o755)
		if !mayBeIn("magpie-fake-agent", []string{d}) {
			t.Errorf("with %s there, magpie-fake-agent isn't thought to be", n)
		}
	}
}

// A folder that can't be read may hold the command: it is looked for.
func TestUnreadableFolderMayHoldIt(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a folder this user can't list")
	}
	d := t.TempDir()
	installTool(t, d, "magpie-fake-agent")
	if err := os.Chmod(d, 0o311); err != nil { // searchable, not listable
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(d, 0o755) })
	t.Setenv("PATH", d)
	lookWindowFor(t, 0)
	if _, err := Look("magpie-fake-agent"); err != nil {
		t.Fatalf("a command in a folder that can't be listed wasn't found: %v", err)
	}
}

func joinList(dirs []string) string {
	s := ""
	for i, d := range dirs {
		if i > 0 {
			s += string(os.PathListSeparator)
		}
		s += d
	}
	return s
}

func isNotFound(err error) bool {
	e, ok := err.(*exec.Error)
	return ok && e.Err == exec.ErrNotFound
}
