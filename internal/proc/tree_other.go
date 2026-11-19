//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
	"time"
)

// group is the session the command leads: its children are in it unless
// they make one of their own, and grok's MCP servers do, but those end when
// their input closes with the CLI.
type group struct{}

func ownGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

func (group) join(*exec.Cmd) {}

// kill ends every process of the group. One signal to the group misses a
// process being forked as it is sent (macOS): a script that keeps starting
// others, as a launcher that calls itself does (Cursor's `cursor agent`
// running a ~/.local/bin/cursor-agent that runs `cursor agent`, #1278), had
// a child of the moment live on with its parent gone, adopted by init, and
// call itself until the user could start no process. What the signal missed
// is still in the group, and the group keeps its id while any of it lives,
// so the group is signalled again until none of it is left. Its leader,
// killed and not yet waited for, counts until it is: hence behind the
// caller, which waits for it next. The channel closes once none of the group
// is left, or killFor has passed: magpie exiting or putting a new version in
// its place waits for it, since an exit ends the loop with what it missed
// still running (#1278).
func (group) kill(cmd *exec.Cmd) <-chan struct{} {
	gone := make(chan struct{})
	pgid := cmd.Process.Pid
	// the one kill function for the whole kill, taken once: the loop below
	// outlives the caller, and a test swapping signalGroup for its own and
	// putting the old one back when it ends must not race its reads (the
	// data race TestProbeKillEndsWhatTheSignalMissed went red under).
	signal := signalGroup
	if signal(pgid) != nil {
		close(gone) // none of it is left
		return gone
	}
	go func() {
		defer close(gone)
		for end := time.Now().Add(killFor); time.Now().Before(end); {
			time.Sleep(5 * time.Millisecond)
			if signal(pgid) != nil {
				return
			}
		}
	}()
	return gone
}

// term asks the command alone to end: what it started is ended with the
// group once it has.
func (group) term(cmd *exec.Cmd) error { return cmd.Process.Signal(syscall.SIGTERM) }

// signalGroup kills the group pgid leads; a var so a test can have it miss.
// Readers take it once at kill's entry, never from the loop's goroutine.
var signalGroup = func(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

// killFor is how long a group is signalled again while some of it lives.
var killFor = 10 * time.Second

// sweep ends what is left of the group once its leader is gone: the group
// keeps the leader's pid while any of it runs, so none else has it.
func (g group) sweep(cmd *exec.Cmd) { _ = g.kill(cmd) }

func (group) setEfficiency(bool) {}
