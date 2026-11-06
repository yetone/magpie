//go:build !windows

package proc

import "testing"

// TestWaitFollowingStopsAnswersTheExit: the exit code as it is, and 128
// plus the signal's number for a command a signal ended, as a shell says.
func TestWaitFollowingStopsAnswersTheExit(t *testing.T) {
	for script, want := range map[string]int{"exit 0": 0, "exit 3": 3, "kill -TERM $$": 143} {
		cmd := Command("/bin/sh", "-c", script)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		got, err := WaitFollowingStops(cmd)
		if err != nil || got != want {
			t.Errorf("%s: %d %v, want %d", script, got, err, want)
		}
	}
}
