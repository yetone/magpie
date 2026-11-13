//go:build !windows

package update

import "time"

// awaitGone waits, 20s at most, for the magpie that relaunched this one to
// exit.
func awaitGone(pid int) {
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && alive(pid); {
		time.Sleep(100 * time.Millisecond)
	}
}
