package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// A folder on PATH on a mount that no longer answers (a network share
// gone away, an autofs path whose server is down) holds every stat and
// open in it until the mount comes back, which can be never. Looking a
// command up walks PATH in order, so a command not found before such a
// folder blocked whoever asked: magpie's start-up asks for every agent's
// command (agent.RenameLegacy → vscodePortableData → code-insiders), and
// `open -a` took over a minute to start while the same binary run from a
// shell, with another PATH, took one second (ifccod7391564 on X). A PATH
// folder is now asked in a goroutine of its own first, and one that
// hasn't answered in reachWait is left out of every look until it does.

// reachWait is how long a PATH folder has to answer; reachKeep how long
// its answer is trusted before it is asked again.
var (
	reachWait = time.Second
	reachKeep = time.Minute
)

// probeDir is the asking: a stat and an open of the folder, as a lookup
// does. Whatever either answers, the folder answered. A var for the tests.
var probeDir = func(dir string) {
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		if f, err := os.Open(dir); err == nil {
			f.Close()
		}
	}
}

var reach struct {
	sync.Mutex
	m map[string]*reachProbe
}

type reachProbe struct {
	done     chan struct{}
	deadline time.Time // when a probe not done counts as hung
	at       time.Time // when it answered, set before done closes
}

// answers reports whether dir answers within reachWait. A probe still out
// past its deadline isn't started again: every look leaves the folder out
// at once until that probe returns.
func answers(dir string) bool {
	if dir == "" {
		return true
	}
	now := time.Now()
	reach.Lock()
	p := reach.m[dir]
	if p != nil {
		select {
		case <-p.done:
			if now.Sub(p.at) < reachKeep {
				reach.Unlock()
				return true
			}
			p = nil // stale: asked again below
		default:
		}
	}
	if p == nil {
		p = &reachProbe{done: make(chan struct{}), deadline: now.Add(reachWait)}
		if reach.m == nil {
			reach.m = map[string]*reachProbe{}
		}
		reach.m[dir] = p
		go func(p *reachProbe) {
			probeDir(dir)
			p.at = time.Now()
			close(p.done)
		}(p)
	}
	reach.Unlock()
	wait := time.Until(p.deadline)
	if wait <= 0 {
		select {
		case <-p.done:
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-p.done:
		return true
	case <-t.C:
		return false
	}
}

// hungDirs are the folders of dirs that don't answer; nil when every one
// does. The folders are asked at once, so several gone mounts cost one
// reachWait, not one each.
func hungDirs(dirs []string) map[string]bool {
	if allAnswered(dirs) {
		return nil
	}
	var (
		mu   sync.Mutex
		hung map[string]bool
		wg   sync.WaitGroup
	)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			if !answers(d) {
				mu.Lock()
				if hung == nil {
					hung = map[string]bool{}
				}
				hung[d] = true
				mu.Unlock()
			}
		}(d)
	}
	wg.Wait()
	return hung
}

// allAnswered reports whether every folder of dirs answered within
// reachKeep, so a look asks none of them (no goroutine each).
func allAnswered(dirs []string) bool {
	now := time.Now()
	reach.Lock()
	defer reach.Unlock()
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := reach.m[d]
		if p == nil {
			return false
		}
		select {
		case <-p.done:
			if now.Sub(p.at) >= reachKeep {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// lookIn is exec.LookPath over dirs alone, in their order: the command's
// name in each folder asked as a path, as exec.LookPath does along PATH.
// The current folder is left out, as exec.LookPath refuses a command
// found there (exec.ErrDot).
func lookIn(name string, dirs []string) (string, error) {
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		if p, err := execLookPath(filepath.Join(d, name)); err == nil {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
