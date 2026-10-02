//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestZedCredentialCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS security command")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	args := filepath.Join(bin, "args")
	t.Setenv("ZED_TEST_ARGS", args)
	name := "security"
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ZED_TEST_ARGS\"\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:7654/v1"
	if err := saveZedCredential(url); err != nil {
		t.Fatal(err)
	}
	want := "add-internet-password\n-U\n-s\n" + url + "\n-a\nBearer\n-w\nmagpie-zed\n"
	if got := readFile(args); got != want {
		t.Fatalf("credential arguments: %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho locked >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveZedCredential(url); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("credential store error: %v", err)
	}
}
