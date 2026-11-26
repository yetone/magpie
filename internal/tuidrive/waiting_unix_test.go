//go:build darwin || linux

package tuidrive

import (
	"strings"
	"testing"
	"time"
)

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
	if cmds[0].Age != 50*time.Second || !strings.HasPrefix(cmds[0].Args, "/usr/bin/security") || cmds[1].Age != 10*time.Second {
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
