package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// An agent's config is a file anyone can write: another switcher, an
// installer, the agent's own setup. One that takes magpie out leaves the
// row showing a magpie model while the agent asks its own vendor for it, and
// the user blames magpie. So magpie remembers what it last set on each agent
// (applied.json, beside the stash) and says when that no longer holds —
// Drift — with the way to set it again.

var appliedMu sync.Mutex

// applied is what magpie last set on one agent, and when.
type applied struct {
	At     time.Time         `json:"at"`
	Fields map[string]string `json:"fields"` // field key → value
}

func appliedPath() string { return filepath.Join(filepath.Dir(provider.Path()), "applied.json") }

// appliedLoad is agent id → what magpie set on it.
func appliedLoad() map[string]applied {
	out := map[string]applied{}
	if b, err := os.ReadFile(appliedPath()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func appliedSave(m map[string]applied) {
	b, _ := json.MarshalIndent(m, "", "  ")
	os.MkdirAll(filepath.Dir(appliedPath()), 0o755)
	os.WriteFile(appliedPath(), b, 0o600)
}

func appliedOf(id string) applied {
	appliedMu.Lock()
	defer appliedMu.Unlock()
	return appliedLoad()[id]
}

// record keeps what a field reads after magpie set it; a field put back to
// the agent's default is magpie's no longer.
func record(id, key, v string) {
	appliedMu.Lock()
	defer appliedMu.Unlock()
	m := appliedLoad()
	a := m[id]
	if a.Fields == nil {
		a.Fields = map[string]string{}
	}
	if v == "" {
		delete(a.Fields, key)
	} else {
		a.Fields[key] = v
	}
	a.At = time.Now()
	m[id] = a
	appliedSave(m)
}

// Apply sets one of the agent's fields and remembers it as magpie's, so it
// can be told apart from what something else writes there later. Every
// setting of a field by the user goes through here. A field magpie set
// before that this one moves too is magpie's doing as well: Claude Code's
// Default takes its tiers and subagent model out with magpie's endpoint,
// which read as changed outside magpie, and the drift kept the row up
// among the connected ones while it said Not connected (#834).
func (a *Agent) Apply(key, v string) error {
	if a.Native != nil {
		return a.Native.Apply(key, v)
	}
	f := a.Field(key)
	if f == nil {
		return nil
	}
	was := a.Values()
	if err := f.Set(v); err != nil {
		return err
	}
	now := a.Values()
	for k := range appliedOf(a.ID).Fields {
		if k != f.Key && now[k] != was[k] {
			record(a.ID, k, now[k])
		}
	}
	record(a.ID, f.Key, f.Get())
	return nil
}

// Drift is how an agent differs from what magpie set on it.
type Drift struct {
	// Kind says what is off:
	//   "unwired"  the model is magpie's but the config no longer sends it
	//              through magpie (Check);
	//   "replaced" a magpie model magpie set was replaced by the agent's own;
	//   "bypassed" the config is right, yet the agent was used since and
	//              nothing of it reached the gateway — it runs on an old
	//              config, or something outside the file overrides it;
	//   "unreachable" the config is right, but the address off loopback
	//              it names the gateway at doesn't answer (#1013).
	Kind   string `json:"kind"`
	Field  string `json:"field"`         // the field it shows on
	Now    string `json:"now,omitempty"` // what that field says now
	Want   string `json:"want"`          // what setting it again sets
	Detail string `json:"detail"`        // what exactly is off, for a tooltip
	// Addr is where an unreachable agent is pointed; Move, when set, the
	// address WSL reaches Windows at now, which Reapply points it at.
	Addr string `json:"addr,omitempty"`
	Move string `json:"move,omitempty"`
}

// started is when this process — and the gateway in it — came up: before
// then, a request the gateway missed says nothing about the agent.
var started = time.Now()

// Drift says what, if anything, keeps the agent off what magpie set: its
// config first (wiring, then each field against magpie's record), then —
// the config being right — whether its latest use actually came through.
// A field moved from one magpie model to another (the agent's own picker)
// isn't drift; one moved off magpie is.
func (a *Agent) Drift() *Drift {
	if a.Native != nil {
		s := a.Native.Read()
		if s.Provider == "invalid" {
			return &Drift{Kind: "unwired", Field: "model", Detail: s.Detail}
		}
		return nil
	}
	if len(a.Fields) == 0 || a.theInstalled() {
		return nil
	}
	vals := a.Values()
	// the field on one of magpie's models: where drift shows, and what
	// setting it again sets
	on, onMagpie := a.Fields[0], false
	for _, f := range a.Fields {
		if magpieValue(a, f, vals[f.Key], vals) {
			on, onMagpie = f, true
			break
		}
	}
	if a.Check != nil {
		if d := a.Check(); d != "" {
			return &Drift{Kind: "unwired", Field: on.Key, Now: vals[on.Key], Want: vals[on.Key], Detail: d}
		}
	}
	rec := appliedOf(a.ID)
	// still joined, a model off magpie is one picked in the agent, beside
	// magpie's in its list (dsh's /model), not a change from outside
	joined := a.Joined != nil && a.Joined()
	for _, f := range a.Fields {
		want, ok := rec.Fields[f.Key]
		// a field that reads empty while it follows another (Claude Code's
		// tiers and subagents on its main model) runs on that one's model:
		// the main model moved onto the one magpie set the field to reads
		// as following it, not as the field put back to the agent's own
		// default (#1050)
		now := vals[f.Key]
		if now == "" && f.Follows != "" {
			now = vals[f.Follows]
		}
		if !ok || joined || now == want || !magpieValue(a, f, want, vals) || magpieValue(a, f, now, vals) || sameGroup(want, now) {
			continue
		}
		return &Drift{Kind: "replaced", Field: f.Key, Now: vals[f.Key], Want: want,
			Detail: a.Name + "'s config was changed outside magpie: " + f.Label + " is " + orDefault(vals[f.Key]) + ", not " + want + " as magpie set it"}
	}
	if onMagpie || joined {
		if d := a.unreachable(on, vals[on.Key]); d != nil {
			return d
		}
	}
	if a.Reached != nil && onMagpie {
		if at, to, refused := a.Reached(rec.At); !at.IsZero() {
			switch {
			case !sameHost(to, gateway.URL()):
				return &Drift{Kind: "bypassed", Field: on.Key, Now: vals[on.Key], Want: vals[on.Key],
					Detail: a.Name + "'s last request (" + at.Format("15:04") + ") went to " + hostOf(to) + ", not magpie — it was started before magpie set it up and still runs on its old config: quit and reopen it"}
			case refused:
				return &Drift{Kind: "bypassed", Field: on.Key, Now: vals[on.Key], Want: vals[on.Key],
					Detail: a.Name + " couldn't reach magpie at " + at.Format("15:04") + " — magpie wasn't running then, so it has none of magpie's models: quit and reopen it"}
			}
		}
	}
	if a.LastUsed != nil && onMagpie {
		if used := a.LastUsed(); bypassed(used, rec.At, lastSeen(a.ID)) {
			return &Drift{Kind: "bypassed", Field: on.Key, Now: vals[on.Key], Want: vals[on.Key],
				Detail: a.Name + " was used at " + used.Format("15:04") + " but none of its requests reached magpie — one started before magpie set it up still runs on its old config: restart it"}
		}
	}
	return nil
}

// installedURL is the gateway of the magpie people install, on its own
// port, the one its Settings have; a magpie MAGPIE_ADDR puts on another
// (magpie-dev, a sandbox) is run beside it.
var installedURL = gateway.SavedURL

// elsewhere: this magpie is not on the installed one's port.
func elsewhere() bool { return !sameHost(gateway.URL(), installedURL()) }

// theInstalled: this magpie runs beside the installed one, and the agent is
// wired to that one's gateway, not this one's — the installed magpie set it
// up, so it is that one's to check, not drift here. Otherwise the two would
// take turns flagging each other's wiring.
func (a *Agent) theInstalled() bool {
	if !elsewhere() || a.Path == "" {
		return false
	}
	b, err := edit.Read(a.Path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), hostOf(installedURL())) && !strings.Contains(string(b), hostOf(gateway.URL()))
}

// bypassed: the agent was used — while this gateway was up and after magpie
// last set it — and no request of it arrived since. A request leaves within
// moments of the prompt; a little grace keeps one in flight from counting.
// lastSeen is when the agent's last request reached this process's gateway
// (usage.LastSeen). What usage.Saw records lasts as long as the process, so
// a test that stands for a request gives its own here.
var lastSeen = usage.LastSeen

func bypassed(used, applied, seen time.Time) bool {
	const grace = 30 * time.Second
	return !used.IsZero() && used.After(started) && used.After(applied) &&
		time.Since(used) > grace && seen.Before(used.Add(-2*time.Second))
}

// magpieValue: the value is one of magpie's models as this agent spells it,
// its suffix after the model (SplitSuffix) aside; a list of models is the
// user's own.
func magpieValue(a *Agent, f Field, v string, vals map[string]string) bool {
	if v == "" {
		return false
	}
	v, _, one := a.split(v)
	if !one || a.Spelled != nil && !a.Spelled(v) {
		return false
	}
	if isMagpie(v) {
		return true
	}
	// spelled as magpie's provider in the agent's own config (OpenCode's
	// magpie/…): magpie's even when its list there doesn't have the model
	if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
		return true
	}
	if f.Options == nil {
		return false
	}
	for _, o := range f.Options(vals) {
		if o.Value == v {
			return o.Ref != ""
		}
	}
	return false
}

// groupNamed is the routing group ("group/<id>") a model value reaches at
// the gateway: a group's own id, or a model's id without a provider in it
// that the gateway takes as a group's (provider.GroupFor: gpt-6.1-sol is
// group/auto-gpt-6-1-sol); "" for any other.
func groupNamed(v string) string {
	// Claude Code's [1m] mark rides on the group's id as it does on a
	// model's, and one of the two values here comes from the agent's own
	// settings: without it off, a group read back marked is not the one
	// magpie set (GroupFor takes it off; so does GroupFinder)
	v = strings.TrimSuffix(strings.TrimSpace(v), "[1m]")
	v = strings.TrimPrefix(v, magpieID+"/")
	if strings.HasPrefix(v, provider.GroupPrefix) {
		return v
	}
	g, _ := provider.GroupFor(v)
	return g
}

// sameGroup: two values of a field reach the same routing group, one by
// the group's id and the other by its model's name (#750: Codex's config
// read gpt-6.1-sol where magpie had set group/auto-gpt-6-1-sol). Asked for
// either, the gateway serves the group, so the one is not the other
// replaced.
func sameGroup(a, b string) bool {
	g := groupNamed(a)
	return g != "" && g == groupNamed(b)
}

// setAsGroup: magpie set the agent's field to a routing group, and v names
// that group by its model's name (sameGroup) — magpie's model still, for an
// agent whose config sends v to magpie's gateway.
func setAsGroup(id, key, v string) bool {
	if v == "" || isMagpie(v) {
		return false
	}
	want, ok := appliedOf(id).Fields[key]
	return ok && sameGroup(want, v)
}

func orDefault(v string) string {
	if v == "" {
		return "the agent's default"
	}
	return v
}

// Reapply sets again what magpie set on the agent: what drifted, else its
// fields as they read now — for a config taken off magpie in a way no check
// catches. A replaced field brings back the others magpie set with it.
// Either way the record is renewed, so a use before now no longer counts.
func (a *Agent) Reapply() error {
	if a.Native != nil {
		return a.Native.Connect()
	}
	d := a.Drift()
	if d != nil && d.Kind == "unreachable" && d.Move != "" && a.move != nil {
		return a.move(d.Addr, d.Move)
	}
	if d != nil && d.Kind == "replaced" {
		rec := appliedOf(a.ID)
		// the model first: the others (an effort) are checked against it
		if err := a.Apply(d.Field, d.Want); err != nil {
			return err
		}
		for _, f := range a.Fields {
			if v, ok := rec.Fields[f.Key]; ok && f.Key != d.Field && f.Get() != v {
				if err := a.Apply(f.Key, v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	vals := a.Values()
	for _, f := range a.Fields {
		if v := vals[f.Key]; v != "" && (d != nil && f.Key == d.Field || magpieValue(a, f, v, vals)) {
			if err := a.Apply(f.Key, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Keep takes the agent's config as it is now: what magpie set before is
// forgotten, and no longer said to have been changed.
func (a *Agent) Keep() {
	appliedMu.Lock()
	defer appliedMu.Unlock()
	m := appliedLoad()
	if _, ok := m[a.ID]; !ok {
		return
	}
	delete(m, a.ID)
	appliedSave(m)
}

// Wired reports whether magpie is in the agent's config: a field on one of
// magpie's models, or on magpie itself (an app whose one setting is magpie
// as its provider).
func (a *Agent) Wired() bool {
	if a.Native != nil {
		return a.Native.Read().Provider == "connected"
	}
	if a.Joined != nil && a.Joined() || a.Beside != nil && a.Beside() {
		return true
	}
	vals := a.Values()
	for _, f := range a.Fields {
		if v := vals[f.Key]; v == magpieID || magpieValue(a, f, v, vals) || a.Routed != nil && setAsGroup(a.ID, f.Key, v) && a.Routed() {
			return true
		}
	}
	return false
}

// Pick sets a field from the Agents page's model picker. One of magpie's
// models picked on an agent not connected yet connects it first, as its
// switch does, then starts it on that model (the owner: the switch, and a
// model picked in magpie in one click, both): the agent gets magpie's whole
// list, and Disconnect puts back what it had before.
func (a *Agent) Pick(key, v string) error {
	if a.Native != nil {
		value, err := a.Spell(key, v)
		if err != nil {
			return err
		}
		return a.Apply(key, value)
	}
	if f := a.Field(key); f != nil && !a.Wired() {
		vals := a.Values()
		vals[key] = v
		if v == magpieID || magpieValue(a, *f, v, vals) {
			if _, err := a.connect(); err != nil {
				return err
			}
		}
	}
	return a.Apply(key, v)
}

// Connect puts magpie into the agent's config, the Agents page's switch
// (the owner: an agent is connected to magpie, and its models are picked in
// the agent): the agent's main field goes to one of magpie's models — the
// one serving the model it is on now, else the first — or to magpie itself
// for an app whose one setting is magpie as its provider. Setting it writes
// the provider and the whole catalog in, so the agent's own model list has
// every one of magpie's models. An agent that can keep the model it is on
// (Join) keeps it. An agent already connected is left as it is.
// NoModelsError is Connect's answer while magpie has no models to give
// the agent: no provider or subscription has been added yet. The GUI says
// it in the reader's language by its code (no_models).
type NoModelsError struct{ Agent string }

func (e *NoModelsError) Error() string {
	return "Add a provider or subscription in magpie first, then connect " + e.Agent
}

func (a *Agent) Connect() error {
	_, err := a.ConnectHow()
	return err
}

// Connection is how Connect chose the model an agent starts on, for the
// Agents page to say it (#726: Codex showed its last pick, Claude Code a
// Sonnet through magpie and Antigravity some model, with nothing saying
// why). How is one of:
//
//   - "kept": it was connected already, left as it was
//   - "again": the models it was on when it was switched off, put back
//   - "joined": the model it is on stays its own (Codex signed in with
//     ChatGPT), magpie's models join its list
//   - "magpie": an app whose one setting is magpie as its provider
//   - "same": the model it is on now, through magpie
//   - "alike": the model its alias (opus) stands for, through magpie
//   - "default": on its own default with no model set: the first of its
//     own models in its list (the newest, for Claude Code), through magpie
//   - "first": magpie has none of the model it is on: the first on the
//     account it is signed in to, else a subscription's, else the first
//
// Field and Value are the field set and what it was set to, where Connect
// picked one model ("" for kept, again and joined).
type Connection struct {
	How   string `json:"how"`
	Field string `json:"field,omitempty"`
	Value string `json:"value,omitempty"`
}

// ConnectHow is Connect, saying how the model was chosen.
func (a *Agent) ConnectHow() (Connection, error) {
	if a.Native != nil {
		return Connection{How: "joined"}, a.Native.Connect()
	}
	if len(a.Fields) == 0 || a.Wired() {
		return Connection{How: "kept"}, nil
	}
	return a.connect()
}

func (a *Agent) connect() (Connection, error) {
	// what it is on now, for Disconnect to put back what it can't
	// otherwise (Goose's own model)
	now := a.Values()
	b, _ := json.Marshal(now)
	stash(map[string]string{a.ID + ".connect.was": string(b)})
	// switched off and on again: the models it was on, as they were
	if ok, err := a.reconnect(now); ok || err != nil {
		return Connection{How: "again"}, err
	}
	// the model it is on stays, where the agent can have magpie's models
	// beside it (the owner: Codex keeps its own last pick)
	if a.Join != nil {
		if ok, err := a.Join(); ok || err != nil {
			return Connection{How: "joined"}, err
		}
	}
	// the field magpie is picked in: the one listing magpie's models (for
	// Gemini CLI its model, its first field being how it signs in), else one
	// whose value is magpie itself
	vals := a.Values()
	var f *Field
	var opts []Option
	for pass := 0; pass < 2 && f == nil; pass++ {
		for i := range a.Fields {
			if a.Fields[i].Options == nil {
				continue
			}
			list := a.Fields[i].Options(vals)
			for _, o := range list {
				if pass == 0 && o.Ref != "" || pass == 1 && o.Value == magpieID {
					f, opts = &a.Fields[i], list
					break
				}
			}
			if f != nil {
				break
			}
		}
	}
	if f == nil {
		// nothing to connect it to yet: no provider or subscription added,
		// which said only that it can't be connected (Tystem on Discord)
		if shown, hidden := provider.CatalogFor(a.ListsFor()); len(shown)+len(hidden) == 0 {
			return Connection{}, &NoModelsError{Agent: a.Name}
		}
		return Connection{}, fmt.Errorf("%s can't be connected to magpie", a.Name)
	}
	cur := connectWas(vals[f.Key], opts)
	if cur == "" && f.Key != a.Fields[0].Key {
		cur = connectWas(a.Values()[a.Fields[0].Key], nil)
	}
	onDefault := cur == ""
	if cur == "" {
		// on its default: the first of its own models
		for _, o := range opts {
			if o.Ref == "" && o.Group == a.Name && o.Value != magpieID {
				cur = connectWas(o.Value, opts)
				break
			}
		}
	}
	// the model it is on now, through magpie (or of its family, for an
	// alias): on the account it is signed in to (its vendor's) first, then
	// on a subscription, before a key's; else its own vendor's; else a
	// subscription's; else the first
	var pick, same, alike, own, sub, group string
	sameRank, alikeRank := -1, -1
	for _, o := range opts {
		if o.Value == magpieID {
			return Connection{How: "magpie", Field: f.Key, Value: magpieID}, a.Apply(f.Key, magpieID)
		}
		if o.Ref != "" && o.Group == RoutingGroups && group == "" {
			group = o.Value
		}
		if o.Ref == "" || o.Group == RoutingGroups {
			continue
		}
		rank := 0
		if o.own || o.Group == a.Name {
			rank = 2
		} else if o.sub {
			rank = 1
		}
		if pick == "" {
			pick = o.Value
		}
		id := o.Ref[strings.LastIndex(o.Ref, "/")+1:]
		if cur != "" && (id == cur || strings.TrimSuffix(id, "[1m]") == cur) && rank > sameRank {
			same, sameRank = o.Value, rank
		}
		// an alias (opus) no label read: the model of that family
		if cur != "" && !strings.Contains(cur, "-") && strings.Contains(id+"-", "-"+cur+"-") && rank > alikeRank {
			alike, alikeRank = o.Value, rank
		}
		if own == "" && (o.own || o.Group == a.Name) {
			own = o.Value
		}
		if sub == "" && o.sub {
			sub = o.Value
		}
	}
	// the model it is on, of its own, as magpie serves it on a sign-in of
	// the user's, which its field lists as its own (Codex's on a ChatGPT
	// account it can't join, #940)
	if same == "" && alike == "" && cur != "" && a.OwnVia != nil {
		same = a.OwnVia(cur)
	}
	how := "first"
	switch {
	case same != "":
		pick, how = same, "same"
		if onDefault {
			how = "default"
		}
	case alike != "":
		pick, how = alike, "alike"
	case own != "":
		pick = own
	case sub != "":
		pick = sub
	case pick == "" && group != "":
		// only routing groups are shown it (its models hidden, or its own
		// account's alone beside them): the first group (#939)
		pick = group
	}
	if pick == "" {
		return Connection{}, fmt.Errorf("magpie has no models %s can use: add a subscription or a provider first", a.Name)
	}
	return Connection{How: how, Field: f.Key, Value: pick}, a.Apply(f.Key, pick)
}

// connectWas is the model an agent is on before it is connected, as a
// model id: its own option's alias read through the label that names the
// model ("opus · claude-opus-5-5"), a provider's prefix taken off.
func connectWas(v string, opts []Option) string {
	for _, o := range opts {
		if o.Value == v && o.Ref == "" {
			if _, id, ok := strings.Cut(o.Label, " · "); ok {
				v = strings.TrimSpace(id)
			}
			break
		}
	}
	if i := strings.LastIndex(v, "/"); i >= 0 {
		v = v[i+1:]
	}
	return strings.TrimSpace(v)
}

// Disconnect takes magpie out of the agent's config and puts back what the
// user had, the Agents page's "Disconnect from magpie" (Fate on Discord:
// picking the agent's default did it, but nothing said so). Unwire first,
// for an agent whose default would leave it as installed; then each field
// still on magpie goes to its default, as picking it does, and so does
// each one magpie set that reads as magpie set it (an effort). What magpie
// remembered setting is forgotten.
func (a *Agent) Disconnect() error {
	if a.Native != nil {
		plan, err := a.Native.Disconnect()
		if err != nil {
			return err
		}
		return a.Native.Execute(plan)
	}
	if !a.Wired() {
		return nil
	}
	before, rec := a.Values(), appliedOf(a.ID)
	if a.Unwire != nil {
		if err := a.Unwire(); err != nil {
			return err
		}
	}
	for _, f := range a.Fields {
		vals := a.Values()
		v := vals[f.Key]
		// set by magpie: as it set it, or its group by the model's name
		set := v != "" && (rec.Fields[f.Key] == v || sameGroup(rec.Fields[f.Key], v)) && before[f.Key] == v
		if v == "" || !set && v != magpieID && !magpieValue(a, f, v, vals) {
			continue
		}
		if err := f.Set(""); err != nil {
			return fmt.Errorf("%s: %w", f.Label, err)
		}
	}
	// a field its own Set left empty goes back to what it was on before
	// Connect (Goose's own provider and model)
	var was map[string]string
	json.Unmarshal([]byte(unstash(a.ID+".connect.was")), &was)
	after := a.Values()
	for _, f := range a.Fields {
		if w := was[f.Key]; w != "" && after[f.Key] == "" && w != magpieID && !magpieValue(a, f, w, was) {
			if err := f.Set(w); err != nil {
				return fmt.Errorf("%s: %w", f.Label, err)
			}
		}
	}
	// what it was on through magpie, for switching it on again
	back := reconnection{Magpie: map[string]string{}, Left: a.Values()}
	for _, f := range a.Fields {
		if v := before[f.Key]; v != back.Left[f.Key] {
			back.Magpie[f.Key] = v
			if v == magpieID || magpieValue(a, f, v, before) {
				back.Routed = append(back.Routed, f.Key)
			}
		}
	}
	b, _ := json.Marshal(back)
	stash(map[string]string{a.ID + ".reconnect": string(b)})
	a.Keep()
	return nil
}

// DisconnectOffline restores a native agent's saved files only after an
// explicit offline choice. Legacy callers of Disconnect keep live semantics.
func (a *Agent) DisconnectOffline() error {
	if a.Native == nil || a.Native.ExecuteOffline == nil {
		return fmt.Errorf("%s does not support offline disconnect", a.Name)
	}
	plan, err := a.Native.Disconnect()
	if err != nil {
		return err
	}
	return a.Native.ExecuteOffline(plan)
}

// reconnection is what Disconnect took an agent off: the values of its
// fields it changed (Routed: those that were magpie's) and what it left
// every field at.
type reconnection struct {
	Magpie map[string]string `json:"magpie"`
	Routed []string          `json:"routed,omitempty"`
	Left   map[string]string `json:"left"`
}

// reconnect puts back the models an agent was on when it was disconnected
// (the owner: an agent remembers its last pick), if nothing has changed
// it since and magpie still routes them; false when it doesn't apply.
func (a *Agent) reconnect(now map[string]string) (bool, error) {
	var rec reconnection
	if json.Unmarshal([]byte(unstash(a.ID+".reconnect")), &rec) != nil || len(rec.Routed) == 0 {
		return false, nil
	}
	for k, v := range rec.Left {
		if now[k] != v {
			return false, nil
		}
	}
	for _, k := range rec.Routed {
		f := a.Field(k)
		if f == nil || rec.Magpie[k] != magpieID && !magpieValue(a, *f, rec.Magpie[k], rec.Magpie) {
			return false, nil
		}
	}
	// the models on magpie first, as Connect sets them (setting Gemini
	// CLI's model signs it in through magpie, which its sign-in field
	// can't), then the rest that aren't so by then
	var keys []string
	for _, k := range rec.Routed {
		if rec.Magpie[k] != magpieID {
			keys = append(keys, k)
		}
	}
	for _, f := range a.Fields {
		if _, ok := rec.Magpie[f.Key]; ok && !slices.Contains(keys, f.Key) {
			keys = append(keys, f.Key)
		}
	}
	for _, k := range keys {
		v := rec.Magpie[k]
		if v != a.Values()[k] {
			if err := a.Apply(k, v); err != nil {
				return true, err
			}
		} else {
			// set along with the model (Codex's effort): magpie's as before,
			// for Disconnect to take out again
			record(a.ID, k, v)
		}
	}
	return a.Wired(), nil
}

// lastJSONLTime reads the newest Unix timestamp (seconds or milliseconds)
// under key in the last lines of a prompt log, passing over the lines whose
// text (under textKey) is a slash command: those an agent answers itself.
// Zero if there is none.
func lastJSONLTime(path, key, textKey string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	const tail = 64 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		f.Seek(st.Size()-tail, 0)
	}
	var last int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		var text string
		if json.Unmarshal(m[textKey], &text) == nil && strings.HasPrefix(strings.TrimSpace(text), "/") {
			continue
		}
		n, err := strconv.ParseInt(string(m[key]), 10, 64)
		if err != nil {
			continue
		}
		if n > 1e12 {
			n /= 1000
		}
		last = max(last, n)
	}
	if last == 0 {
		return time.Time{}
	}
	return time.Unix(last, 0)
}

// sameHost: two URLs name the same server, whatever their scheme (Codex
// asks magpie's http gateway over ws) or path.
func sameHost(a, b string) bool { return hostOf(a) == hostOf(b) }

// wiringOff checks what magpie wrote into an agent's config to reach the
// gateway — pairs of key and value, read with get — and says which no longer
// holds, "" when all do. A URL is quoted; a key's value never is.
func wiringOff(name, file string, get func(string) (string, bool), kvs ...string) string {
	for i := 0; i+1 < len(kvs); i += 2 {
		k, want := kvs[i], kvs[i+1]
		v, _ := get(k)
		if v == want {
			continue
		}
		where := name + "'s " + k + " (" + filepath.Base(file) + ")"
		switch {
		case v == "":
			return where + " is gone, so it no longer reaches magpie"
		case strings.Contains(strings.ToLower(k), "url"):
			return where + " is " + v + ", not magpie's gateway at " + want
		default:
			return where + " was changed, so magpie's gateway won't take its requests"
		}
	}
	return ""
}
