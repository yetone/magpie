package gui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// xyxyxy_15591 on Discord: an update put in by itself shouldn't put the
// window up. A restart by itself (Settings' AutoRestart) marks itself, so
// the new version comes back without taking focus; a
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
	markQuietRestart()
	if !restartToUpdate(false, true, "") {
		t.Fatal("restart to update failed")
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatalf("a restart the user asked for is marked quiet: %v", err)
	}
	if quiet, _ := takeQuietRestart(); quiet {
		t.Fatal("the start after a clicked restart is quiet")
	}

	// by itself: marked, read once, never minimised (no window asked for)
	if !restartBySelf() {
		t.Fatal("restart by itself failed")
	}
	if quiet, minimised := takeQuietRestart(); !quiet || minimised {
		t.Fatalf("the start after a restart by itself read quiet=%v minimised=%v", quiet, minimised)
	}
	if quiet, _ := takeQuietRestart(); quiet {
		t.Fatal("the mark is read twice")
	}

	// magpie web restarts in place: nothing to mark
	if !restartInto(true, false, "", true) {
		t.Fatal("web restart failed")
	}
	webReexec.Store(false)
	if quiet, _ := takeQuietRestart(); quiet {
		t.Fatal("a web restart marked the app's next start quiet")
	}

	// the new version refused (#894): this one runs on, unmarked
	refuse = true
	if restartBySelf() {
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

	// an older version's mark, which says its window was minimised
	// (#3495): it restarts into this one with the window up as it was
	markOldQuietRestart(true)
	if quiet, minimised := takeQuietRestart(); !quiet || !minimised {
		t.Fatalf("an older version's minimised mark read quiet=%v minimised=%v", quiet, minimised)
	}
}

// eating_dream on X (v0.1.4303, AutoRestart on, a check every half
// hour): every update put the main window back over what they were
// doing. A restart nobody clicked (restartBySelf: Settings' AutoRestart,
// a terminal's magpie update, a Remote magpie's ask, another copy's
// install) comes back in the tray alone, whether the window was up,
// minimised or closed; one the user clicked opens it as it was.
func TestRestartBySelfComesBackToTheTray(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	exe := filepath.Join(t.TempDir(), "magpie")
	os.WriteFile(exe, []byte("old"), 0o755)
	self, _ := os.Stat(exe)
	os.WriteFile(exe, []byte("newer"), 0o755)
	o := updates
	updates = &updater{exe: exe, self: self, latest: &update.Release{Version: "0.1.9999"}}
	defer func() { updates = o }()
	var asked []bool
	wasRelaunch := relaunchBinary
	relaunchBinary = func(exe, old string, window bool, view string) error {
		asked = append(asked, window)
		return nil
	}
	defer func() { relaunchBinary = wasRelaunch }()

	if !restartBySelf() {
		t.Fatal("restart by itself failed")
	}
	if !restartToUpdate(false, true, "") {
		t.Fatal("restart to update failed")
	}
	if len(asked) != 2 || asked[0] || !asked[1] {
		t.Fatalf("windows asked for %v, want [false true]: by itself the tray alone, clicked the window", asked)
	}
}

// markOldQuietRestart writes the mark as a version before this one did,
// minimised when its window was.
func markOldQuietRestart(minimised bool) {
	markQuietRestart()
	if minimised {
		f := filepath.Join(settings.Dir(), quietRestartFile)
		b, _ := os.ReadFile(f)
		os.WriteFile(f, []byte(strings.TrimSpace(string(b))+" "+quietMinimisedWord+"\n"), 0o644)
	}
}

func quietStampNow() string { return strconv.FormatInt(time.Now().Unix(), 10) }
