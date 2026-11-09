// Package pluginrt runs magpie's built-in plugins: the parts of magpie not
// everyone needs (Lxxyx on X: "功能太多了"), each one a package under
// plugins/ that registers a Manifest. The runtime applies the ones the user
// has on (plugins.yml, see config.go) in the order their Requires and
// Provides make, and disposes of what each registered, in reverse, when it
// is turned off. One that fails or panics is FAILED and the rest run on.
//
// The runtime is core: it never imports a plugin, and a plugin never
// imports another (TestPluginsImportOnlyCore). Plugins reach each other
// only through the services they Provide.
package pluginrt

import (
	"fmt"
	"log"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// API is the host API plugins are written against. A plugin's
// Manifest.API is a caret range ("^1", "^1.2"): the same major, and a
// minor no newer than this one's. One that doesn't match isn't applied.
const API = "1.0"

// Core are the services magpie itself provides, which a plugin may
// Require without another plugin providing them.
var Core = []string{"settings", "providers", "gateway", "window"}

// Manifest is a built-in plugin: what it is, what it needs and gives, and
// Apply, which registers its parts through the Ctx.
type Manifest struct {
	ID  string
	API string // the host API range it is written for, "^1"
	// Requires are services it can't run without; Provides those it gives
	// others ("router.strategy/groups")
	Requires, Provides []string
	// Essential plugins are on in the Essentials preset, when the user
	// picks it; every built-in is on otherwise, a new install's too
	Essential bool
	// Name and Sub say what it is, in English; the window translates them
	Name, Sub string
	// One and Many say what of it is in use, of 1 or of {n}
	One, Many string
	// InUse counts what the user has made with it (groups, warm-ups, items
	// given to agents). One in use stays on whatever plugins.yml says: off,
	// its things would go on working with nowhere to see or change them.
	// An error counts as in use: unknown is never unused.
	InUse func() (int, error)
	// Apply registers the plugin's parts; nil is a plugin of parts only
	// (Contribute) that does nothing else
	Apply func(*Ctx) error
	// Parts are the window's parts it is, by slot: "nav.page" sessions,
	// "settings.section" otel, "part" groups (whatever the page marks
	// data-feature="<id>"). The runtime contributes them before Apply.
	Parts []Part
	// SetAt is where Settings has its settings, when it has any there: the
	// tab and the row they start at, "privacy #redactList". Its row in
	// Plugins › Built-in goes there, and there an off plugin says so
	// (netube99 on X: masking's switch, settings and page were three
	// places, none naming the others).
	SetAt string
	// Opens is the window page it is used on when it has no page of its
	// own ("providers" for routing strategies, used in a provider's
	// editor): its row in Plugins › Built-in leads there
	Opens string
	// Defaults is its config before plugins.yml's preset and rows
	Defaults map[string]any
	// Settings is a struct of its settings, their defaults its values
	// (schema.go): the window draws their form, and SetConfig takes only
	// what it allows. Register adds them to Defaults.
	Settings any
	// Client are its files in the window, under assets/plugins/<id>/: its
	// scripts register its parts with the window's kernel (kernel.js), its
	// i18n.js says its words in each language, and a .css is its styles.
	// TestBuiltinClientsLoaded holds the page to loading each.
	Client []string
	schema *Schema
}

// Schema is the schema of the plugin's Settings, nil for one with none.
func (m Manifest) Schema() *Schema { return m.schema }

// Part is one thing a plugin puts in the window: a Slot and a Key in it.
type Part struct{ Slot, Key string }

// State is where a plugin is in its life.
type State string

const (
	Off      State = "off"      // turned off in plugins.yml
	Pending  State = "pending"  // on, waiting for a service no active plugin provides
	Active   State = "active"   // applied
	Failed   State = "failed"   // Apply returned an error or panicked
	Disposed State = "disposed" // turned off at run time, its parts gone
)

// Ctx is what a plugin's Apply is handed. Everything registered through it
// is undone when the plugin is turned off, in reverse.
type Ctx struct {
	ID       string
	Config   map[string]any // the composed config, read only
	rt       *Runtime
	disposes []func()
	parts    []Part
	provided []string
}

// OnDispose runs f when the plugin is turned off or magpie stops.
func (c *Ctx) OnDispose(f func()) { c.disposes = append(c.disposes, f) }

// Contribute puts a part in the window while the plugin is active.
func (c *Ctx) Contribute(slot, key string) {
	c.parts = append(c.parts, Part{slot, key})
}

// Provide gives a service other plugins may Require. It is withdrawn when
// the plugin is turned off.
func (c *Ctx) Provide(name string, v any) {
	c.rt.services[name] = v
	c.provided = append(c.provided, name)
	c.OnDispose(func() { delete(c.rt.services, name) })
}

// Service is a service a plugin Provides, or nil.
func (c *Ctx) Service(name string) any { return c.rt.services[name] }

var (
	regMu    sync.Mutex
	registry = map[string]Manifest{}
)

// Register adds a built-in plugin; plugins call it from init. Two with one
// id is a programming error.
func Register(m Manifest) {
	regMu.Lock()
	defer regMu.Unlock()
	if m.ID == "" {
		panic("pluginrt: a plugin with no id")
	}
	if _, dup := registry[m.ID]; dup {
		panic("pluginrt: two plugins " + m.ID)
	}
	if m.Settings != nil {
		var defs map[string]any
		m.schema, defs = SchemaOf(m.Settings)
		d := maps.Clone(m.Defaults)
		if d == nil {
			d = map[string]any{}
		}
		for k, v := range defs {
			if _, set := d[k]; !set {
				d[k] = v
			}
		}
		m.Defaults = d
	}
	registry[m.ID] = m
}

// Registered are the built-in plugins, by id.
func Registered() []Manifest {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]Manifest, 0, len(registry))
	for _, m := range registry {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Status is one plugin as it runs now.
type Status struct {
	ID    string `json:"id"`
	State State  `json:"state"`
	// Why says what kept it from Active: the service it waits for, the
	// error it failed with, the API it wants
	Why   string `json:"why,omitempty"`
	Parts []Part `json:"parts,omitempty"`
}

type running struct {
	m      Manifest
	state  State
	why    string
	ctx    *Ctx
	wanted bool
}

// Runtime applies the plugins that are on and keeps what each registered.
type Runtime struct {
	mu       sync.Mutex
	plugins  map[string]*running
	order    []string // applied order, for disposing in reverse
	services map[string]any
	// reconciled is set once Reconcile has run
	reconciled bool
}

// New is a runtime of the registered plugins, none applied yet.
func New(ms []Manifest) *Runtime {
	rt := &Runtime{plugins: map[string]*running{}, services: map[string]any{}}
	for _, s := range Core {
		rt.services[s] = struct{}{}
	}
	for _, m := range ms {
		rt.plugins[m.ID] = &running{m: m, state: Off}
	}
	return rt
}

// Reconcile brings the runtime to on: each plugin wanted and not active is
// applied, once what it requires is provided; each active one no longer
// wanted is disposed, with whatever requires a service it provided. on
// says, by id, whether a plugin is wanted; config is each one's composed
// config.
func (rt *Runtime) Reconcile(on map[string]bool, config map[string]map[string]any) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.reconciled = true
	for id, p := range rt.plugins {
		p.wanted = on[id]
	}
	// off first: each one no longer wanted, and each that requires a
	// service one of those provided, newest first, so a service is
	// withdrawn after its users
	going := map[string]bool{}
	for {
		more := false
		for _, id := range rt.order {
			p := rt.plugins[id]
			if going[id] {
				continue
			}
			if !p.wanted || rt.needsGoing(p, going) {
				going[id], more = true, true
			}
		}
		if !more {
			break
		}
	}
	for i := len(rt.order) - 1; i >= 0; i-- {
		if id := rt.order[i]; going[id] {
			rt.dispose(rt.plugins[id])
		}
	}
	// then on, as long as one more can be applied
	ids := make([]string, 0, len(rt.plugins))
	for id := range rt.plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for {
		applied := false
		for _, id := range ids {
			p := rt.plugins[id]
			if !p.wanted || p.state == Active || p.state == Failed {
				continue
			}
			if !Compatible(p.m.API) {
				p.state, p.why = Failed, fmt.Sprintf("written for host API %s, this is %s", p.m.API, API)
				continue
			}
			if !rt.satisfied(p.m) {
				p.state, p.why = Pending, "waiting for "+strings.Join(rt.missing(p.m), ", ")
				continue
			}
			rt.apply(p, config[id])
			applied = applied || p.state == Active
		}
		if !applied {
			break
		}
	}
	for _, p := range rt.plugins {
		if !p.wanted && p.state != Disposed {
			p.state, p.why = Off, ""
		}
		if p.state == Pending {
			log.Printf("plugins: %s is on but %s", p.m.ID, p.why)
		}
	}
}

// needsGoing is whether p requires a service a plugin in going provides.
func (rt *Runtime) needsGoing(p *running, going map[string]bool) bool {
	for id := range going {
		if q := rt.plugins[id]; q.ctx != nil {
			for _, s := range q.ctx.provided {
				if slices.Contains(p.m.Requires, s) {
					return true
				}
			}
		}
	}
	return false
}

func (rt *Runtime) missing(m Manifest) []string {
	var out []string
	for _, s := range m.Requires {
		if _, ok := rt.services[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

func (rt *Runtime) satisfied(m Manifest) bool { return len(rt.missing(m)) == 0 }

// apply runs a plugin's Apply, called with rt.mu held. A panic is a
// failure of that plugin alone; what it registered before it is undone.
func (rt *Runtime) apply(p *running, config map[string]any) {
	ctx := &Ctx{ID: p.m.ID, Config: config, rt: rt}
	ctx.parts = append(ctx.parts, p.m.Parts...)
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		if p.m.Apply != nil {
			return p.m.Apply(ctx)
		}
		return nil
	}()
	if err != nil {
		undo(ctx)
		p.state, p.why, p.ctx = Failed, err.Error(), nil
		log.Printf("plugins: %s failed: %v", p.m.ID, err)
		return
	}
	p.state, p.why, p.ctx = Active, "", ctx
	rt.order = append(rt.order, p.m.ID)
}

// dispose undoes what a plugin registered, newest first, called with
// rt.mu held.
func (rt *Runtime) dispose(p *running) {
	if p.ctx != nil {
		undo(p.ctx)
	}
	p.ctx, p.state, p.why = nil, Disposed, ""
	rt.order = slices.DeleteFunc(rt.order, func(id string) bool { return id == p.m.ID })
}

func undo(ctx *Ctx) {
	for i := len(ctx.disposes) - 1; i >= 0; i-- {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("plugins: %s: disposing panicked: %v", ctx.ID, r)
				}
			}()
			ctx.disposes[i]()
		}()
	}
	ctx.disposes = nil
}

// Stop disposes of every active plugin, newest first.
func (rt *Runtime) Stop() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for len(rt.order) > 0 {
		rt.dispose(rt.plugins[rt.order[len(rt.order)-1]])
	}
}

// Statuses are the plugins as they run now, by id.
func (rt *Runtime) Statuses() []Status {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]Status, 0, len(rt.plugins))
	for id, p := range rt.plugins {
		s := Status{ID: id, State: p.state, Why: p.why}
		if p.ctx != nil {
			s.Parts = slices.Clone(p.ctx.parts)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Active is whether the plugin id is applied now.
func (rt *Runtime) Active(id string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.plugins[id]
	return p != nil && p.state == Active
}

// Compatible is whether a plugin written for the caret range want runs on
// this host's API. An empty range is "^1".
func Compatible(want string) bool {
	if want == "" {
		want = "^1"
	}
	want, ok := strings.CutPrefix(want, "^")
	if !ok {
		return false
	}
	wMaj, wMin, ok1 := version(want)
	hMaj, hMin, ok2 := version(API)
	return ok1 && ok2 && wMaj == hMaj && wMin <= hMin
}

func version(v string) (int, int, bool) {
	maj, min, _ := strings.Cut(v, ".")
	a, err := strconv.Atoi(maj)
	if err != nil {
		return 0, 0, false
	}
	if min == "" {
		return a, 0, true
	}
	min, _, _ = strings.Cut(min, ".")
	b, err := strconv.Atoi(min)
	return a, b, err == nil
}
