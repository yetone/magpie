package provider

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// an ask that couldn't tell leaves the account served, on disk too, where
// it had dropped Cursor from the Providers page and routing (#154); a CLI
// sure nobody is signed in still signs it out
func TestCLIIdentityUnsureKeepsAnswer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keepingIdentities(t)
	c := &cliIdentity{name: "x", exe: func() string { return "/bin/sh" },
		ask: func() (string, string, bool, error) { return "me@example.com", "Pro", true, nil }}
	if _, _, ok := c.get(); !ok {
		t.Fatal("never signed in")
	}
	ask := func(u string, ok bool, err error) {
		t.Helper()
		c.Lock()
		c.ask = func() (string, string, bool, error) { return u, "", ok, err }
		c.at = time.Now().Add(-2 * time.Minute) // due to be asked again
		done := c.refresh()
		c.Unlock()
		<-done
	}
	ask("", false, errors.New("signal: killed"))
	if u, p, ok := c.get(); !ok || u != "me@example.com" || p != "Pro" {
		t.Fatalf("a failed ask dropped the account: %q %q %v", u, p, ok)
	}
	if k := readIdentities()["x"]; !k.OK || k.User != "me@example.com" {
		t.Fatalf("a failed ask was kept: %+v", k)
	}
	c.Lock()
	again := time.Since(c.at) < time.Minute
	c.Unlock()
	if !again {
		t.Fatal("a failed ask is asked again at once, every look")
	}
	ask("", false, nil)
	if _, _, ok := c.get(); ok {
		t.Fatal("signed out, still served")
	}
	if k := readIdentities()["x"]; k.OK {
		t.Fatalf("signed out, still kept: %+v", k)
	}
}

func TestParseCursorAbout(t *testing.T) {
	for _, c := range []struct {
		out, user, plan string
		said            bool
	}{
		{`{"userEmail":"me@example.com","subscriptionTier":"Pro"}`, "me@example.com", "Pro", true},
		{"\x1b[33mA new version is available\x1b[0m\n{\"userEmail\":\" me@example.com \",\"subscriptionTier\":\"Ultra\"}\n", "me@example.com", "Ultra", true},
		{"{\n  \"userEmail\": \"me@example.com\"\n}", "me@example.com", "", true},
		{`{"userEmail":null,"subscriptionTier":"Free"}`, "", "Free", true},
		{"", "", "", false},
		{"Error: network request failed", "", "", false},
		{`{"userEmail":"me@exa`, "", "", false},
	} {
		u, p, said := parseCursorAbout([]byte(c.out))
		if u != c.user || p != c.plan || said != c.said {
			t.Errorf("%q: %q %q %v, want %q %q %v", c.out, u, p, said, c.user, c.plan, c.said)
		}
	}
}

func TestAskCursorStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	exe := filepath.Join(home, "cursor-agent")
	wasExe, wasOut := CursorExecutable, cursorSignedOut
	t.Cleanup(func() { CursorExecutable, cursorSignedOut = wasExe, wasOut })
	CursorExecutable = func() string { return exe }
	signedOut := false
	cursorSignedOut = func() bool { return signedOut }

	for _, c := range []struct {
		name, script string
		out          bool // the token is gone
		user         string
		sure         bool
	}{
		{"signed in", `echo '{"userEmail":"me@example.com","subscriptionTier":"Pro"}'`, false, "me@example.com", true},
		{"signed in after an update notice", `echo 'Updating…'; echo '{"userEmail":"me@example.com"}'`, false, "me@example.com", true},
		{"says nobody", `echo '{"userEmail":""}'`, false, "", true},
		{"fails", `echo 'fetch failed' >&2; exit 1`, false, "", false},
		{"prints no JSON", `echo 'Something went wrong'`, false, "", false},
		{"fails, token gone", `exit 1`, true, "", true},
	} {
		testenv.Program(t, exe, "#!/bin/sh\n"+c.script+"\n")
		signedOut = c.out
		u, _, ok, err := askCursorStatus()
		if u != c.user || ok != (c.user != "") || (err == nil) != c.sure {
			t.Errorf("%s: %q %v %v", c.name, u, ok, err)
		}
	}
}
