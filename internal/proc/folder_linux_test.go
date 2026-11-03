package proc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A file manager that stays in the foreground (a first window) must not hold
// the call, and must live on after it returns.
func TestOpenFolderDoesNotWaitForTheFileManager(t *testing.T) {
	bin, dir, pidf := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "pid")
	script := "#!/bin/sh\necho $$ > " + pidf + "\necho \"$1\" > " + pidf + ".arg\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(bin, "xdg-open"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	start := time.Now()
	if err := OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("OpenFolder waited %v for the file manager", d)
	}
	var pid int
	for i := 0; i < 500 && pid == 0; i++ {
		if b, err := os.ReadFile(pidf); err == nil && strings.Contains(string(b), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("the opener never ran")
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	if sid, err := sidOf(pid); err != nil || sid == mustSid() {
		t.Fatalf("the opener shares magpie's session: %d %v", sid, err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the opener is gone: %v", err)
	}
	if b, _ := os.ReadFile(pidf + ".arg"); strings.TrimSpace(string(b)) != dir {
		t.Fatalf("opened %q, want %q", b, dir)
	}
}

func mustSid() int { s, _ := sidOf(os.Getpid()); return s }

// sidOf is the session id, the sixth field after the command in /proc/pid/stat.
func sidOf(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b[strings.LastIndex(string(b), ")")+1:]))
	return strconv.Atoi(f[3])
}

func TestOpenFolderNeedsAFolderAndAnOpener(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := OpenFolder(t.TempDir()); err == nil {
		t.Fatal("no opener on PATH should be an error")
	}
	if err := OpenFolder(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("a missing folder should be an error")
	}
}
