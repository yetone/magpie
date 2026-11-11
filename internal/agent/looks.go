package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentplug"
)

// Look is how an agent of Clients() is named and drawn.
type Look struct {
	ID, Name, Shared, Icon string
}

// looksFresh is how long Looks keeps what it built. Only a new omp or
// QwenPaw profile changes the list without a plugin changing; its rows
// show the agent's id until then, or until the Agents page's refresh
// (Rescan). Nothing is kept in a test binary, where a test that adds an
// agent reads it at once, unless the test says.
var looksFresh = looksFreshInApp

const looksFreshInApp = 30 * time.Second

func init() {
	if testing.Testing() {
		looksFresh = 0
	}
}

var looksKept struct {
	sync.Mutex
	at      time.Time
	plugged []*agentplug.Agent
	list    []Look
	built   int // times the list was built, for the tests
}

// Looks is each agent of Clients() as it is named and drawn, in that
// order, for pages that only name and draw rows by agent: the Sessions
// and Usage pages ask on every poll, and Clients() builds every agent,
// ~2.5 ms and ~380 KB of stats, paths and environment each time. It is
// built again after looksFresh, or at once when the plugins' agents
// change. What it returns is shared: callers only read it.
func Looks() []Look {
	plugged, _ := agentplug.List()
	k := &looksKept
	k.Lock()
	defer k.Unlock()
	if k.list != nil && time.Since(k.at) < looksFresh && samePlugged(k.plugged, plugged) {
		return k.list
	}
	cs := Clients()
	k.built++
	list := make([]Look, len(cs))
	for i, a := range cs {
		list[i] = Look{a.ID, a.Name, a.SharedName(), a.Icon}
	}
	k.at, k.plugged, k.list = time.Now(), plugged, list
	return list
}

func samePlugged(a, b []*agentplug.Agent) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// forgetLooks has the next Looks build again.
func forgetLooks() {
	looksKept.Lock()
	looksKept.list = nil
	looksKept.Unlock()
}

func looksBuilt() int {
	looksKept.Lock()
	defer looksKept.Unlock()
	return looksKept.built
}
