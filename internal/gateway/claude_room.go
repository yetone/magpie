package gateway

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// claudeRoom counts the Claude Code processes magpie has running for
// Claude subscriptions: each conversation's run, busy, waiting for its next
// turn or on its caller's tool results, and each one-off test, warm-up and
// /usage read. Each holds about 100 MiB of its own (Claude Code 2.1.303 on
// Linux arm64: 86–112 MiB anonymous, the 285 MB binary's pages shared), and
// nothing bounded how many ran: a 512 MiB service had 2 busy runs and 14
// starting at once, and the kernel killed them (#2956). A new one waits for
// room (take): an idle run is let go for it first, its session shelved for
// its next turn, then one waiting parkLongest on tool results; with neither,
// it waits for one to end, claudeRoomWait at most, and is answered 503,
// which routing passes to the next account or provider.
type claudeRoomT struct {
	mu sync.Mutex
	// live is how many Claude Code processes run; leaving how many of them
	// magpie has asked to end, not ended yet; waiting how many asks wait
	// for room
	live, leaving, waiting int
	// freed is closed, and made anew, each time one ends, or a run comes
	// to wait for its next turn (nudge), which may be let go for room
	freed chan struct{}
}

var claudeRoom = &claudeRoomT{freed: make(chan struct{})}

// claudeRoomWait is how long a Claude Code waits for room before its
// request is answered 503.
var claudeRoomWait = 2 * time.Minute

// claudeFull is the error of an ask that found no room in claudeRoomWait.
type claudeFull struct {
	live, most int
	why        string
	waited     time.Duration
}

// claudeNoRoomSaid is in every claudeFull's words, as routing finds it
// (claudeNoRoom).
const claudeNoRoomSaid = "Claude Code processes are running, as many as may run at once"

// claudeNoRoom says whether a failed reply is the bridge's 503 for a full
// claudeRoom.
func claudeNoRoom(status int, body []byte) bool {
	return status == 503 && bytes.Contains(body, []byte(claudeNoRoomSaid))
}

func (e *claudeFull) Error() string {
	return fmt.Sprintf("%d "+claudeNoRoomSaid+" (%s); none ended within %s. "+
		"Set how many may run with `magpie claude-code runs N` (claudeRunsMost in settings.json)",
		e.live, e.why, e.waited)
}

// claudeRunsMost is how many Claude Code processes may run at once, and
// what says so: the user's claudeRunsMost, else what this machine's memory
// holds (claudeMemRuns), else 0, no bound.
func claudeRunsMost() (int, string) {
	if n := settings.Load().ClaudeRunsMost; n > 0 {
		return n, "claudeRunsMost in settings"
	}
	return claudeMemRuns()
}

// ClaudeMemRuns is how many Claude Code processes this machine's memory
// holds, and why; 0 when not bounded by it (`magpie claude-code runs`).
func ClaudeMemRuns() (int, string) { return claudeMemRuns() }

// The memory a Claude Code process is given, and what is kept for magpie
// and the rest of the machine, as the bound is sized from memory: 512 MiB
// holds 2, 1 GiB 6.
const (
	claudeRunMiB     = 128
	claudeReserveMiB = 256
)

var memRuns struct {
	sync.Mutex
	known bool
	n     int
	why   string
}

// claudeMemRuns is how many Claude Code processes this machine's memory
// holds: (limit - claudeReserveMiB) / claudeRunMiB, 1 at least, the
// limit the least of the cgroup's memory.max and memory.high (v1: its
// memory.limit_in_bytes) and MemTotal. A machine whose memory can't be
// read, or that isn't Linux, has no bound from it (0): a limit not known
// is not a small one. It is read once.
func claudeMemRuns() (int, string) {
	memRuns.Lock()
	defer memRuns.Unlock()
	if !memRuns.known {
		memRuns.known = true
		if limit, why := memLimit(memGOOS, memRoot); limit > 0 {
			n := runsFor(limit)
			memRuns.n = n
			memRuns.why = fmt.Sprintf("%d MiB of memory (%s) holds %d", limit>>20, why, n)
		}
	}
	return memRuns.n, memRuns.why
}

// runsFor is how many Claude Code processes limit bytes of memory hold.
func runsFor(limit int64) int {
	return max(int((limit>>20-claudeReserveMiB)/claudeRunMiB), 1)
}

// memGOOS and memRoot are the system memLimit reads, and the root its
// files are under, for tests.
var (
	memGOOS = runtime.GOOS
	memRoot = "/"
)

// memOwn says memLimit reads this computer, so where it reads nothing
// the computer's memory may stand in. A test's root is not this computer
// even when it names this computer's system: such a root, holding
// nothing, read the test machine's own memory as its own (#5118, on Linux
// CI and WSL).
func memOwn() bool { return memGOOS == runtime.GOOS && memRoot == "/" }

// memLimit is the memory magpie's processes may use, in bytes, and where
// that is said; 0 when not known.
func memLimit(goos, root string) (int64, string) {
	if goos != "linux" {
		return 0, ""
	}
	var limit int64
	var why string
	least := func(n int64, from string) {
		if n > 0 && (limit == 0 || n < limit) {
			limit, why = n, from
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "proc/meminfo")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					least(kb<<10, "MemTotal")
				}
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "proc/self/cgroup"))
	if err != nil {
		return limit, why
	}
	cgroups := filepath.Join(root, "sys/fs/cgroup")
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			// v2: this cgroup's limits and every one above it. The
			// cgroup path is a "/" path whatever the host reads it on,
			// so the walk is path's: filepath's root is "\" on Windows,
			// where dir never read "/" and the loop never ended (#4113).
			for dir := path.Clean("/" + parts[2]); ; dir = path.Dir(dir) {
				for _, name := range []string{"memory.max", "memory.high"} {
					if n, ok := readLimit(filepath.Join(cgroups, dir, name)); ok {
						least(n, "cgroup "+path.Join(dir, name))
					}
				}
				if dir == "/" {
					break
				}
			}
		case slices.Contains(strings.Split(parts[1], ","), "memory"):
			// v1: the memory controller's group, or the container's root
			// where the host's path isn't mounted
			for _, dir := range []string{filepath.Join(cgroups, "memory", parts[2]), filepath.Join(cgroups, "memory")} {
				if n, ok := readLimit(filepath.Join(dir, "memory.limit_in_bytes")); ok {
					least(n, "cgroup memory.limit_in_bytes")
					break
				}
			}
		}
	}
	return limit, why
}

// readLimit is a cgroup memory limit file's bytes; "max", a file not
// there or not read, is none.
func readLimit(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// take waits for room for one Claude Code process, what naming it in the
// log, and gives the func that frees it once the process has ended; left
// says magpie ended it (leave). An ask that finds the room full lets go of
// an idle or long-parked run for it, unless enough are ending already, and
// waits for one to end: claudeRoomWait at most, or as long as ctx lasts.
func (c *claudeRoomT) take(ctx context.Context, what string) (func(left bool), error) {
	var deadline <-chan time.Time
	var timer *time.Timer
	began := time.Now()
	waiting := false
	defer func() {
		if timer != nil {
			timer.Stop()
		}
		if waiting {
			c.mu.Lock()
			c.waiting--
			c.mu.Unlock()
		}
	}()
	for {
		most, why := claudeRunsMost()
		c.mu.Lock()
		if most <= 0 || c.live < most {
			c.live++
			c.mu.Unlock()
			if waiting {
				log.Printf("claude: %s had room after %s", what, time.Since(began).Round(time.Millisecond))
			}
			return c.release, nil
		}
		if !waiting {
			waiting = true
			c.waiting++
			log.Printf("claude: %s waits for room: %d Claude Code processes run, %d may (%s)", what, c.live, most, why)
		}
		freed, live := c.freed, c.live
		evict := c.live-c.leaving >= most && c.leaving < c.waiting
		c.mu.Unlock()
		if evict && letGoForRoom(most) {
			continue // what it let go ends soon: waited for below
		}
		if timer == nil {
			timer = time.NewTimer(claudeRoomWait)
			deadline = timer.C
		}
		select {
		case <-freed:
		case <-time.After(roomLook):
			// a run parked since before comes to have waited parkLongest
		case <-deadline:
			log.Printf("claude: %s found no room in %s", what, claudeRoomWait)
			return nil, &claudeFull{live: live, most: most, why: why, waited: claudeRoomWait}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// leave counts a process magpie has asked to end as ending, so asks
// waiting for room don't let go of another for it.
func (c *claudeRoomT) leave() {
	c.mu.Lock()
	c.leaving++
	c.mu.Unlock()
}

// roomLook is how often an ask waiting for room looks again for a run to
// let go, unasked.
var roomLook = 5 * time.Second

// nudge has the asks waiting for room look again: a run has come to wait
// for its next turn.
func (c *claudeRoomT) nudge() {
	c.mu.Lock()
	if c.waiting > 0 {
		close(c.freed)
		c.freed = make(chan struct{})
	}
	c.mu.Unlock()
}

// release frees a process's room once it has ended.
func (c *claudeRoomT) release(left bool) {
	c.mu.Lock()
	c.live--
	if left {
		c.leaving--
	}
	close(c.freed)
	c.freed = make(chan struct{})
	c.mu.Unlock()
}

// letGoForRoom lets go of one run for room, of any bridge: the one idle
// longest, its session shelved for its conversation's next turn as when
// more than idleMost wait, else the one waiting longest on tool results
// past parkLongest. It says whether it found one. Busy runs, and those
// waiting on tool calls made recently, are kept.
func letGoForRoom(most int) bool {
	var idle, parked *subscriptionRun
	var parkedAt time.Time
	var idleBridge *subscriptionBridge
	var idleKey string
	now := time.Now()
	bridges.Range(func(k, _ any) bool {
		b := k.(*subscriptionBridge)
		b.mu.Lock()
		for key, run := range b.idle {
			if idle == nil || run.idleAt.Before(idle.idleAt) {
				idle, idleBridge, idleKey = run, b, key
			}
		}
		for _, run := range b.runs {
			run.mu.Lock()
			at, closed, killed := run.parkedAt, run.closed, run.killed
			run.mu.Unlock()
			if !closed && !killed && !at.IsZero() && now.Sub(at) >= parkLongest && (parked == nil || at.Before(parkedAt)) {
				parked, parkedAt = run, at
			}
		}
		b.mu.Unlock()
		return true
	})
	why := "magpie let it go to make room for another Claude Code (" + strconv.Itoa(most) + " may run at once)"
	if idle != nil {
		b := idleBridge
		b.mu.Lock()
		if b.idle[idleKey] != idle {
			b.mu.Unlock()
			return true // taken meanwhile: the room is looked at again
		}
		b.shelve(idle)
		delete(b.idle, idleKey)
		if idle.idleKey == idleKey {
			idle.idleKey = ""
		}
		b.mu.Unlock()
		log.Printf("claude: an idle run on %s is let go to make room (%d may run at once); its conversation's next turn resumes its session", idle.model, most)
		idle.abortFor(why)
		return true
	}
	if parked != nil {
		log.Printf("claude: a run on %s waiting on tool results since %s is let go to make room (%d may run at once)", parked.model, parkedAt.Format(time.TimeOnly), most)
		parked.abortFor(why)
		return true
	}
	return false
}
