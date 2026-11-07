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
			by := time.Now().Add(3 * time.Second).UnixMilli()
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

func TestDescendants(t *testing.T) {
	ps := `    1     0  1000  10-01:00:00 /sbin/launchd
  100     1 43000     00:52 /usr/local/bin/claude --mcp-config x
  101   100  2000     00:50 /usr/bin/security find-generic-password -a me -w -s Claude Code-credentials
  102   101   100     00:10 /bin/sleep 5
  103     1   100     01:00 /bin/other
  bad line
`
	cmds, rss, ok := descendants(ps, 100)
	if !ok || rss != 43000 || len(cmds) != 2 {
		t.Fatalf("%v %d %v", cmds, rss, ok)
	}
	if cmds[0].age != 50*time.Second || !strings.HasPrefix(cmds[0].args, "/usr/bin/security") || cmds[1].age != 10*time.Second {
		t.Fatalf("%v", cmds)
	}
	if _, _, ok := descendants(ps, 999); ok {
		t.Fatal("a pid ps doesn't list read as known")
	}
}

func TestEtime(t *testing.T) {
	for v, want := range map[string]time.Duration{
		"00:05":       5 * time.Second,
		"01:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"2-00:00:01":  48*time.Hour + time.Second,
		"10-01:00:00": 241 * time.Hour,
	} {
		if d, err := etime(v); err != nil || d != want {
			t.Errorf("%s: %s %v", v, d, err)
		}
	}
	for _, v := range []string{"5", "a:b", "1:2:3:4", "x-00:01"} {
		if _, err := etime(v); err == nil {
			t.Errorf("%s read", v)
		}
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

func TestProgramNamesNoArguments(t *testing.T) {
	for args, want := range map[string]string{
		"/usr/bin/git config --get user.token=SECRET": "git",
		"/bin/zsh -c echo SECRET":                     "zsh",
		"/usr/bin/python3 /opt/helper.py SECRET":      "python3 helper.py",
		"node /x/apiKeyHelper.js --key SECRET":        "node apiKeyHelper.js",
		"":                                            "a command",
	} {
		if got := program(args); got != want {
			t.Errorf("%q: %q", args, got)
		}
	}
}
