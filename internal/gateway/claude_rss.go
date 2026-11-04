package gateway

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// A Claude Code run is kept for its conversation's next turn, turn after
// turn, for days on a service that is never restarted, and nothing bounded
// what one process grew to: claudeRoom bounds how many run, not how large
// each is. On a 31 GiB host one reached 8 GiB resident after four days of
// a large conversation, and the kernel's OOM killer took the machine's
// other work with it (#3256). Once a turn is over, a run holding more than
// claudeRunBudget is let go instead of kept: its session is shelved, and
// the next turn resumes it in a new Claude Code, which asks Anthropic the
// same cached prefix.
//
// The budget is an eighth of what magpie's processes may use (memLimit on
// Linux, the computer's memory elsewhere), claudeRunFloor at least; where
// neither is known, claudeRunUnknown. A Claude Code holds ~100 MiB of its
// own at the start (claudeRoom).
const (
	claudeRunFloor   = 1 << 30
	claudeRunUnknown = 4 << 30
)

// claudeRunRSS reads a run's resident memory, for tests to give one.
var claudeRunRSS = func(t *proc.Tree) (int64, bool) { return t.RSS() }

var runBudget struct {
	sync.Once
	n   int64
	why string
}

// claudeRunBudget is the most a Claude Code run may hold resident and
// still be kept for its next turn, and what sized it.
func claudeRunBudget() (int64, string) {
	runBudget.Do(func() {
		limit, why := memLimit(memGOOS, memRoot)
		if limit <= 0 && memOwn() {
			limit, why = proc.MemTotal(), "the computer's memory"
		}
		if limit <= 0 {
			runBudget.n, runBudget.why = claudeRunUnknown, "memory not known"
			return
		}
		runBudget.n = max(limit/8, claudeRunFloor)
		runBudget.why = fmt.Sprintf("an eighth of %d MiB (%s), %d MiB at least", limit>>20, why, claudeRunFloor>>20)
	})
	return runBudget.n, runBudget.why
}

// oversized says the run holds more than claudeRunBudget times share/2
// (2 a whole turn, 3 a run that waits on tool calls), and logs what it
// holds either way: a run's growth across turns is in the log, for an OOM
// to be put down to the process it was. One waiting on its caller's tool
// results has no session to shelve there: let go, it is started anew with
// the whole conversation, which Anthropic's cache has not seen, so it is
// let go only past half as much again.
func (r *subscriptionRun) oversized(share int64) (string, bool) {
	r.mu.Lock()
	t := r.tree
	r.mu.Unlock()
	if t == nil {
		return "", false
	}
	rss, ok := claudeRunRSS(t)
	if !ok {
		return "", false
	}
	budget, why := claudeRunBudget()
	budget = budget * share / 2
	log.Printf("claude: Claude Code pid %d (%s) holds %d MiB after its turn; %d MiB may be kept (%s)", r.pid(), r.model, rss>>20, budget>>20, why)
	if rss <= budget {
		return "", false
	}
	return fmt.Sprintf("its Claude Code held %d MiB, over the %d MiB a run may hold to be kept (%s)", rss>>20, budget>>20, why), true
}

// pid is the run's Claude Code process id, 0 before it starts. It is
// read once tree is set under mu: cmd.Process is written by cmd.Start,
// which a list of the runs may be reading through.
func (r *subscriptionRun) pid() int {
	r.mu.Lock()
	t := r.tree
	r.mu.Unlock()
	if t == nil || r.cmd == nil || r.cmd.Process == nil {
		return 0
	}
	return r.cmd.Process.Pid
}

// letGoOversized lets go of a run whose turn was whole and that holds too
// much to be kept (oversized): its session is shelved at key, as one let go
// past idleMost is, so the next turn resumes it in a new Claude Code.
func (b *subscriptionBridge) letGoOversized(r *subscriptionRun, key, conv, why string) {
	b.mu.Lock()
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if !closed {
		if r.idleKey != "" && b.idle[r.idleKey] == r {
			delete(b.idle, r.idleKey)
		}
		r.idleKey, r.idleAt = key, time.Now()
		if conv != "" {
			r.convKey = conv
		}
		b.shelve(r)
		r.idleKey = ""
	}
	b.mu.Unlock()
	log.Printf("claude: Claude Code pid %d (%s) is let go: %s; its conversation's next turn resumes its session in a new one", r.pid(), r.model, why)
	r.abortFor(why)
}
