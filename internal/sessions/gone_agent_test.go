package sessions

import (
	"os"
	"testing"
	"time"
)

// An agent uninstalled with its folders deleted leaves nothing in the
// listing, kept or cached (_SekiBetu_ on X).
func TestGoneAgentLeavesNoSession(t *testing.T) {
	claude, codex := setup(t)
	const cc, cx = "claude:11111111-2222-3333-4444-555555555555", "codex:01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd"
	first := List(All)
	keptRow(t, first, cc)
	keptRow(t, first, cx)
	if err := os.RemoveAll(claude); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(codex); err != nil {
		t.Fatal(err)
	}
	for _, s := range List(All) {
		t.Errorf("still listed after its files were deleted: %s:%s", s.Agent, s.ID)
	}
	mu.Lock()
	n := len(cache)
	mu.Unlock()
	if n != 0 {
		t.Errorf("%d parses kept of files that are gone", n)
	}
}

// The last agent in a WSL distro is uninstalled and its folders deleted:
// WSLHomes names no distro any more, and wsl.exe answered, so the listing
// kept of the distro goes (it stayed, cold and undeletable, for good). With
// no answer from wsl.exe it is kept as it was.
func TestWSLAgentsGoneListingDrops(t *testing.T) {
	for _, answered := range []bool{true, false} {
		home, _ := wslDistro(t)
		WSLListed = func() bool { return answered }
		t.Cleanup(func() { WSLListed = nil })
		if n := len(List(0)); n != 7 {
			t.Fatalf("answered=%v: want 7 sessions, got %d", answered, n)
		}
		wslSettle()
		os.RemoveAll(home)
		WSLHomes = func() []WSLHome { return nil }
		wslSess.Lock()
		wslSess.at = time.Time{}
		wslSess.Unlock()
		List(0)
		wslSettle()
		n := len(List(0))
		if answered && n != 0 || !answered && n != 7 {
			t.Errorf("answered=%v: %d sessions listed after the agents were taken away", answered, n)
		}
	}
}
