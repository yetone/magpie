//go:build darwin && cgo && !nogui

package gui

import (
	"bytes"
	"context"
	"debug/macho"
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
	return runAppKitSDK(t, 0, timeout, env, args...)
}

// runAppKitSDK is runAppKit with the program naming sdk (major<<16 |
// minor<<8) as the macOS SDK it was linked against, or the one it was, for
// 0. AppKit gives a program the design of the SDK it names, so a test sees
// each design whatever SDK the toolchain has.
func runAppKitSDK(t *testing.T, sdk uint32, timeout time.Duration, env []string, args ...string) ([]byte, error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%s-%d", filepath.Base(self), time.Now().UnixNano())
	bin := filepath.Join(t.TempDir(), name)
	if sdk != 0 {
		stampSDK(t, self, bin, sdk)
	} else if err := os.Link(self, bin); err != nil {
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

// stampSDK writes to dst a copy of the program src that names sdk as the
// macOS SDK it was linked against, and signs it again, as an arm64 Mac runs
// only a program whose signature matches it.
func stampSDK(t *testing.T, src, dst string, sdk uint32) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	f, err := macho.NewFile(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	// The load commands follow the header (32 bytes in a 64-bit program).
	// The SDK is the fifth word of LC_BUILD_VERSION, or the fourth of
	// LC_VERSION_MIN_MACOSX, which a program has instead when it runs on
	// macOS 10.13, as go test links one for an Intel Mac.
	sdkAt := map[uint32]int{0x32: 16, 0x24: 12} // LC_BUILD_VERSION, LC_VERSION_MIN_MACOSX
	at, off := -1, 32
	for _, l := range f.Loads {
		raw := l.Raw()
		if n, ok := sdkAt[f.ByteOrder.Uint32(raw)]; ok {
			at = off + n
			break
		}
		off += len(raw)
	}
	if f.Magic != macho.Magic64 || at < 0 {
		t.Fatalf("%s isn't a 64-bit program that names its SDK", src)
	}
	f.ByteOrder.PutUint32(b[at:], sdk)
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("codesign", "--sign", "-", "--force", dst).CombinedOutput(); err != nil {
		t.Fatalf("signing the program again: %v\n%s", err, out)
	}
}
