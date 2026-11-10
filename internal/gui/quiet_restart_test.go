package gui

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/update"
)

// xyxyxy_15591 on Discord: an update put in by itself shouldn't put the
// window up. A restart by itself (Settings' AutoRestart) marks itself, so
// the new version comes back as the old one was without taking focus; a
// "Restart to update" the user clicked doesn't, and comes to the front as
// it always has. The mark is read once, and one left by a restart that
// never came back (stale) or refused (#894) doesn't make a later start
// quiet.
func TestRestartBySelfComesBackQuietly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	mark := filepath.Join(home, "magpie", quietRestartFile)

	exe := filepath.Join(t.TempDir(), "magpie")
	os.WriteFile(exe, []byte("old"), 0o755)
	self, _ := os.Stat(exe)
	os.WriteFile(exe, []byte("newer"), 0o755) // in already: install has nothing to swap
	o := updates
	updates = &updater{exe: exe, self: self, latest: &update.Release{Version: "0.1.9999"}}
	defer func() { updates = o }()
	var refuse bool
	wasRelaunch := relaunchBinary
	relaunchBinary = func(exe, old string, window bool, view string) error {
		if refuse {
			return &update.BlockedError{Err: &os.PathError{Op: "fork/exec", Path: exe, Err: syscall.Errno(4551)}}
		}
		return nil
	}
	defer func() { relaunchBinary = wasRelaunch }()

	// clicked: no mark, the new one comes to the front, even after a
	// restart by itself that left one (it never came back)
	markQuietRestart(false)
	if !restartToUpdate(false, true, "") {
		t.Fatal("restart to update failed")
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatalf("a restart the user asked for is marked quiet: %v", err)
	}
	if quiet, _ := takeQuietRestart(); quiet {
		t.Fatal("the start after a clicked restart is quiet")
	}

	// by itself, window up or not: marked, read once; minimised (#3495)
	// only for a window up and minimised
	for _, c := range []struct{ window, minimised, want bool }{
		{true, false, false}, {false, false, false}, {true, true, true}, {false, true, false},
	} {
		if !restartInto(false, c.window, "", true, c.minimised) {
			t.Fatalf("%+v: restart by itself failed", c)
		}
		quiet, minimised := takeQuietRestart()
		if !quiet {
			t.Fatalf("%+v: the start after a restart by itself isn't quiet", c)
		}
		if minimised != c.want {
			t.Fatalf("%+v: minimised %v, want %v", c, minimised, c.want)
		}
		if quiet, _ := takeQuietRestart(); quiet {
			t.Fatalf("%+v: the mark is read twice", c)
		}
	}

	// magpie web restarts in place: nothing to mark
	if !restartInto(true, false, "", true, false) {
		t.Fatal("web restart failed")
	}
	webReexec.Store(false)
	if quiet, _ := takeQuietRestart(); quiet {
		t.Fatal("a web restart marked the app's next start quiet")
	}

	// the new version refused (#894): this one runs on, unmarked
	refuse = true
	if restartInto(false, true, "", true, false) {
		t.Fatal("a refused restart said to quit")
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatalf("a refused restart left its mark: %v", err)
	}

	// a mark from a restart that never came back is not this start's
	os.MkdirAll(filepath.Dir(mark), 0o755)
	old := strconv.FormatInt(time.Now().Add(-quietFor-time.Minute).Unix(), 10)
	os.WriteFile(mark, []byte(old+"\n"), 0o644)
	if quiet, _ := takeQuietRestart(); quiet {
		t.Fatal("a stale mark made this start quiet")
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatalf("a stale mark is kept: %v", err)
	}
	for _, bad := range []string{"garbage", quietStampNow() + " junk", quietStampNow() + " minimised more", ""} {
		os.WriteFile(mark, []byte(bad), 0o644)
		if quiet, minimised := takeQuietRestart(); quiet || minimised {
			t.Fatalf("an unreadable mark %q made this start quiet", bad)
		}
	}

	// a mark from a version before #3495, the seconds alone: a window up
	os.WriteFile(mark, []byte(quietStampNow()+"\n"), 0o644)
	if quiet, minimised := takeQuietRestart(); !quiet || minimised {
		t.Fatalf("an older version's mark read quiet=%v minimised=%v", quiet, minimised)
	}
}

func quietStampNow() string { return strconv.FormatInt(time.Now().Unix(), 10) }
