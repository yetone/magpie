package davsync

import (
	"context"
	"maps"
	"slices"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/profile"
)

// An agent's field that sync wrote over a value picked on this computer,
// and that the user then puts back, is this computer's own (hayden4608 on
// Discord: two computers on one GitHub sync, each wanting its own Codex
// model, set each other's every sync). Putting the value back is what says
// it: a new value picked after a sync still goes up, as before, so a
// change made on any computer still reaches the others. From then on sync
// neither brings an own field in nor takes this computer's value up, while
// the agent's other fields, and every other part, still sync.
//
// The state is this computer's alone (sync-state.json): Brought is each
// field's value as the last sync that changed it here wrote it, Replaced
// the value picked here that it wrote over, if it was one (not one an
// earlier sync brought), and Own the fields put back since. Follow, from
// the Settings page, gives the own fields back to sync: the next sync
// brings the server's values in.

// noticeOwn makes own each field sync wrote over a pick, put back to that
// pick here since. Another value picked since is a pick like any other: it
// goes up, and what sync brought before is forgotten.
func noticeOwn(st *state, here map[string]string) {
	for k, v := range st.Brought {
		now, ok := here[k]
		if !ok || now == v { // the agent isn't here now, or the field is as sync left it
			continue
		}
		if was, ok := st.Replaced[k]; ok && now == was {
			if st.Own == nil {
				st.Own = map[string]bool{}
			}
			st.Own[k] = true
		}
		delete(st.Brought, k)
		delete(st.Replaced, k)
	}
}

// withoutOwn is b without this computer's own agent fields: what is
// hashed to tell a change, so a pick of an own field is no change to push
// and the server's value of one is no change to bring.
func withoutOwn(b backup.Bundle, own map[string]bool) backup.Bundle {
	if len(own) == 0 || b.Agents == nil {
		return b
	}
	a := maps.Clone(b.Agents)
	for k := range b.Agents {
		if isOwn(own, k) {
			delete(a, k)
		}
	}
	b.Agents = a
	return b
}

// An agent whose picks stay on this computer (settings' AgentsHere,
// chenxiang0010 on Discord, as an MCP server can be kept to one) is in an
// own set as "<agent>.*": every field of it, the ones it gains later too.
// That set is made for each sync and never kept in Own, so Settings'
// Follow the others doesn't give it back; the agent's row does.

// withHere is own with every field of here's agents.
func withHere(own map[string]bool, here []string) map[string]bool {
	if len(here) == 0 {
		return own
	}
	out := maps.Clone(own)
	if out == nil {
		out = map[string]bool{}
	}
	for _, id := range here {
		out[id+".*"] = true
	}
	return out
}

// isOwn is whether field k is in own: by itself, or as its agent's.
func isOwn(own map[string]bool, k string) bool {
	if own[k] {
		return true
	}
	id, _ := splitField(k)
	return own[id+".*"]
}

// hereOf is the agents whose picks stay here, as b's settings say, in
// order; none when sync leaves the agents out.
func hereOf(c Config, b backup.Bundle) []string {
	if !c.Agents || b.Settings == nil {
		return nil
	}
	return slices.Compact(slices.Sorted(slices.Values(b.Settings.AgentsHere)))
}

// hashesOwn is hashes of b with this computer's own fields left out.
func hashesOwn(b backup.Bundle, own map[string]bool) map[string]string {
	return hashes(withoutOwn(b, own))
}

// keepServersOwn puts the server's values of this computer's own fields
// back into the agents a sync takes up from here: they never go up.
func keepServersOwn(merged *backup.Bundle, remote backup.Bundle, own map[string]bool) {
	if len(own) == 0 {
		return
	}
	a := maps.Clone(merged.Agents)
	if a == nil {
		a = map[string]string{}
	}
	for k := range merged.Agents {
		if isOwn(own, k) {
			delete(a, k)
		}
	}
	for k, v := range remote.Agents {
		if isOwn(own, k) {
			a[k] = v
		}
	}
	merged.Agents = a
}

// brought is a field a bring changed here: the value it wrote over, and
// the value it reads now.
type brought struct{ was, now string }

// bringAgents puts fields (the server's agents) in here, but for keep's,
// this computer's own. An own field that another's change moved with it
// (a provider re-settling the model behind it) is put back. It returns
// each field the bring changed here.
func bringAgents(fields map[string]string, keep map[string]bool) (map[string]brought, error) {
	in := maps.Clone(fields)
	for k := range fields {
		if isOwn(keep, k) {
			delete(in, k)
		}
	}
	before := profile.Fields()
	_, err := backup.Restore(backup.Bundle{Agents: in}, backup.Parts{Agents: true})
	after := profile.Fields()
	back := map[string]string{}
	for k, v := range before {
		if isOwn(keep, k) && after[k] != v {
			back[k] = v
		}
	}
	if len(back) > 0 {
		if _, berr := profile.ApplyFields(back); berr != nil && err == nil {
			err = berr
		}
		after = profile.Fields()
	}
	got := map[string]brought{}
	for k, v := range after {
		if !isOwn(keep, k) && before[k] != v {
			got[k] = brought{before[k], v}
		}
	}
	return got, err
}

// broughtIn records what a bring set here, and what it wrote over when
// that was picked here rather than brought by an earlier sync.
func broughtIn(st *state, got map[string]brought) {
	if len(got) == 0 {
		return
	}
	if st.Brought == nil {
		st.Brought = map[string]string{}
	}
	if st.Replaced == nil {
		st.Replaced = map[string]string{}
	}
	for k, b := range got {
		if last, ok := st.Brought[k]; ok && last == b.was {
			delete(st.Replaced, k) // sync's own value, written over by sync
		} else {
			st.Replaced[k] = b.was
		}
		st.Brought[k] = b.now
	}
}

// OwnField is an agent field kept on this computer, as Settings names it.
type OwnField struct {
	Agent string `json:"agent"` // the agent's name
	Field string `json:"field"` // the field's label: model, effort…
}

// ownFields names st's own fields, by agent then field.
func ownFields(st state) []OwnField {
	if len(st.Own) == 0 {
		return nil
	}
	agents := map[string]*agent.Agent{}
	for _, a := range agent.Detected() {
		agents[a.ID] = a
	}
	keys := slices.Sorted(maps.Keys(st.Own))
	out := make([]OwnField, 0, len(keys))
	for _, k := range keys {
		id, field := splitField(k)
		f := OwnField{Agent: id, Field: field}
		if a := agents[id]; a != nil {
			f.Agent = a.Name
			if x := a.Field(field); x != nil && x.Label != "" {
				f.Field = x.Label
			}
		}
		out = append(out, f)
	}
	return out
}

// splitField is a field's agent and key: the last dot, as a WSL distro's
// name may have one (codex@wsl:Ubuntu-24.04.model).
func splitField(k string) (string, string) {
	for i := len(k) - 1; i >= 0; i-- {
		if k[i] == '.' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

// Follow gives this computer's own agent fields back to sync, and syncs:
// the server's values come in, and the others' picks follow again.
func Follow(ctx context.Context) error {
	err := locked(func() error {
		if _, ok := Load(); !ok {
			return ErrOff
		}
		st := loadState()
		if len(st.Own) == 0 {
			return nil
		}
		st.Follow = true
		saveState(st)
		return nil
	})
	if err != nil {
		return err
	}
	return SyncNow(ctx)
}
