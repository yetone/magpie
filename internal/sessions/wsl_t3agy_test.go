package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// t3AgyProfile is where T3 Code's own Antigravity instance keeps agy's
// conversations under the T3 Code folder base (TestAgyCallsInT3Code).
func t3AgyProfile(base string) string {
	sum := sha256.Sum256([]byte("antigravity"))
	return filepath.Join(base, "userdata", "providers", "antigravity", hex.EncodeToString(sum[:]), "antigravity-acp")
}

func agyCalls(t *testing.T) []Call {
	t.Helper()
	var out []Call
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "agy" {
			out = append(out, c)
		}
	}
	return out
}

// tovelo on Discord: Antigravity in T3 Code, both run in WSL Ubuntu, wasn't
// on Usage, while a plain agy in the distro was. T3 Code there keeps agy's
// conversations in its profile under the distro's ~/.t3, which magpie on
// Windows didn't read. They count, with the distro, and are not listed as
// sessions (as this computer's aren't), also from the listing kept on disk.
func TestWSLAgyCallsInT3Code(t *testing.T) {
	t.Setenv("T3CODE_HOME", "")
	home, _ := wslDistro(t)
	copyTree(t, "testdata/agy-t3", t3AgyProfile(filepath.Join(home, ".t3")))

	check := func(when string) {
		t.Helper()
		got := agyCalls(t)
		if len(got) != 1 {
			t.Fatalf("%s: want the one model step of T3 Code's agy conversation in the distro, got %d: %+v", when, len(got), got)
		}
		at, _ := time.Parse(time.RFC3339, "2026-11-19T01:06:24Z")
		want := Tokens{Input: 1034, CacheRead: 200, Output: 56}
		if g := got[0]; !g.Time.Equal(at) || g.Session != "f8dd5efd-4ce8-499e-8359-ac13b3b2dd03" || g.Tokens != want {
			t.Errorf("%s: call %v %s %+v, want %v f8dd5efd-… %+v", when, g.Time, g.Session, g.Tokens, at, want)
		}
		if n := count(List(0), "agy"); n != 0 {
			t.Errorf("%s: T3 Code's agy conversation listed as %d sessions, want none", when, n)
		}
	}
	check("first listing")
	wslSettle()
	for _, d := range Dirs() {
		if strings.HasPrefix(d, home) && filepath.Base(d) == "antigravity-cli" {
			t.Errorf("Dirs() names %s, which T3 Code's agy profile isn't a sessions folder of", d)
		}
	}

	// magpie starts again: the listing kept on disk is read first
	wslSettle()
	Saved()
	Reset()
	check("after a restart")
}

// A distro whose ~/.t3 is this computer's own (a link that distro.open
// opens on Windows' side) is read once, as this computer's: the distro's
// listing doesn't take its files in again (the calls, each with its
// message id, would come once anyway).
func TestWSLAgyInT3CodeLinkedOnce(t *testing.T) {
	t.Setenv("T3CODE_HOME", "")
	home, _ := wslDistro(t)
	own, _ := os.UserHomeDir()
	copyTree(t, "testdata/agy-t3", t3AgyProfile(filepath.Join(own, ".t3")))
	WSLHomes = func() []WSLHome {
		return []WSLHome{{Distro: "Ubuntu", Home: home, Running: true, Open: func(p string) string {
			if rel, err := filepath.Rel(filepath.Join(home, ".t3"), p); err == nil && filepath.IsLocal(rel) {
				return filepath.Join(own, ".t3", rel)
			}
			return p
		}}}
	}
	Reset()
	if n := len(agyCalls(t)); n != 1 {
		t.Fatalf("want the one call once, got %d", n)
	}
	wslSettle()
	if fs := wslCallFiles("agy"); len(fs) != 0 {
		t.Fatalf("this computer's T3 Code profile listed again as the distro's: %+v", fs)
	}
}
