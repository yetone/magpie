package proc

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// ProbeContext is CommandContext for a command magpie only asks something
// of and waits on for the answer: who a CLI says is signed in, its version.
// The CLI is often a script (a version manager's wrapper, a shim), and
// killing it on the timeout left what it had started running; a probe still
// asking when magpie quit or updated itself was left whole, adopted by init
// with nothing to end it, and one stuck in a loop held a core for hours. A
// probe leads a group of its own on Unix, the context ending kills the whole
// group, and EndProbes does the same for those still asking when magpie
// exits. ctx must end (a timeout): the probe is forgotten when it does.
func ProbeContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	ctx, cancel := context.WithCancel(ctx)
	p := &probe{cancel: cancel, killing: make(chan struct{}), ended: make(chan struct{})}
	cmd := CommandContext(ctx, name, args...)
	// a probe never opens an app: asked its version, an app's own program
	// puts its window up instead (appMain on a Mac, guiExe on Windows)
	if cmd.Err == nil && (appMain(cmd.Path) || guiExe(cmd.Path)) {
		cmd.Err = errAppMain
	}
	probeGroup(cmd, p)
	probes.Lock()
	probes.m[p] = struct{}{}
	probes.Unlock()
	context.AfterFunc(ctx, func() {
		probes.Lock()
		delete(probes.m, p)
		probes.Unlock()
	})
	return cmd
}

type probe struct {
	cancel         context.CancelFunc
	killOnce, once sync.Once
	killing, ended chan struct{} // closed once its kill began, and once it was over
}

func (p *probe) kill() { p.killOnce.Do(func() { close(p.killing) }) }
func (p *probe) end()  { p.once.Do(func() { close(p.ended) }) }

var probes = struct {
	sync.Mutex
	m map[*probe]struct{}
}{m: map[*probe]struct{}{}}

// endWait is how long EndProbes waits for a probe's kill to begin: one that
// had answered, or never started, has none.
var endWait = time.Second

// EndProbes kills every probe still running, with what it started, and
// waits until none of that is left: magpie is exiting or putting a new
// version in its place, and nothing would end them after. A kill that missed
// a process being forked signals the group again until it is empty, and an
// exit before that left the missed one running, adopted by init (#1278); the
// wait is bounded by how long a kill goes on (killFor on Unix).
func EndProbes() {
	probes.Lock()
	ps := make([]*probe, 0, len(probes.m))
	for p := range probes.m {
		ps = append(ps, p)
	}
	probes.Unlock()
	for _, p := range ps {
		p.cancel()
	}
	begun := time.Now().Add(endWait)
	over := begun.Add(killWait())
	for _, p := range ps {
		select {
		case <-p.killing:
		case <-time.After(time.Until(begun)):
			continue // one that had answered already, or never started
		}
		select {
		case <-p.ended:
		case <-time.After(time.Until(over)):
			return
		}
	}
}
