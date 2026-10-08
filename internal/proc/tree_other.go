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
// caller, which waits for it next.
func (group) kill(cmd *exec.Cmd) {
	pgid := cmd.Process.Pid
	if signalGroup(pgid) != nil {
		return // none of it is left
	}
	go func() {
		for end := time.Now().Add(killFor); time.Now().Before(end); {
			time.Sleep(5 * time.Millisecond)
			if signalGroup(pgid) != nil {
				return
			}
		}
	}()
}

// signalGroup kills the group pgid leads; a var so a test can have it miss.
var signalGroup = func(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

// killFor is how long a group is signalled again while some of it lives.
var killFor = 10 * time.Second

// sweep ends what is left of the group once its leader is gone: the group
// keeps the leader's pid while any of it runs, so none else has it.
func (g group) sweep(cmd *exec.Cmd) { g.kill(cmd) }
