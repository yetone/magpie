package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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
// setting of a field by the user goes through here.
func (a *Agent) Apply(key, v string) error {
	f := a.Field(key)
	if f == nil {
		return nil
	}
	if err := f.Set(v); err != nil {
		return err
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
	//              config, or something outside the file overrides it.
	Kind   string `json:"kind"`
	Field  string `json:"field"`         // the field it shows on
	Now    string `json:"now,omitempty"` // what that field says now
	Want   string `json:"want"`          // what setting it again sets
	Detail string `json:"detail"`        // what exactly is off, for a tooltip
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
	for _, f := range a.Fields {
		want, ok := rec.Fields[f.Key]
		if !ok || vals[f.Key] == want || !magpieValue(a, f, want, vals) || magpieValue(a, f, vals[f.Key], vals) {
			continue
		}
		return &Drift{Kind: "replaced", Field: f.Key, Now: vals[f.Key], Want: want,
			Detail: a.Name + "'s config was changed outside magpie: " + f.Label + " is " + orDefault(vals[f.Key]) + ", not " + want + " as magpie set it"}
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
		if used := a.LastUsed(); bypassed(used, rec.At, usage.LastSeen(a.ID)) {
			return &Drift{Kind: "bypassed", Field: on.Key, Now: vals[on.Key], Want: vals[on.Key],
				Detail: a.Name + " was used at " + used.Format("15:04") + " but none of its requests reached magpie — one started before magpie set it up still runs on its old config: restart it"}
		}
	}
	return nil
}

// installedURL is the gateway of the magpie people install, on its own
// port; a magpie on another one (magpie-dev, a sandbox) is run beside it.
var installedURL = "http://" + gateway.DefaultAddr

// elsewhere: this magpie is not on the installed one's port.
func elsewhere() bool { return !sameHost(gateway.URL(), installedURL) }

// theInstalled: this magpie runs beside the installed one, and the agent is
// wired to that one's gateway, not this one's — the installed magpie set it
// up, so it is that one's to check, not drift here. Otherwise the two would
// take turns flagging each other's wiring.
func (a *Agent) theInstalled() bool {
	if !elsewhere() || a.Path == "" {
		return false
	}
	b, err := os.ReadFile(a.Path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), gateway.DefaultAddr) && !strings.Contains(string(b), hostOf(gateway.URL()))
}

// bypassed: the agent was used — while this gateway was up and after magpie
// last set it — and no request of it arrived since. A request leaves within
// moments of the prompt; a little grace keeps one in flight from counting.
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
	if !one {
		return false
	}
	if isMagpie(v) {
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
	d := a.Drift()
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
