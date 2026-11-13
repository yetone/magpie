package edit

import (
	"maps"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/yetone/magpie/internal/filememo"
)

// A file's whole parse is let go once nothing has needed it for
// tomlIdle. Codex's config.toml keeps a table for every project it was
// trusted in: 1.5 MB, ~11k tables, 4.5 MB parsed, kept for as long as
// magpie ran because the Codex app's route is looked at every 3s
// (codexAppRoute). What those looks ask, a top-level key or one table's
// scalars, is kept beside it as tomlAnswers, which is what stays.
const tomlIdle = time.Minute

// tomlParses counts files parsed whole, for tests.
var tomlParses atomic.Int64

var tomlKept struct {
	last  atomic.Int64 // UnixNano of the last whole parse used
	armed atomic.Bool
	off   bool
}

func init() { tomlKept.off = testing.Testing() }

// tomlUsed notes a use of a file's whole parse, and arms the timer that
// lets it go once uses stop.
func tomlUsed() {
	if tomlKept.off {
		return
	}
	tomlKept.last.Store(time.Now().UnixNano())
	if tomlKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(tomlIdle, tomlIdleCheck)
	}
}

func tomlIdleCheck() {
	idle := time.Since(time.Unix(0, tomlKept.last.Load()))
	if idle < tomlIdle {
		time.AfterFunc(tomlIdle-idle, tomlIdleCheck)
		return
	}
	tomlKept.armed.Store(false)
	dropTOMLParses()
	// a use between the check and the drop armed nothing: look again
	if time.Since(time.Unix(0, tomlKept.last.Load())) < tomlIdle && tomlKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(tomlIdle, tomlIdleCheck)
	}
}

// dropTOMLParses lets go of every file's whole parse, tomlAnswers kept,
// and gives the memory back to the system: left to the scavenger, a
// let-go parse stayed in magpie's footprint.
func dropTOMLParses() {
	filememo.DropKind("toml file")
	debug.FreeOSMemory()
}

// tomlAnswers is what GetTOMLTop and GetTOMLTable answered for one read
// of a file, kept as filememo keeps that read: a file changed is a new
// tomlAnswers, empty.
type tomlAnswers struct {
	sync.Mutex
	top       map[string]tomlTop
	tables    map[string]map[string]string // nil for a table the file hasn't
	names     []string                     // TOMLTables' answer, once haveNames
	haveNames bool
	namesHeld bool // in namesKept.held
}

type tomlTop struct {
	v  string
	ok bool
}

// tomlAnswersOf is path's tomlAnswers, nil when the file can't be read
// (the caller asks the whole parse, which says why).
func tomlAnswersOf(path string) *tomlAnswers {
	a, err := filememo.Read("toml answers", path, func([]byte) (*tomlAnswers, error) {
		return &tomlAnswers{}, nil
	})
	if err != nil {
		return nil
	}
	return a
}

func (a *tomlAnswers) getTop(key string) (tomlTop, bool) {
	a.Lock()
	defer a.Unlock()
	v, ok := a.top[key]
	return v, ok
}

func (a *tomlAnswers) setTop(key string, v tomlTop) {
	a.Lock()
	defer a.Unlock()
	if a.top == nil {
		a.top = map[string]tomlTop{}
	}
	a.top[key] = v
}

// getTable is a copy of the table's answer: callers own what they get.
func (a *tomlAnswers) getTable(name string) (map[string]string, bool) {
	a.Lock()
	defer a.Unlock()
	t, ok := a.tables[name]
	if !ok || t == nil {
		return nil, ok
	}
	return maps.Clone(t), true
}

func (a *tomlAnswers) setTable(name string, t map[string]string) {
	a.Lock()
	defer a.Unlock()
	if a.tables == nil {
		a.tables = map[string]map[string]string{}
	}
	a.tables[name] = maps.Clone(t)
}

// getNames is TOMLTables' kept answer, shared, so read only.
func (a *tomlAnswers) getNames() ([]string, bool) {
	a.Lock()
	defer a.Unlock()
	if a.haveNames {
		namesUsed(a)
	}
	return a.names, a.haveNames
}

// setNames keeps a copy of names that holds no piece of the file's text.
// The copies share one buffer: Codex's config.toml names ~11k tables, one
// allocation each, rounded up to its size class, for as long as magpie
// runs (#2278).
func (a *tomlAnswers) setNames(names []string) {
	var kept []string
	if names != nil {
		kept = packStrings(names)
	}
	a.Lock()
	defer a.Unlock()
	a.names, a.haveNames = kept, true
	namesUsed(a)
}

// A file's kept table names are let go too once no one has listed them
// for namesIdle: Codex's ~11k are 1.2 MB, listed by a one-time rename at
// start and then only by the Agents page and a sync, and were held for
// as long as magpie ran. The next list parses the file again: 9 ms and
// 21 MB of garbage for that config on an M5 Pro.
const namesIdle = 5 * time.Minute

var namesKept struct {
	last  atomic.Int64 // UnixNano of the last list answered
	armed atomic.Bool
	mu    sync.Mutex
	held  []weak.Pointer[tomlAnswers]
}

// namesUsed notes a list of a's names, and arms the timer that lets them
// go once lists stop. a.Lock is held.
func namesUsed(a *tomlAnswers) {
	if tomlKept.off {
		return
	}
	namesKept.last.Store(time.Now().UnixNano())
	if !a.namesHeld {
		a.namesHeld = true
		namesKept.mu.Lock()
		namesKept.held = append(namesKept.held, weak.Make(a))
		namesKept.mu.Unlock()
	}
	if namesKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(namesIdle, namesIdleCheck)
	}
}

func namesIdleCheck() {
	idle := time.Since(time.Unix(0, namesKept.last.Load()))
	if idle < namesIdle {
		time.AfterFunc(namesIdle-idle, namesIdleCheck)
		return
	}
	namesKept.armed.Store(false)
	dropTOMLNames()
	// a list between the check and the drop armed nothing: look again
	if time.Since(time.Unix(0, namesKept.last.Load())) < namesIdle && namesKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(namesIdle, namesIdleCheck)
	}
}

// dropTOMLNames lets go of every file's kept table names; the other
// answers stay.
func dropTOMLNames() {
	namesKept.mu.Lock()
	held := namesKept.held
	namesKept.held = nil
	namesKept.mu.Unlock()
	for _, w := range held {
		if a := w.Value(); a != nil {
			a.Lock()
			a.names, a.haveNames, a.namesHeld = nil, false, false
			a.Unlock()
		}
	}
	debug.FreeOSMemory()
}

// packStrings copies ss into one buffer; the copies share nothing with ss.
func packStrings(ss []string) []string {
	n := 0
	for _, s := range ss {
		n += len(s)
	}
	var b strings.Builder
	b.Grow(n)
	for _, s := range ss {
		b.WriteString(s)
	}
	all := b.String()
	out := make([]string, len(ss))
	off := 0
	for i, s := range ss {
		out[i] = all[off : off+len(s)]
		off += len(s)
	}
	return out
}
