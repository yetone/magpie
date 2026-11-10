package edit

import (
	"maps"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	top    map[string]tomlTop
	tables map[string]map[string]string // nil for a table the file hasn't
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
