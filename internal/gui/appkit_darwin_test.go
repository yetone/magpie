//go:build darwin && cgo && !nogui

package gui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

// runAppKit runs this test binary again under a name of its own, as AppKit
// needs a process's main thread, with env and args added, and gives what it
// printed. WebKit keeps data in folders named after the process under the
// account's home, not HOME, so CFFIXED_USER_HOME gives it a temporary home,
// and the test fails when such a folder is left in the real ~/Library.
func runAppKit(t *testing.T, timeout time.Duration, env []string, args ...string) ([]byte, error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%s-%d", filepath.Base(self), time.Now().UnixNano())
	bin := filepath.Join(t.TempDir(), name)
	if err := os.Link(self, bin); err != nil {
		b, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(append(os.Environ(), env...), "XDG_CONFIG_HOME="+t.TempDir(), "CFFIXED_USER_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	// WebKit also makes empty folders under the name in the account's
	// temporary folder, which TMPDIR doesn't move; the name is this run's.
	if rerr := os.RemoveAll(filepath.Join(os.TempDir(), name)); rerr != nil {
		t.Errorf("can't remove WebKit's temporary folders: %v", rerr)
	}
	account, uerr := user.Current()
	if uerr != nil {
		t.Errorf("can't tell where the account's home is: %v", uerr)
		return out, err
	}
	for _, dir := range []string{"WebKit", "Caches"} {
		left := filepath.Join(account.HomeDir, "Library", dir, name)
		switch _, serr := os.Lstat(left); {
		case serr == nil:
			t.Errorf("the windows' process left %s in the account's real home", left)
		case !errors.Is(serr, fs.ErrNotExist):
			t.Errorf("can't tell whether the windows' process left %s: %v", left, serr)
		}
	}
	return out, err
}
