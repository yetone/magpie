//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

func TestZedCredentialCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS security command")
	}
	bin := t.TempDir()
	app := filepath.Join(t.TempDir(), "Zed.app")
	zedBin := filepath.Join(app, "Contents", "MacOS", "zed")
	testenv.Program(t, zedBin, "#!/bin/sh\n")
	if err := os.Symlink(zedBin, filepath.Join(bin, "zed")); err != nil {
		t.Fatal(err)
	}
	app, err := filepath.EvalSymlinks(app)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	args := filepath.Join(bin, "args")
	t.Setenv("ZED_TEST_ARGS", args)
	name := "security"
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ZED_TEST_ARGS\"\n"
	testenv.Program(t, filepath.Join(bin, name), script)
	url := "http://127.0.0.1:7654/v1"
	if err := saveZedCredential(url); err != nil {
		t.Fatal(err)
	}
	want := "add-internet-password\n-U\n-T\n" + app + "\n-T\n/usr/bin/security\n-s\n" + url + "\n-a\nBearer\n-w\nmagpie-zed\n"
	if got := readFile(args); got != want {
		t.Fatalf("credential arguments: %q, want %q", got, want)
	}
	testenv.Program(t, filepath.Join(bin, name), "#!/bin/sh\necho locked >&2\nexit 1\n")
	if err := saveZedCredential(url); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("credential store error: %v", err)
	}
}

func TestZedAppPath(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	home := t.TempDir()
	testenv.SetHome(t, home)
	app := filepath.Join(home, "Applications", "Zed.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := zedAppPath(context.Background()); err != nil || got != app {
		t.Fatalf("home Applications: %q, %v", got, err)
	}
	preview := filepath.Join(home, "Applications", "Zed Preview.app")
	if err := os.Rename(app, preview); err != nil {
		t.Fatal(err)
	}
	if got, err := zedAppPath(context.Background()); err != nil || got != preview {
		t.Fatalf("Preview without a CLI: %q, %v", got, err)
	}
	custom := filepath.Join(t.TempDir(), "Custom Zed.app")
	zedBin := filepath.Join(custom, "Contents", "MacOS", "zed")
	testenv.Program(t, zedBin, "#!/bin/sh\n")
	if err := os.Symlink(zedBin, filepath.Join(bin, "zed")); err != nil {
		t.Fatal(err)
	}
	custom, err := filepath.EvalSymlinks(custom)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := zedAppPath(context.Background()); err != nil || got != custom {
		t.Fatalf("CLI's bundle: %q, %v", got, err)
	}
	customEnv := filepath.Join(t.TempDir(), "ZedG.app")
	customBin := filepath.Join(customEnv, "Contents", "MacOS", "zedg")
	testenv.Program(t, customBin, "#!/bin/sh\n")
	customEnv, err = filepath.EvalSymlinks(customEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ZED_BIN", customBin)
	if got, err := zedAppPath(context.Background()); err != nil || got != customEnv {
		t.Fatalf("custom configured bundle: %q, %v", got, err)
	}
}
