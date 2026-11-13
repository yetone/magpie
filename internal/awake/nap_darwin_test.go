//go:build darwin && cgo

package awake

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestNoNapLetsTheMacSleep holds NoNap and reads the system's power
// assertions: none of this process's may hold sleep off, which is Keep's to
// do and only while the user asked for it (#6261).
func TestNoNapLetsTheMacSleep(t *testing.T) {
	if _, err := os.Stat("/usr/bin/pmset"); err != nil {
		t.Skip("no pmset:", err)
	}
	release := NoNap()
	defer release()
	out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").CombinedOutput()
	if err != nil {
		t.Fatalf("pmset: %v\n%s", err, out)
	}
	mine := fmt.Sprintf("pid %d(", os.Getpid())
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, mine) {
			t.Logf("held: %s", strings.TrimSpace(l))
			if strings.Contains(l, "Prevent") {
				t.Errorf("NoNap holds sleep off: %s", strings.TrimSpace(l))
			}
		}
	}
	release()
	release() // a second release does nothing
}
