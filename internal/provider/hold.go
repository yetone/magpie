package provider

import (
	"hash/maphash"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// The catalog is rebuilt from every provider's files on each Resolve (tens
// of milliseconds with a few hundred models), and a page of the GUI
// resolves a model for each row: /api/state and /api/groups took 4s and
// 7s with 24 providers. A request that only reads holds the catalog it
// built first for all its look-ups.
var held struct {
	sync.Mutex
	holds int
	gen   uint64
	// what was built, and when, by what it is
	built map[string]heldValue
	// env is the environment it was built in (environSum)
	env uint64
	// fgen is filememo's generation it was built in: a write that only
	// tells filememo (settings.Save, an account's file) has it built again
	fgen uint64
	// roots is the providers' and the settings' files as last looked at
	roots [2]rootStamp
	// stamp is StampNow's files as a request holding the catalog last
	// looked at them, at stampAt (stampNowFiles)
	stamp   [3]rootStamp
	stampAt time.Time
}

// rootStamp is a file's time and size, or that it wasn't there.
type rootStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

func stampOf(path string) rootStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return rootStamp{}
	}
	return rootStamp{fi.ModTime(), fi.Size(), true}
}

// heldFreshLocked drops what is built when a file was written since, by
// this process (filememo.Gen) or, for the providers' and the settings'
// files, by another (the CLI's "magpie group add", seen by the next
// request, not a second later). Called with held held.
func heldFreshLocked(look bool) {
	if g := filememo.Gen(); g != held.fgen {
		held.built, held.fgen = nil, g
	}
	if !look {
		return
	}
	for i, path := range [2]string{Path(), settings.Path()} {
		if st := stampOf(path); st != held.roots[i] {
			held.roots[i] = st
			held.built = nil
			filememo.Look(path)
		}
	}
}

var environSeed = maphash.MakeSeed()

// environSum is a hash of this process's environment: the folders every
// file and account is read from are named by it.
func environSum() uint64 {
	var h maphash.Hash
	h.SetSeed(environSeed)
	for _, kv := range os.Environ() {
		h.WriteString(kv)
		h.WriteByte(0)
	}
	return h.Sum64()
}

type heldValue struct {
	v  any
	at time.Time
}

func init() {
	catalog.Forget = Changed
	// a plugin's sign-in saved or renewed, a plugin added, and the models
	// a plugin lists once the host has answered: the catalog may be
	// another, so the model lists agents keep in their own files follow,
	// as for a provider saved (catalog.Changed). Dropping only what is
	// held left Pi's models.json without a plugin's models signed in to
	// after it was written (#2373).
	plugin.OnChange(catalog.Touched)
	plugin.OnListed(catalog.Touched)
	// and what a request holds goes at once: a SyncCatalog running holds
	// the plugins as they were before a sign-in, and a look at the
	// accounts right after it was given those for a second (#2449)
	plugin.OnChangeNow(dropHeld)
}

// heldFor is how long a held catalog is used before it is built again,
// while one request after another keeps it held.
var heldFor = time.Second

// Hold keeps the catalog built once until release is called, for a
// request that only reads; a write meanwhile (Changed) drops it.
func Hold() (release func()) {
	held.Lock()
	if held.holds == 0 {
		// what was built under another environment (a HOME or config
		// folder of its own: a test's) isn't this one's
		if env := environSum(); env != held.env {
			held.built, held.env = nil, env
		}
		heldFreshLocked(true)
		// a request's first StampNow looks at the files again
		held.stampAt = time.Time{}
	}
	held.holds++
	held.Unlock()
	return sync.OnceFunc(func() {
		held.Lock()
		if held.holds--; held.holds == 0 {
			// what is built stays for the next request within heldFor, as
			// it stays for one that overlaps this one: one request after
			// another, each built the catalog again from every provider's
			// files and accounts (a fifth of a request's CPU with 30
			// providers). Only what is past heldFor goes, for memory.
			for k, h := range held.built {
				if time.Since(h.at) >= heldFor {
					delete(held.built, k)
				}
			}
		}
		held.Unlock()
	})
}

// Changed drops what is held: a provider, an account, a plugin or a
// setting was written.
func Changed() {
	filememo.Forget()
	dropHeld()
}

// dropHeld is Changed for what is built on the plugins alone, which
// read their files afresh each time.
func dropHeld() {
	held.Lock()
	held.gen++
	held.built = nil
	held.Unlock()
}

// Stamp marks what the catalog is built from, read without building it.
// Two equal stamps mean nothing magpie wrote or was told of came between
// them: no Changed (a provider, an account, a setting saved), no plugin's
// change, no catalog.Reset or Touched, no filememo.Forget, the same
// environment, and the providers', the settings' and the catalog's own
// files with the same time and size, as another process leaves them.
// What it can't see — another app rewriting an account's file, a model
// retired by its date — a caller that keeps what it built across stamps
// builds again now and then all the same.
type Stamp struct {
	gen, fgen, env uint64
	roots          [3]rootStamp
}

// StampNow is the Stamp as things are now.
func StampNow() Stamp {
	held.Lock()
	gen := held.gen
	held.Unlock()
	return Stamp{gen: gen, fgen: filememo.Gen(), env: environSum(), roots: stampNowFiles()}
}

// stampLooks counts StampNow's looks at its files, for a test.
var stampLooks atomic.Int64

// stampNowFiles is the files' part of StampNow: looked at once by a
// request holding the catalog and its overlapping ones, for heldFor at
// most, as what is held is. KeptListing takes a stamp for every
// provider, and its three stats a provider were most of what a
// Providers page poll spent (~600 of them a poll with 200 providers).
// Another process's write is seen by the next request, as Hold's look
// sees it.
func stampNowFiles() [3]rootStamp {
	held.Lock()
	roots, fresh := held.stamp, held.holds > 0 && !held.stampAt.IsZero() && time.Since(held.stampAt) < heldFor
	held.Unlock()
	if fresh {
		return roots
	}
	stampLooks.Add(1)
	roots = [3]rootStamp{stampOf(Path()), stampOf(settings.Path()), stampOf(catalog.CachePath())}
	held.Lock()
	if held.holds > 0 {
		held.stamp, held.stampAt = roots, time.Now()
	}
	held.Unlock()
	return roots
}

// Held is what build gives, built once while a request holds it (Hold),
// for what each row of a page looks up: shared by them, so read only.
func Held[T any](key string, build func() T) T { return heldOf(key, build) }

// heldCall is a build in flight, for the callers that miss it together.
type heldCall struct {
	done chan struct{}
	// v is what it built, and fresh says nothing was written meanwhile
	// (Changed, a plugin's change, filememo.Forget, another environment):
	// what a caller waiting on it would have built itself. Both are set
	// before done is closed.
	v     any
	fresh bool
}

// heldFlight is the builds in flight by key, held's lock guarding it. A
// write drops what is held, and every request then in flight missed
// together: each built again, one after another, each taking
// logins.json's lock when the one before it let go, 20 requests queued
// on it and the routing plan waiting past its 30s (#5104).
var heldFlight = map[string]*heldCall{}

// heldWait is how long a caller waits on another's build before it
// builds on its own, as each did before (#5104). A build waits on
// nothing of its waiters' today, but one that came to (a build asking,
// deep down, for what waits on it, or for a lock its waiter holds) is
// then a pause, not a request held for good.
var heldWait = 5 * time.Second

// heldRounds is how many builds in a row a caller waits on before it
// builds on its own: each one a write made stale, so that writes coming
// one after another can't keep a request waiting.
const heldRounds = 3

// heldOf is what build gives, built once while a request holds it.
// Callers that miss together build once: the first builds, the rest wait
// for it and take what it built, unless a write came while it was built;
// then one of them builds again (#5104). Outside a request every caller
// builds, as before.
func heldOf[T any](key string, build func() T) T {
	held.Lock()
	if held.holds == 0 {
		held.Unlock()
		return build()
	}
	for round := 0; ; round++ {
		heldFreshLocked(false)
		if h, ok := held.built[key]; ok && time.Since(h.at) < heldFor {
			held.Unlock()
			return h.v.(T)
		}
		c := heldFlight[key]
		if c == nil {
			break
		}
		held.Unlock()
		if round == heldRounds || !c.wait(heldWait) {
			return build()
		}
		if c.fresh {
			return c.v.(T)
		}
		held.Lock()
		if held.holds == 0 {
			held.Unlock()
			return build()
		}
	}
	c := &heldCall{done: make(chan struct{})}
	heldFlight[key] = c
	gen, fgen, env := held.gen, filememo.Gen(), held.env
	held.Unlock()
	// the build ends for its waiters however it ends: one that panics
	// leaves them to build on their own (fresh is false)
	defer func() {
		held.Lock()
		if heldFlight[key] == c {
			delete(heldFlight, key)
		}
		held.Unlock()
		close(c.done)
	}()
	v := build()
	held.Lock()
	// a filememo-only write (settings.Save) meanwhile is a write too: kept,
	// what was read before it would be given for a second after it
	c.v, c.fresh = v, held.gen == gen && filememo.Gen() == fgen && held.env == env
	if held.holds > 0 && c.fresh {
		if held.built == nil {
			held.built = map[string]heldValue{}
		}
		held.built[key] = heldValue{v, time.Now()}
	}
	delete(heldFlight, key)
	held.Unlock()
	return v
}

// wait waits for the build to end, for d at most, and says whether it
// ended.
func (c *heldCall) wait(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-c.done:
		return true
	case <-t.C:
		return false
	}
}

func heldEntries(build func() []Entry) []Entry {
	// clipped: a caller's append copies
	return heldOf("entries", func() []Entry {
		return keptOf("entries", func() []Entry {
			entriesBuilt.Add(1)
			return slices.Clip(build())
		})
	})
}

// entriesBuilt counts the catalogs built through heldEntries, for a test
// that counts them.
var entriesBuilt atomic.Int64

// keptFor is how long what kept has is used while the Stamp stays the
// same: an account another program signed in to or out of, or a model
// retired by its date, moves no stamp, and is seen this long after.
var keptFor = time.Minute

// kept is what a request built from every provider's models (the catalog,
// the switched-off providers' models, the groups and their entries), for
// the requests after it while StampNow stays the same and keptFor hasn't
// gone by. Held for heldFor alone, an agent's requests a few seconds apart
// each built them all again from every provider's models and accounts
// (~725 MB of allocations in 300s of a gateway in use). What it gives is
// shared, as what heldOf gives: only for reading. Two requests building
// one at once each build it; none waits on another.
var kept struct {
	sync.Mutex
	stamp Stamp
	at    time.Time
	of    map[string]any
}

// keptOf is what build gives, kept across requests (kept) while one holds
// the catalog, and built afresh outside one.
func keptOf[T any](key string, build func() T) T {
	return keptFit(key, func(any) bool { return true }, build)
}

// keptFit is keptOf, taking what is kept only where fits says it is
// still the one wanted.
func keptFit[T any](key string, fits func(any) bool, build func() T) T {
	if !holdingNow() {
		return build()
	}
	st := StampNow()
	kept.Lock()
	if kept.stamp != st || time.Since(kept.at) >= keptFor {
		kept.stamp, kept.at, kept.of = st, time.Now(), nil
	}
	if v, ok := kept.of[key]; ok && fits(v) {
		kept.Unlock()
		return v.(T)
	}
	kept.Unlock()
	// built from what is there after st was taken: never older than st,
	// and a write meanwhile moves the stamp the next look takes
	v := build()
	kept.Lock()
	if kept.stamp == st {
		if kept.of == nil {
			kept.of = map[string]any{}
		}
		kept.of[key] = v
	}
	kept.Unlock()
	return v
}

// holdingNow is whether a request holds the catalog now.
func holdingNow() bool {
	held.Lock()
	defer held.Unlock()
	return held.holds > 0
}

// heldPlugins is plugin.Cached, which reads the plugins' list and
// sign-ins each time, and PluginOf asks for every model.
func heldPlugins() []plugin.Provider {
	return heldOf("plugins", func() []plugin.Provider {
		pluginsBuilt.Add(1)
		return slices.Clip(plugin.Cached())
	})
}

// pluginsBuilt counts the plugins' list built, for the test that a look at
// the accounts builds it once.
var pluginsBuilt atomic.Int64

// fromEntries is what was built from a catalog, and which catalog: its
// first entry and its length.
type fromEntries[T any] struct {
	at *Entry
	n  int
	v  T
}

// heldFrom is heldOf for what is built from entries: held while a request
// holds the catalog, and built again for another catalog than it was built
// from (providerEntries built afresh after heldFor, or one a caller made).
// A request asks for the groups' entries several times (Catalog, Served),
// and each built them from every member of every group again (the CPU a
// request to a group took at once, with many providers).
func heldFrom[T any](key string, entries []Entry, build func([]Entry) T) T {
	var at *Entry
	if len(entries) > 0 {
		at = &entries[0]
	}
	held.Lock()
	if held.holds == 0 {
		held.Unlock()
		return build(entries)
	}
	heldFreshLocked(false)
	if h, ok := held.built[key]; ok && time.Since(h.at) < heldFor {
		if f := h.v.(fromEntries[T]); f.at == at && f.n == len(entries) {
			held.Unlock()
			return f.v
		}
	}
	gen := held.gen
	held.Unlock()
	f := keptFit(key, func(v any) bool {
		f, ok := v.(fromEntries[T])
		return ok && f.at == at && f.n == len(entries)
	}, func() fromEntries[T] { return fromEntries[T]{at, len(entries), build(entries)} })
	v := f.v
	held.Lock()
	if held.holds > 0 && held.gen == gen {
		if held.built == nil {
			held.built = map[string]heldValue{}
		}
		held.built[key] = heldValue{f, time.Now()}
	}
	held.Unlock()
	return v
}

// heldSettings is the settings, read once while a request holds the
// catalog: a look at the agents asks them for every model of every agent.
// Only for reading — what it gives is shared by the request's look-ups.
func heldSettings() settings.Settings { return heldOf("settings", settings.Load) }

// HeldSettings is the settings as heldSettings reads them, for a look
// outside the package made many times in one request.
func HeldSettings() settings.Settings { return heldSettings() }
