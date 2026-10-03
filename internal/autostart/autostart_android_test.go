package autostart

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBootScript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Spaces, quotes and shell expansions must stay part of the file name.
	exe := filepath.Join(home, "magpie ' $not_a_variable")
	out := filepath.Join(home, "args")
	if err := os.WriteFile(exe, []byte("#!/system/bin/sh\nprintf '%s' \"$1\" > \"$HOME/args\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := enable(exe); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(record(), 0o600); err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Fatal("non-executable script reported as enabled")
	}
	if err := enable(exe); err != nil {
		t.Fatal(err)
	}
	if !Enabled() {
		t.Fatal("enable did not restore executable permission")
	}
	if err := exec.Command("/system/bin/sh", record()).Run(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(out)
		if err == nil && string(b) == "serve" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("boot script did not launch the gateway: args=%q, error=%v", b, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := disable(); err != nil {
		t.Fatal(err)
	}
}
