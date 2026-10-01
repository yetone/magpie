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
	p := &probe{cancel: cancel, ended: make(chan struct{})}
	cmd := CommandContext(ctx, name, args...)
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
	cancel context.CancelFunc
	once   sync.Once
	ended  chan struct{} // closed once it was killed
}

func (p *probe) end() { p.once.Do(func() { close(p.ended) }) }

var probes = struct {
	sync.Mutex
	m map[*probe]struct{}
}{m: map[*probe]struct{}{}}

// endWait is how long EndProbes waits for the probes to be killed.
var endWait = time.Second

// EndProbes kills every probe still running, with what it started, and
// waits a moment for that: magpie is exiting or putting a new version in its
// place, and nothing would end them after.
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
	deadline := time.After(endWait)
	for _, p := range ps {
		select {
		case <-p.ended:
		case <-deadline:
			return // one that had answered already, or never started
		}
	}
}
