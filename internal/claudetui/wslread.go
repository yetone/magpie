package claudetui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

// A Claude Code in WSL whose transcripts Windows can't read (the distro
// user's own folder, through \\wsl$, which a session with no 9P
// redirector, one over ssh, has none of), or whose home the probe didn't
// find, has its transcript read inside the distro: a shell there, run
// through wsl.exe, waits for the session's file and tails it, and the
// helper copies what it prints to a file of its own, which it reads as
// it reads any transcript. The shell finds the folder as Claude Code
// does, under its own $HOME.

// wslReadScript is the shell's script: $1 is the session id. It prints
// "ready" once it knows how much of the file is there before Claude Code
// starts (a resumed session's earlier turns), then whatever is written
// after, until its standard input ends.
const wslReadScript = `d="$HOME/.claude/projects"; s="$1"; n=0
for f in "$d"/*/"$s".jsonl; do [ -f "$f" ] && n=$(wc -c <"$f"); done
echo ready
( while :; do for f in "$d"/*/"$s".jsonl; do [ -f "$f" ] && exec tail -c +$((n+1)) -F "$f" 2>/dev/null; done; sleep 0.1; done ) &
cat >/dev/null; kill $! 2>/dev/null`

// wslReadCommand is the shell's command; a var for tests, which run the
// script here.
var wslReadCommand = func(distro, sid string) *exec.Cmd {
	return proc.Command("wsl.exe", "-d", distro, "--exec", "/bin/sh", "-c", wslReadScript, "sh", sid)
}

// how long the shell has to say it is ready
var wslReadWait = 30 * time.Second

var sidRe = lazyre.New(`^[0-9A-Za-z-]+$`)

// wslRead is a transcript read inside the distro, copied to path.
type wslRead struct {
	path string
	cmd  *exec.Cmd
	in   io.WriteCloser
	done chan struct{}
}

// startWSLRead starts reading session sid's transcript inside distro,
// before Claude Code starts, into a file under folder projects as a
// projects folder has it (<projects>/<folder>/<sid>.jsonl).
func startWSLRead(distro, sid, projects string) (*wslRead, error) {
	if !sidRe.MatchString(sid) {
		return nil, fmt.Errorf("a session id %q", sid)
	}
	if err := os.MkdirAll(filepath.Join(projects, "wsl"), 0o700); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(projects, "wsl", sid+".jsonl"))
	if err != nil {
		return nil, err
	}
	cmd := wslReadCommand(distro, sid)
	in, err := cmd.StdinPipe()
	if err != nil {
		f.Close()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, err
	}
	r := &wslRead{path: f.Name(), cmd: cmd, in: in, done: make(chan struct{})}
	rd := bufio.NewReader(out)
	ready := make(chan error, 1)
	go func() {
		line, err := rd.ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "ready" {
			err = fmt.Errorf("it said %q", line)
		}
		ready <- err
		if err == nil {
			_, _ = io.Copy(f, rd)
		}
		f.Close()
		close(r.done)
	}()
	select {
	case err = <-ready:
	case <-time.After(wslReadWait):
		err = errors.New("it didn't start")
	}
	if err != nil {
		r.stop()
		return nil, fmt.Errorf("the transcript can't be read in WSL: %v", err)
	}
	return r, nil
}

// settle waits, once Claude Code has ended, for the last of what it
// wrote: until the copy hasn't grown for a moment.
func (r *wslRead) settle() {
	if r == nil {
		return
	}
	var size int64 = -1
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		fi, err := os.Stat(r.path)
		if err != nil || fi.Size() == size {
			return
		}
		size = fi.Size()
		select {
		case <-r.done:
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// stop ends the shell: its standard input ends, and it ends its tail.
func (r *wslRead) stop() {
	if r == nil {
		return
	}
	_ = r.in.Close()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		_ = r.cmd.Process.Kill()
		<-r.done
	}
	_ = r.cmd.Wait()
}
