//go:build !windows

package proc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A desktop app with launchd's PATH finds a claude installed under a custom
// npm prefix: from the folders such tools use, and from the login shell's
// PATH, whatever its profile prints around it.
func TestUserPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	npm := filepath.Join(home, ".npm-global", "bin")
	os.MkdirAll(npm, 0o755)
	os.WriteFile(filepath.Join(npm, "claude"), []byte("#!/bin/sh\n"), 0o755)
	sh := filepath.Join(home, "sh")
	os.WriteFile(sh, []byte("#!/bin/sh\necho 'welcome back!'\nPATH=/from/profile:$PATH\neval \"$2\"\necho bye\n"), 0o755)
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/usr/bin:/bin")

	UserPath()
	p := os.Getenv("PATH")
	if !strings.HasPrefix(p, "/usr/bin:/bin:") || !strings.Contains(p, npm) {
		t.Fatalf("known folders not added after the old PATH: %q", p)
	}
	for end := time.Now().Add(5 * time.Second); !strings.Contains(os.Getenv("PATH"), "/from/profile"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the login shell's PATH never came: %q", os.Getenv("PATH"))
		}
	}
	if n := strings.Count(os.Getenv("PATH"), "/usr/bin:"); n != 1 {
		t.Fatalf("a folder PATH had was added again: %q", os.Getenv("PATH"))
	}
}
