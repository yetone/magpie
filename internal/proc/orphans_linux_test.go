package proc

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// zombieChildren counts this process's <defunct> children.
func zombieChildren(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if st, pp, _, ok := zombieStat(b); ok && st == 'Z' && pp == os.Getpid() {
			n++
		}
	}
	return n
}

// TestAdoptedOrphansAreReaped: magpie as a container's PID 1 (here a
// subreaper, which adopts the same way) is left the processes of its
// children's children, and they stayed <defunct> until it restarted (254
// after 7 days, Kling__Lab on X).
func TestAdoptedOrphansAreReaped(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Skip("no subreaper:", err)
	}
	defer unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0)
	old := OrphanEvery
	OrphanEvery = 20 * time.Millisecond
	defer func() { OrphanEvery = old }()

	// the shell ends at once; the two it started end a moment later and
	// are adopted by this process, which never started them
	for range 3 {
		cmd := exec.Command("sh", "-c", "(sleep 0.2 &) ; (sleep 0.2 &) ; exit 0")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	// a command that is started and waited for is no orphan
	if err := exec.Command("true").Run(); err != nil {
		t.Fatal(err)
	}

	// they end, and stay zombies of this process
	for end := time.Now().Add(10 * time.Second); zombieChildren(t) < 6; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("only %d of the 6 orphans ended", zombieChildren(t))
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	KeepReaping(ctx)

	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if zombieChildren(t) == 0 {
			return
		}
	}
	t.Fatalf("%d adopted zombies left", zombieChildren(t))
}
