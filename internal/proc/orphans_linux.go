package proc

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Where magpie is PID 1 (a container running `magpie serve`), or a child
// subreaper, the processes its children leave behind are adopted by it:
// an agent's CLI that ends with its MCP servers (uv, node, npm, a graph
// indexer) still running or just ended leaves them to magpie, and nothing
// in magpie waits for a process it never started. They stayed <defunct>
// children of `magpie serve` until it restarted (254 after 7 days,
// Kling__Lab on X). cmd.Wait reaps the ones magpie starts; this reaps the
// rest.

// OrphanEvery is how often the zombies adopted by magpie are looked for.
// One that was already a zombie at the previous look is nobody's to wait
// for: every command magpie starts is waited for the moment it ends.
var OrphanEvery = 5 * time.Second

// adopts says whether processes whose parent ends become magpie's.
func adopts() bool {
	if os.Getpid() == 1 {
		return true
	}
	// the answer is written through the pointer, not returned
	var v int32
	_, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, unix.PR_GET_CHILD_SUBREAPER, uintptr(unsafe.Pointer(&v)), 0, 0, 0, 0)
	return e == 0 && v != 0
}

// KeepReaping reaps the zombies magpie has adopted, until ctx ends. It
// does nothing where magpie adopts none.
func KeepReaping(ctx context.Context) {
	if !adopts() {
		return
	}
	go func() {
		tick := time.NewTicker(OrphanEvery)
		defer tick.Stop()
		seen := map[int]uint64{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				seen = reapOrphans(seen)
			}
		}
	}()
}

// reapOrphans reaps the zombie children that were zombies at the last
// look too (seen: pid and start time), and answers those that are now.
func reapOrphans(seen map[int]uint64) map[int]uint64 {
	now := map[int]uint64{}
	me := os.Getpid()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return seen
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		state, ppid, start, ok := zombieStat(b)
		if !ok || state != 'Z' || ppid != me {
			continue
		}
		if seen[pid] == start {
			var ws syscall.WaitStatus
			if _, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); err == nil {
				continue
			}
		}
		now[pid] = start
	}
	return now
}

// zombieStat is the state, parent and start time of a /proc/<pid>/stat
// line, counted after the command's closing parenthesis.
func zombieStat(b []byte) (state byte, ppid int, start uint64, ok bool) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, 0, 0, false
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 20 || len(f[0]) == 0 {
		return 0, 0, 0, false
	}
	pp, err := strconv.Atoi(string(f[1]))
	if err != nil {
		return 0, 0, 0, false
	}
	st, err := strconv.ParseUint(string(f[19]), 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return f[0][0], pp, st, true
}
