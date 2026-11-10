package agent

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// A provider switched off or removed, or the last account of a sign-in
// signed out, takes its models out of the catalog, but an agent picked one
// of them keeps asking for it, and its next request is refused (#200:
// Copilot switched off, an agent still on copilot/…). Reseat moves such
// agents along with the change: to the same model from a provider still on,
// else back to the agent's own default.

// Move is one of an agent's fields moved off a model magpie no longer serves.
type Move struct {
	Agent string `json:"agent"` // the agent's name
	Field string `json:"field"` // model, or a tier (Claude Code's haiku …)
	From  string `json:"from"`  // the catalog model it was on
	To    string `json:"to"`    // the one it is on now; "" its own default
	// Error is why the agent couldn't be moved (its file unwritable, say):
	// it stays on From, which magpie no longer serves
	Error string `json:"error,omitempty"`
}

// String is the move as the CLI and the TUI say it.
func (m Move) String() string {
	if m.Error != "" {
		return m.Agent + " " + m.Field + ": still on " + m.From + ", not moved: " + m.Error
	}
	to := m.To
	if to == "" {
		to = "its default"
	}
	return m.Agent + " " + m.Field + ": " + m.From + " → " + to
}

// pick is a field on one of magpie's models.
type pick struct {
	a        *Agent
	key, val string
	ref      string // the catalog model
}

// picks reads every detected agent's fields that are on a model magpie serves.
func picks() []pick {
	var out []pick
	for _, a := range Detected() {
		if len(a.Fields) == 0 {
			continue
		}
		vals := a.Values()
		for _, f := range a.Fields {
			v := vals[f.Key]
			if v == "" || f.Options == nil {
				continue
			}
			// a list of models is the user's own, left as it is
			model, _, one := a.split(v)
			if !one {
				continue
			}
			for _, o := range f.Options(vals) {
				if o.Value == model && o.Ref != "" {
					out = append(out, pick{a, f.Key, v, o.Ref})
					break
				}
			}
		}
	}
	return out
}

// Reseat makes a change to the providers (change) and moves every agent
// whose model it stopped serving: to that model from another provider
// that serves it, else to the agent's own default. It answers the moves.
// Its error is the change's alone: once the change is made, an agent that
// can't be moved is a move with its Error and the others are still moved.
// An error then would say the change failed when it was made — the panel,
// told so, kept a removed provider's editor open and listed, and a second
// Remove said there was no such provider.
func Reseat(change func() error) ([]Move, error) {
	before := picks()
	if err := change(); err != nil {
		return nil, err
	}
	var moves []Move
	// the last fields first: a Claude Code tier moved after the main model
	// would have followed it, whatever model of its own there is for it
	for i := len(before) - 1; i >= 0; i-- {
		p := before[i]
		if served(p.ref) {
			continue
		}
		f := p.a.Field(p.key)
		vals := p.a.Values()
		// changed already, with another field
		if f == nil || !p.a.samePick(vals[p.key], p.val) {
			continue
		}
		var to Option
		if f.Options != nil {
			to = sameModel(f.Options(vals), p.ref, p.a.ID)
		}
		m := Move{Agent: p.a.Name, Field: f.Label, From: p.ref, To: to.Ref}
		// the same model elsewhere keeps the agent's suffix after it (omp's
		// thinking level); the agent's own default has none
		if to.Value != "" {
			_, suffix, _ := p.a.split(p.val)
			to.Value += suffix
		}
		if err := p.a.Apply(p.key, to.Value); err != nil {
			m.To, m.Error = "", err.Error()
		}
		moves = append(moves, m)
	}
	slices.Reverse(moves)
	return moves, nil
}

// samePick: a field's value now is still the pick read before the change.
// A found group's auto/<model> reads back as written while the group is
// served, but Claude Code is given a dotted Claude version dashed
// (ClaudeSpelled), and with the group gone nothing takes
// auto/claude-sonnet-4-5 back to auto/claude-sonnet-4.5. Both are the one
// group by its canonical id, so the agent is still moved off it.
func (a *Agent) samePick(now, before string) bool {
	if now == before {
		return true
	}
	m1, s1, one1 := a.split(now)
	m2, s2, one2 := a.split(before)
	return one1 && one2 && s1 == s2 && provider.IsGroupRef(m1) &&
		provider.CanonicalRef(m1) == provider.CanonicalRef(m2)
}

// served: magpie takes requests for the catalog model.
func served(ref string) bool {
	_, _, ok := provider.Resolve(ref)
	return ok
}

// sameModel is the option for ref's model from another provider still
// serving it: the same id, else one that differs only in how it is spelt
// (claude-sonnet-4.5 and claude-sonnet-4-5-20250929). For a group magpie
// found (group/auto-…), gone as found groups are turned off, a provider
// is kept out of them, or its model is down to one provider magpie has, it
// is that model from the agent's own subscription when it serves it (agent
// is the agent's id; freefcw on X), else from the first provider still
// serving it; none for any other routing group, which is nobody else's.
func sameModel(opts []Option, ref, agent string) Option {
	// a group's id carries Claude Code's [1m] mark as a model's does, and
	// this ref comes from the agent's own settings
	if gid, ok := provider.GroupIDOf(ref); ok {
		if !strings.HasPrefix(gid, "auto-") {
			return Option{}
		}
		// a provider the user keeps out of found groups last
		// (provider.SetAutoGroupSkip): skipping it is what took the group
		skip := provider.AutoGroupSkip()
		var first, kept Option
		for _, o := range opts {
			if o.Ref == "" || provider.IsGroupRef(o.Ref) || !served(o.Ref) {
				continue
			}
			pid, m, _ := strings.Cut(o.Ref, "/")
			if provider.AutoGroupOf(pid, m) != gid {
				continue
			}
			if provider.IsOwn(pid, agent) {
				return o
			}
			if first.Ref == "" && !slices.Contains(skip, pid) {
				first = o
			}
			if kept.Ref == "" {
				kept = o
			}
		}
		if first.Ref != "" {
			return first
		}
		return kept
	}
	pid, model, ok := strings.Cut(ref, "/")
	if !ok || pid+"/" == provider.GroupPrefix {
		return Option{}
	}
	want := modelKey(model)
	var alike Option
	for _, o := range opts {
		if o.Ref == "" || provider.IsGroupRef(o.Ref) || !served(o.Ref) {
			continue
		}
		_, m, _ := strings.Cut(o.Ref, "/")
		if m == model {
			return o
		}
		if alike.Ref == "" && modelKey(m) == want {
			alike = o
		}
	}
	return alike
}

var dated = lazyre.New(`-(\d{8}|\d{4}-\d{2}-\d{2})$`)

// modelKey is a model id as providers agree on it: its last part, lower
// case, with dots and underscores as dashes and no date.
func modelKey(m string) string {
	m = strings.ToLower(m[strings.LastIndex(m, "/")+1:])
	m = strings.NewReplacer(".", "-", "_", "-").Replace(m)
	return dated.ReplaceAllString(m, "")
}
