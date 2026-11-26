//go:build darwin || linux

package claudetui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A Claude Code that draws nothing is told by what it waits on. One whose
// macOS Keychain read never answered held 45s with nothing on its screen,
// and the error said only "0 bytes read" (sowhynot): it now names the
// Keychain, the item and what to do.
func TestShimSaysWhatAnUndrawnClaudeWaitsOn(t *testing.T) {
	sec := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(sec, []byte("#!/bin/sh\nread x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		env  []string
		want []string
	}{
		{"keychain", []string{"FAKE_KEYCHAIN=" + sec}, []string{"(0 bytes read); it waits on the macOS Keychain", `read of "Claude Code-credentials"`, "Always Allow"}},
		{"silent", []string{"FAKE_SILENT=1"}, []string{"(0 bytes read); it runs no command,", "MiB resident"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			// the deadline is fixed before the shim and its fake start,
			// and a loaded runner spent all but 100ms of a 3s one there
			// before the fake had started its Keychain read: the window
			// leaves room for both to start
			by := time.Now().Add(10 * time.Second).UnixMilli()
			out, stderr, err := runEnds(t, t.TempDir(), append(c.env, DeadlineEnv+"="+strconv.FormatInt(by, 10)))
			if err == nil || strings.Contains(out, `"type":"result"`) {
				t.Fatalf("ended %v, wrote %q", err, out)
			}
			for _, w := range c.want {
				if !strings.Contains(stderr, w) {
					t.Fatalf("stderr lacks %q: %q", w, stderr)
				}
			}
		})
	}
}

func TestKeychainRead(t *testing.T) {
	for args, want := range map[string]string{
		`/usr/bin/security find-generic-password -a me -w -s Claude Code-credentials`:      "Claude Code-credentials",
		`security find-generic-password -a me -w -s "Claude Code" -x`:                      "Claude Code",
		`/bin/sh /tmp/x/security find-generic-password -a u -w -s Claude Code-credentials`: "Claude Code-credentials",
		`security find-generic-password -a me -w`:                                          "its item",
	} {
		if svc, ok := keychainRead(args); !ok || svc != want {
			t.Errorf("%s: %q %v", args, svc, ok)
		}
	}
	for _, args := range []string{"security list-keychains", "/bin/grep find-generic-password x", "find-generic-password"} {
		if _, ok := keychainRead(args); ok {
			t.Errorf("%s read as a Keychain read", args)
		}
	}
}
