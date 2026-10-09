package library

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// ReadRTK says which agents can be given rtk, and which of those have its
// hook: two passes over the agents on this machine, one for those rtk has a
// hook for, one for those it has none. Detecting the agents asks each of the
// ~47 magpie knows of whether it is here — its folder, its settings, its
// command on the PATH — which on a machine with many of them installed is a
// few hundred milliseconds. Asked once per pass, it doubles, and the RTK tab
// waits for it: the tab reads afresh each time the page is opened (an agent
// may have been installed since), so the cost is paid again and again for one
// answer.

func TestReadRTKDetectsTheAgentsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	// an empty PATH, as the other tests here do: this read must not find the
	// rtk installed on the machine running the test. ReadRTK runs `rtk gain`
	// and `rtk --version` on one it finds, and rtk writes its own history
	// (rtk/history.db) under the working directory — a test would leave that
	// behind, and would be reading the real machine besides (see
	// docs/code-standards.md: Tests stay out of the real machine).
	t.Setenv("PATH", "")
	// agents that are here, so both passes have something to look at
	for _, d := range []string{".claude", ".codex", ".gemini", ".config/opencode", ".dsh"} {
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(d)), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	real := detected
	var counted atomic.Int64
	defer func() { detected = real }()
	detected = func() []*agent.Agent {
		counted.Add(1)
		return real()
	}

	ReadRTK()

	if n := counted.Load(); n > 1 {
		t.Errorf("the agents were detected %d times in one ReadRTK, want at most 1: both passes need the same list", n)
	}
}

// The other read that turns a hook on or off finds the agent among the
// detected ones, so it asks for them once as well.
func TestSetRTKDetectsTheAgentsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	// no rtk on the PATH, so this is the read that says there is none — and
	// so the machine running the test is not read either
	t.Setenv("PATH", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}

	real := detected
	var counted atomic.Int64
	defer func() { detected = real }()
	detected = func() []*agent.Agent {
		counted.Add(1)
		return real()
	}

	// no rtk here, so this is the read that says there is none: SetRTK's own
	// answer is not what is wanted here
	_, _ = SetRTK("claude", true)
	_ = ReadRTK()

	// SetRTK is one read and ReadRTK one, so two detections in all
	if n := counted.Load(); n > 2 {
		t.Errorf("the agents were detected %d times for two reads, want 2", n)
	}
}
