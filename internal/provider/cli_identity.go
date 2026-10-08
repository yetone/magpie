package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
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
// from the Providers page and from routing until the next ask (#154). One
// that keeps failing, or taking slowAsk or longer, is asked less often, the
// wait doubling up to slowestAsk: a cursor-agent that calls itself ran out
// of time every minute, after hundreds of processes, and answered nobody
// signed in all the same, as no token is kept (#1278).
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
	retry      time.Duration // after asks that failed or were slow, the wait before the next
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

func identityPath() string { return filepath.Join(filepath.Dir(Path()), "cli-identity.json") }

func readIdentities() map[string]keptIdentity {
	m := map[string]keptIdentity{}
	if b, err := os.ReadFile(identityPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (c *cliIdentity) keep() {
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
	} else if time.Since(c.at) > max(time.Minute, c.retry) {
		c.refresh()
	}
	return c.user, c.plan, c.ok
}

// firstAsk is how long a look waits for a CLI never answered before.
var firstAsk = 3 * time.Second

// slowAsk is how long an ask takes for the CLI to be asked less often.
var slowAsk = 5 * time.Second

// slowestAsk is the longest wait before asking again a CLI that keeps
// failing or taking long to answer.
const slowestAsk = 30 * time.Minute

// refresh asks the CLI behind what is served, once at a time; the channel is
// closed when it has answered. Called with c locked.
func (c *cliIdentity) refresh() chan struct{} {
	if c.refreshing {
		return c.done
	}
	c.refreshing, c.done = true, make(chan struct{})
	gen, done := c.gen, c.done
	go func() {
		start := time.Now()
		u, p, ok, err := c.ask()
		slow := time.Since(start) >= slowAsk
		c.Lock()
		defer c.Unlock()
		defer close(done)
		if gen != c.gen {
			return
		}
		c.at, c.refreshing = time.Now(), false
		// asked again in a minute, then two, four… up to slowestAsk while
		// it fails or is slow
		if err != nil || slow {
			c.retry = min(max(2*c.retry, time.Minute), slowestAsk)
		} else {
			c.retry = 0
		}
		if err != nil {
			return // what was served stays
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
	c.retry = 0
	c.Unlock()
}
