package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cursorLink is what `cursor-agent login` prints, challenge and uuid made up.
const cursorLink = "https://cursor.com/loginDeepControl?challenge=Zm9vYmFyYmF6cXV4Zm9vYmFyYmF6cXV4Zm9vYmFyYmF&uuid=0b9d5f3e-6f1c-4c2a-9a55-3d1f0e6b7a21&mode=login&redirectTarget=cli&supportsSelectedTeamLogin=true"

// signInLink runs a fake login command printing script and says the link
// the sign-in hands to the window.
func signInLink(t *testing.T, whole func(string) bool, script string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "login")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"+script+"\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &signInFlow{done: make(chan struct{})}
	s.st = SignInState{State: "waiting"}
	err := runCLISignIn(s, "login", nil, true, nil, func() (string, string, bool) { return "", "", false }, whole, exe)
	s.mu.Lock()
	stop, u := s.stop, s.st.URL
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// #261: cursor-agent's link, wrapped where its line ran out, reached
// Cursor's page as "https://cursor.com/loginDeepControl?", which says the
// link "is incomplete or has expired".
func TestCursorSignInJoinsAWrappedLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	head, rest := cursorLink[:36], cursorLink[36:] // cut after "?"
	mode := strings.Index(cursorLink, "&mode")
	redirect := strings.Index(cursorLink, "&redirectTarget")
	for _, c := range []struct{ name, script, want string }{
		{"on one line", `echo "Waiting for browser authentication..."; echo "Open a browser and navigate to this link: ` + cursorLink + `"`, cursorLink},
		{"wrapped after ?", `printf 'Open a browser and navigate to this link: %s\n%s\n' '` + head + `' '` + rest + `'`, cursorLink},
		{"wrapped over three lines", `printf 'Open a browser and navigate to this link: %s\n  %s\n  %s\n' '` + head + `' '` + rest[:70] + `' '` + rest[70:] + `'`, cursorLink},
		{"wrapped in the uuid", `printf '%s\n%s\n' '` + cursorLink[:125] + `' '` + cursorLink[125:] + `'`, cursorLink},
		{"wrapped in the uuid across writes", `printf '%s\n' '` + cursorLink[:125] + `'; sleep 0.05; printf '%s\n' '` + cursorLink[125:] + `'`, cursorLink},
		{"wrapped after the uuid across writes", `printf '%s\n' '` + cursorLink[:mode] + `'; sleep 0.05; printf '%s\n' '` + cursorLink[mode:] + `'`, cursorLink},
		{"query across three writes", `printf '%s\n' '` + cursorLink[:mode] + `'; sleep 0.15; printf '%s\n' '` + cursorLink[mode:redirect] + `'; sleep 0.15; printf '%s\n' '` + cursorLink[redirect:] + `'`, cursorLink},
		{"wrapped in the final value across writes", `printf '%s\n' '` + cursorLink[:len(cursorLink)-2] + `'; sleep 0.05; printf '%s\n' '` + cursorLink[len(cursorLink)-2:] + `'`, cursorLink},
		{"wrapped after the uuid", `printf '%s\n%s\n' '` + cursorLink[:mode] + `' '` + cursorLink[mode:] + `'`, cursorLink},
		{"the rest a moment later", `echo '` + head + `'; sleep 0.3; echo '` + rest + `'`, cursorLink},
		{"whole, then a word", `printf "%s\nWaiting...\n" '` + cursorLink + `'`, cursorLink},
		{"never whole, then words", `echo '` + head + `'; echo 'Waiting for browser authentication...'`, head},
	} {
		if got := signInLink(t, cursorLinkWhole, c.script); got != c.want {
			t.Errorf("%s: link %q, want %q", c.name, got, c.want)
		}
	}
	// never whole, and nothing more: what there is, once the wait is over
	was := linkWait
	t.Cleanup(func() { linkWait = was })
	linkWait = 3 * time.Second
	if got := signInLink(t, cursorLinkWhole, `echo '`+head+`'`); got != head {
		t.Errorf("never whole: link %q", got)
	}
}

// An agent whose link needs nothing more hands on the first one as it was:
// a device code printed on the line after stays off it.
func TestCLISignInFirstLinkAsPrinted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	if got := signInLink(t, nil, `printf 'Open https://accounts.x.ai/device\nABCD-EFGH\n'`); got != "https://accounts.x.ai/device" {
		t.Errorf("link %q", got)
	}
}
