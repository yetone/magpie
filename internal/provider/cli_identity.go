package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cliIdentity is who an agent's CLI says is signed in (`cursor-agent about`,
// `devin auth status`). Asking takes the CLI seconds, ten at worst, and
// every request of the gateway and every page of the window reads the
// accounts, so after the first answer a stale one is served while a fresh
// one is fetched behind it. The answer is kept on disk too: a magpie just
// started serves the last one at once rather than holding everything for
// the CLIs (#123), and asks them again behind it. An ask that couldn't tell
// (the CLI failed, ran out of time or printed something else) leaves what
// was served as it was: taken for nobody signed in, it dropped the account
// from the Providers page and from routing until the next ask (#154).
type cliIdentity struct {
	sync.Mutex
	name       string
	exe        func() string
	ask        func() (user, plan string, ok bool, err error) // err: couldn't tell
	at         time.Time
	refreshing bool
	done       chan struct{} // closed when the ask under way has answered
	gen        int           // bumped by forget: an ask from before is dropped
	waited     bool          // a look waited for the first answer already
	read       bool          // the one kept on disk was looked for
	user, plan string
	ok         bool
}

type keptIdentity struct {
	User string `json:"user,omitempty"`
	Plan string `json:"plan,omitempty"`
	OK   bool   `json:"ok"`
}

var identityFile sync.Mutex

// keepIdentities is whether the answers are kept on disk. Off under test: an
// ask behind a request answers after it, often once the test that made the
// request has ended, and wrote cli-identity.json into that test's config
// folder as it was removed ("unlinkat …/magpie: directory not empty",
// TestStandaloneToolOutputResponsesRoutes in a whole run). The tests of
// keeping turn it on.
var keepIdentities atomic.Bool

func init() { keepIdentities.Store(!testing.Testing()) }

func identityPath() string { return filepath.Join(filepath.Dir(Path()), "cli-identity.json") }

func readIdentities() map[string]keptIdentity {
	m := map[string]keptIdentity{}
	if b, err := os.ReadFile(identityPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (c *cliIdentity) keep() {
	if !keepIdentities.Load() {
		return
	}
	identityFile.Lock()
	defer identityFile.Unlock()
	m := readIdentities()
	k := keptIdentity{User: c.user, Plan: c.plan, OK: c.ok}
	// nobody signed in is kept too, or a CLI signed out was waited for at
	// every start
	if was, found := m[c.name]; found && was == k {
		return
	}
	m[c.name] = k
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = writePrivate(identityPath(), append(b, '\n'))
	}
}

func (c *cliIdentity) get() (user, plan string, ok bool) {
	c.Lock()
	defer c.Unlock()
	if c.at.IsZero() && !c.read {
		c.read = true
		identityFile.Lock()
		k, found := readIdentities()[c.name]
		identityFile.Unlock()
		// served now, asked again behind it; a CLI since removed has no one
		if found && c.exe() != "" {
			c.user, c.plan, c.ok = k.User, k.Plan, k.OK
			c.at = time.Unix(1, 0)
		}
	}
	if c.at.IsZero() {
		// never answered: waited for, but only so long and only once — a
		// request looks several times, and a CLI that takes longer is
		// answered as nobody signed in until it has (#123)
		done := c.refresh()
		if !c.waited {
			c.waited = true
			c.Unlock()
			select {
			case <-done:
			case <-time.After(firstAsk):
			}
			c.Lock()
		}
	} else if time.Since(c.at) > time.Minute {
		c.refresh()
	}
	return c.user, c.plan, c.ok
}

// firstAsk is how long a look waits for a CLI never answered before.
var firstAsk = 3 * time.Second

// refresh asks the CLI behind what is served, once at a time; the channel is
// closed when it has answered. Called with c locked.
func (c *cliIdentity) refresh() chan struct{} {
	if c.refreshing {
		return c.done
	}
	c.refreshing, c.done = true, make(chan struct{})
	gen, done := c.gen, c.done
	go func() {
		u, p, ok, err := c.ask()
		c.Lock()
		defer c.Unlock()
		defer close(done)
		if gen != c.gen {
			return
		}
		c.at, c.refreshing = time.Now(), false
		if err != nil {
			return // asked again in a minute; what was served stays
		}
		c.user, c.plan, c.ok = u, p, ok
		c.keep()
	}()
	return done
}

// forget has the next look ask the CLI and wait for it: after a sign-in or
// a sign-out, the last answer is the wrong one.
func (c *cliIdentity) forget() {
	c.Lock()
	c.at, c.read = time.Time{}, true
	c.gen++
	c.refreshing, c.waited = false, false
	c.Unlock()
}
