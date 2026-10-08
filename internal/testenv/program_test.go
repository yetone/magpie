package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// A program Program writes runs what it was given; when its test ends, the
// file goes back to the pool and the next test's program is that same
// file, which macOS has let run already. A file that was there before, such
// as a stand-in TestMain made, stays when the test that wrote over it ends.
func TestProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	t.Cleanup(RemovePrograms) // this package has no TestMain to do it
	var first os.FileInfo
	t.Run("first", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "fake")
		Program(t, p, "#!/bin/sh\necho one \"$@\"\n")
		if out, err := exec.Command(p, "a").Output(); err != nil || string(out) != "one a\n" {
			t.Fatalf("ran: %q %v", out, err)
		}
		// written again within the test: the new script runs
		Program(t, p, []byte("#!/bin/sh\necho two\n"))
		if out, err := exec.Command(p).Output(); err != nil || string(out) != "two\n" {
			t.Fatalf("ran again: %q %v", out, err)
		}
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("mode: %v %v", fi, err)
		}
		first = fi
	})
	p := filepath.Join(t.TempDir(), "next")
	Program(t, p, "#!/bin/sh\necho next\n")
	if out, err := exec.Command(p).Output(); err != nil || string(out) != "next\n" {
		t.Fatalf("the next test's program: %q %v", out, err)
	}
	fi, _ := os.Stat(p)
	if runtime.GOOS == "darwin" && !os.SameFile(first, fi) {
		t.Fatal("the next test's program is a new file, which macOS checks before its first run")
	}

	standIn := filepath.Join(t.TempDir(), "stand-in")
	if err := WriteProgram(standIn, "#!/bin/sh\nexit 1\n"); err != nil {
		t.Fatal(err)
	}
	t.Run("over a stand-in", func(t *testing.T) {
		Program(t, standIn, "#!/bin/sh\necho test\n")
	})
	if out, _ := exec.Command(standIn).Output(); string(out) != "test\n" {
		t.Fatalf("a stand-in written over is gone or changed back: %q", out)
	}

	if err := WriteProgram(filepath.Join(t.TempDir(), "bin"), "MZ"); err == nil {
		t.Fatal("a binary written as a script")
	}
}
