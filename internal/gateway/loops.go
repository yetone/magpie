package gateway

import (
	"log"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// loops is the work ListenAndServe starts beside the server: the
// Keep… loops, WhileServing's, the files it watches. Its end waits for
// them, so nothing it started writes into the config folder after it
// returned: KeepAutoPicksHeld's auto-picks.json, a retiring move's
// migrations.json, written as ctx ended, landed in a test's folder as it
// was being removed (#7232).
type loops struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	running map[string]int
	ended   bool
}

// loopsEndFor bounds the wait at the end: a loop that doesn't end with its
// ctx is named in the log and left running, rather than holding magpie's
// quit.
var loopsEndFor = 10 * time.Second

// Go runs f as the loop named name, unless the loops have ended:
// holdServing starts its work once another magpie lets go of the config
// folder, which may be as this one ends.
func (l *loops) Go(name string, f func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	if l.running == nil {
		l.running = map[string]int{}
	}
	l.running[name]++
	l.wg.Go(func() {
		defer func() {
			l.mu.Lock()
			if l.running[name]--; l.running[name] == 0 {
				delete(l.running, name)
			}
			l.mu.Unlock()
		}()
		f()
	})
}

// end starts no loop after it, and waits for those running, for at most
// loopsEndFor. Their ctx has ended: each one ends on its own.
func (l *loops) end() {
	l.mu.Lock()
	l.ended = true
	l.mu.Unlock()
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	t := time.NewTimer(loopsEndFor)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		l.mu.Lock()
		var names []string
		for n := range l.running {
			names = append(names, n)
		}
		l.mu.Unlock()
		slices.Sort(names)
		log.Printf("gateway: ending with %s still running %s after its end", strings.Join(names, ", "), loopsEndFor)
	}
}

// loopName is f's function name, for the log of a loop that outlived the
// gateway's end.
func loopName(f any) string {
	if fn := runtime.FuncForPC(reflect.ValueOf(f).Pointer()); fn != nil {
		return strings.TrimPrefix(fn.Name(), "github.com/yetone/magpie/internal/")
	}
	return "WhileServing"
}
