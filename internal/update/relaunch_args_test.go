package update

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// #275: a restart to update from the open window (Windows, Linux) came
// back as the tray icon alone; it opens the window again, on its tab, and
// only a restart with the window closed stays in the tray, as autostart.
func TestRelaunchArgs(t *testing.T) {
	for _, c := range []struct {
		window bool
		view   string
		want   []string
	}{
		{true, "settings", []string{"gui", "settings"}},
		{true, "", []string{"gui"}},
		{false, "", []string{"tray"}},
		{false, "settings", []string{"tray"}},
	} {
		if got := RelaunchArgs(c.window, c.view); !slices.Equal(got, c.want) {
			t.Errorf("RelaunchArgs(%v, %q) = %q, want %q", c.window, c.view, got, c.want)
		}
	}
}

// RelaunchBinary starts exe with those arguments, and the handover to this
// process as before.
func TestRelaunchBinaryArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for magpie")
	}
	for _, c := range []struct {
		window bool
		view   string
		want   string
	}{
		{true, "settings", "gui settings"},
		{false, "", "tray"},
	} {
		// a script and a file of its own each, so a slow start can't be
		// read as the next one's
		dir := t.TempDir()
		out := filepath.Join(dir, "out")
		exe := filepath.Join(dir, "magpie")
		script := "#!/bin/sh\necho \"$* $MAGPIE_REPLACES\" > " + out + ".tmp && mv " + out + ".tmp " + out + "\n"
		testenv.Program(t, exe, script)
		if err := RelaunchBinary(exe, "", c.window, c.view); err != nil {
			t.Fatal(err)
		}
		want := c.want + " " + strconv.Itoa(os.Getpid())
		var got string
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if b, err := os.ReadFile(out); err == nil {
				got = strings.TrimSpace(string(b))
				break
			}
		}
		if got != want {
			t.Errorf("relaunched with %q, want %q", got, want)
		}
	}
}
