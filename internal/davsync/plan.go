package davsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/memory"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// applied is the last sync that brought parts in from the server (#6428):
// which, the copy of this computer kept before (keepSealed), and the
// parts' hashes right after, so UndoSync can tell a change made since.
type applied struct {
	At    time.Time         `json:"at"`
	Parts []string          `json:"parts"`
	File  string            `json:"file"`
	After map[string]string `json:"after,omitempty"`
}

// Applied is what Status tells of the last sync that brought parts in,
// while its copy is there to undo it.
type Applied struct {
	At    time.Time `json:"at"`
	Parts []string  `json:"parts"`
}

// Plan is what a sync would do now (Preview), nothing done.
type Plan struct {
	// Empty: the server has no file yet, and this computer's goes up.
	Empty bool `json:"empty,omitempty"`
	// Follow: this computer follows the server, never writing it.
	Follow bool       `json:"follow,omitempty"`
	Parts  []PartPlan `json:"parts"`
	// Memory is how many memories come in here and go up, by id.
	Memory *MemoryPlan `json:"memory,omitempty"`
	// SignIns is how many sign-ins change here, when they sync.
	SignIns int `json:"signIns,omitempty"`
	// PluginsGone are the plugins removed here because another computer
	// removed them.
	PluginsGone []string `json:"pluginsGone,omitempty"`
	// Token tells this plan from another: SyncPreviewed syncs only while
	// the sync would still do what it says.
	Token string `json:"token"`
}

// PartPlan is what a sync would do with one part.
type PartPlan struct {
	Part string `json:"part"`
	// Do is "in" (the server's comes in here), "up" (this computer's goes
	// to the server), "same", or "off" (not synced: stays as it is here).
	Do string `json:"do"`
	// Replaces: a change made here too is replaced, a copy kept first.
	Replaces bool   `json:"replaces,omitempty"`
	Items    []Item `json:"items,omitempty"`
}

// Item is one thing in a part that the sync adds, removes or changes on
// the side it writes. Only names: never a value, which may be a key.
type Item struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Change string `json:"change"` // added, removed, changed
}

// MemoryPlan counts the memories a sync would change on each side.
type MemoryPlan struct {
	In int `json:"in"`
	Up int `json:"up"`
}

// ErrPlanChanged is SyncPreviewed's when the sync would now do something
// other than the preview said: something changed here or on the server.
var ErrPlanChanged = errors.New("something changed since the preview: look at it again before syncing")

// Preview is what syncing now would do, with the setup saved; nothing is
// changed here or on the server.
func Preview(ctx context.Context) (Plan, error) {
	var p Plan
	err := locked(func() error {
		c, ok := Load()
		if !ok {
			return ErrOff
		}
		st := loadState()
		if st.Key != stateKey(c) {
			st = state{Key: stateKey(c), Brought: st.Brought, Replaced: st.Replaced, Own: st.Own, Follow: st.Follow, Here: st.Here}
		}
		var err error
		p, err = preview(ctx, c, st)
		return err
	})
	return p, err
}

// PreviewWith is Preview for a setup not saved yet: Settings shows what
// saving it would do first. A password or passphrase left empty is the
// saved one's, as Configure takes it.
func PreviewWith(ctx context.Context, c Config) (Plan, error) {
	var p Plan
	err := locked(func() error {
		if old, ok := Load(); ok {
			if c.Password == "" && c.URL == old.URL && c.User == old.User && c.Endpoint == old.Endpoint {
				c.Password = old.Password
			}
			if c.Passphrase == "" {
				c.Passphrase = old.Passphrase
			}
		}
		st := loadState()
		if st.Key != stateKey(c) {
			st = state{Key: stateKey(c), Brought: st.Brought, Replaced: st.Replaced, Own: st.Own, Follow: st.Follow, Here: st.Here}
		}
		var err error
		p, err = preview(ctx, c, st)
		return err
	})
	return p, err
}

// preview runs syncOnce as a preview over a copy of st: the server's file
// is read whole, whatever was seen of it before.
func preview(ctx context.Context, c Config, st state) (Plan, error) {
	st.Server = version{}
	st.Local, st.Remote = maps.Clone(st.Local), maps.Clone(st.Remote)
	st.Own, st.Brought, st.Replaced = maps.Clone(st.Own), maps.Clone(st.Brought), maps.Clone(st.Replaced)
	var p Plan
	if err := syncOnce(ctx, c, &st, &p); err != nil {
		return Plan{}, err
	}
	p.Follow = c.FollowServer
	p.Token = ""
	j, _ := json.Marshal(p)
	p.Token = sum(j)[:16]
	return p, nil
}

// previewed fills pv from what syncOnce decided.
func previewed(pv *Plan, c Config, local, remote, merged backup.Bundle, bringIn, here, there, goneThere []string, prevSignIns map[string]string) error {
	M := hashes(merged)
	Rh := hashes(remote)
	for _, p := range Parts {
		pp := PartPlan{Part: p, Do: "same"}
		switch {
		case !c.syncs(p):
			pp.Do = "off"
		case slices.Contains(bringIn, p):
			pp.Do = "in"
			pp.Replaces = slices.Contains(here, p)
			pp.Items = diffItems(itemsOf(local, p), itemsOf(remote, p))
		case M[p] != Rh[p] && !c.FollowServer:
			pp.Do = "up"
			pp.Replaces = slices.Contains(there, p)
			pp.Items = diffItems(itemsOf(remote, p), itemsOf(local, p))
		}
		pv.Parts = append(pv.Parts, pp)
	}
	pv.PluginsGone = slices.Clone(goneThere)
	if c.memoryOn() && (local.Memory != nil || remote.Memory != nil) {
		both := memory.Combine(local.Memory, remote.Memory)
		in, up := memoryDiff(local.Memory, both), memoryDiff(remote.Memory, both)
		if c.FollowServer {
			up = 0
		}
		if in+up > 0 {
			pv.Memory = &MemoryPlan{In: in, Up: up}
		}
	}
	if c.SignIns {
		_, plan, _ := provider.MergeSignIns(local.SignIns, remote.SignIns, prevSignIns)
		pv.SignIns = plan.Count()
	}
	return nil
}

// memoryDiff is how many memories of to aren't in from as they are there:
// new, changed, or deleted.
func memoryDiff(from, to *memory.Bundle) int {
	key := func(m *memory.Memory) string {
		k := *m
		k.Used, k.Uses = time.Time{}, 0
		j, _ := json.Marshal(k)
		return string(j)
	}
	have := map[string]string{}
	if from != nil {
		for _, m := range from.Memories {
			if m != nil {
				have[m.ID] = key(m)
			}
		}
	}
	n := 0
	seen := map[string]bool{}
	for _, m := range to.Memories {
		if m == nil {
			continue
		}
		seen[m.ID] = true
		if have[m.ID] != key(m) {
			n++
		}
	}
	for id := range have {
		if !seen[id] {
			n++
		}
	}
	return n
}

// itemsOf is part p of b as named items (kind\x00key → its JSON), as its
// hash sees them: what is this computer's own left out.
func itemsOf(b backup.Bundle, p string) map[string]item {
	out := map[string]item{}
	switch p {
	case "providers":
		flatten(out, "provider", syncedProviders(b.Providers))
		flatten(out, "group", b.Groups)
		if b.Searches != nil {
			flatten(out, "search", *b.Searches)
		}
		if b.Plugins != nil {
			flatten(out, "plugin", *b.Plugins)
		}
		if b.Proxies != nil {
			flatten(out, "proxy", *b.Proxies)
		}
		if len(b.Order) > 0 || len(b.GroupOrder) > 0 {
			flatten(out, "order", map[string]any{"providers": b.Order, "groups": b.GroupOrder})
		}
	case "settings":
		var s settings.Settings
		if b.Settings != nil {
			s = *b.Settings
		}
		s.KeepOwn(settings.Settings{})
		flatten(out, "setting", s)
		if b.GatewayKeys != nil {
			flatten(out, "gatewayKey", *b.GatewayKeys)
		}
		if b.AccountGroups != nil {
			flatten(out, "accountGroup", *b.AccountGroups)
		}
	case "profiles":
		flatten(out, "profile", b.Profiles)
	case "agents":
		flatten(out, "agent", b.Agents)
	case "library":
		if b.Library == nil {
			break
		}
		var top map[string]json.RawMessage
		j, _ := json.Marshal(b.Library)
		json.Unmarshal(j, &top)
		for k, v := range top {
			if k == "icons" {
				continue
			}
			var x any
			json.Unmarshal(v, &x)
			if k == "instructions" {
				out["instructions\x00"] = item{Kind: "instructions", Raw: string(v)}
				continue
			}
			// billion-context's own config is one file, shown by its name
			if k == "bili" {
				out["bili\x00"] = item{Kind: "file", Name: "billion-context.json", Raw: string(v)}
				continue
			}
			flatten(out, k, x)
		}
	}
	return out
}

type item struct {
	Kind, Name, Raw string
}

// flatten puts v's items in out: an object's by key, a list's by each
// element's id, name or spec.
func flatten(out map[string]item, kind string, v any) {
	j, _ := json.Marshal(v)
	var x any
	json.Unmarshal(j, &x)
	switch x := x.(type) {
	case map[string]any:
		for k, e := range x {
			r, _ := json.Marshal(e)
			out[kind+"\x00"+k] = item{Kind: kind, Name: k, Raw: string(r)}
		}
	case []any:
		for i, e := range x {
			r, _ := json.Marshal(e)
			key, name := strconv.Itoa(i), ""
			if o, ok := e.(map[string]any); ok {
				for _, f := range []string{"id", "name", "spec", "key", "path"} {
					if s, ok := o[f].(string); ok && s != "" {
						key = s
						break
					}
				}
				for _, f := range []string{"name", "label", "id", "spec"} {
					if s, ok := o[f].(string); ok && s != "" {
						name = s
						break
					}
				}
			}
			if name == "" {
				name = key
			}
			out[kind+"\x00"+key] = item{Kind: kind, Name: name, Raw: string(r)}
		}
	}
}

// diffItems is what going from a to b adds, removes and changes, in order.
func diffItems(a, b map[string]item) []Item {
	var out []Item
	for _, k := range slices.Sorted(maps.Keys(b)) {
		was, ok := a[k]
		switch {
		case !ok:
			out = append(out, Item{Kind: b[k].Kind, Name: b[k].Name, Change: "added"})
		case was.Raw != b[k].Raw:
			out = append(out, Item{Kind: b[k].Kind, Name: b[k].Name, Change: "changed"})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(a)) {
		if _, ok := b[k]; !ok {
			out = append(out, Item{Kind: a[k].Kind, Name: a[k].Name, Change: "removed"})
		}
	}
	return out
}

// UndoSync puts back the parts the last sync brought in, as they were
// before it (#6428). It stays on this computer: the next sync doesn't take
// it to the server, and the server's copy comes in again only when it
// changes there. A part changed here since that sync is not written over
// unless force, and the error says which.
func UndoSync(force bool) ([]string, error) {
	var back []string
	err := locked(func() error {
		c, ok := Load()
		if !ok {
			return ErrOff
		}
		st := loadState()
		a := st.Applied
		if st.Key != stateKey(c) || a == nil || !isFile(a.File) {
			return errors.New("there is no sync to undo")
		}
		data, err := os.ReadFile(a.File)
		if err != nil {
			return fmt.Errorf("the copy kept before the sync is gone: %w", err)
		}
		kept, err := backup.Open(data, c.Passphrase)
		if err != nil {
			return err
		}
		local, err := collect(c)
		if err != nil {
			return err
		}
		ownNow := withHere(st.Own, st.Here)
		H := hashesOwn(local, ownNow)
		if !force {
			var since []string
			for _, p := range a.Parts {
				if a.After != nil && H[p] != a.After[p] {
					since = append(since, p)
				}
			}
			if len(since) > 0 {
				return &ChangedSince{Parts: since}
			}
		}
		if _, err := keepSealed(c, "before-undo"); err != nil {
			return fmt.Errorf("keeping a copy before undoing: %w", err)
		}
		for _, p := range a.Parts {
			if !c.syncs(p) || p == "library" && kept.Library == nil {
				continue
			}
			if p == "agents" {
				if _, err := bringAgents(kept.Agents, ownNow); err != nil {
					return fmt.Errorf("bringing back the %s: %w", p, err)
				}
			} else if err := bring(kept, p); err != nil {
				return fmt.Errorf("bringing back the %s: %w", p, err)
			}
			back = append(back, p)
		}
		// as the last sync left them: what is here now is not a change
		// to take up, so the undo stays on this computer
		if local, err = collect(c); err != nil {
			return err
		}
		H = hashesOwn(local, ownNow)
		if st.Local == nil {
			st.Local = map[string]string{}
		}
		for _, p := range back {
			st.Local[p] = H[p]
		}
		st.Applied = nil
		saveState(st)
		return nil
	})
	return back, err
}

// ChangedSince is UndoSync's when parts changed here after the sync.
type ChangedSince struct{ Parts []string }

func (e *ChangedSince) Error() string {
	return fmt.Sprintf("changed here since the sync: %v", e.Parts)
}
