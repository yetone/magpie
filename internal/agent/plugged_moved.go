package agent

// A built-in agent can move onto a community agent plugin, as the retired
// built-in subscriptions moved onto provider plugins (AGENTS.md). While
// the plugin agentMovers names for it is installed and on, the agent is
// the plugin's: the built-in is left out of the list (on this machine and
// in WSL distros, wslKind.served), its id and aliases are the plugin's,
// and what the built-in had wired is handed over (handovers), so the
// plugin's agent reads as connected and puts back what the user had when
// magpie steps out. With the plugin off or removed the built-in is back.

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/agentplug"
	"github.com/yetone/magpie/internal/plugin"
)

// agentMovers are the built-in agents a community plugin serves, by id:
// the plugin's npm package. A var so tests can name their own.
var agentMovers = map[string]string{"agy": "@magpie-community/agent-agy"}

// movedAgent is the plugin listed that serves the built-in agent id, nil
// for none.
func movedAgent(id string) *agentplug.Agent {
	pkg, ok := agentMovers[id]
	if !ok {
		return nil
	}
	list, _ := agentplug.List()
	for _, p := range list {
		if movesTo(id, p) && plugin.Name(p.Spec) == pkg {
			return p
		}
	}
	return nil
}

// movesTo says p's agent is the one the built-in id moves onto: its id is
// the built-in's, and agentMovers names its package.
func movesTo(id string, p *agentplug.Agent) bool {
	pkg, ok := agentMovers[id]
	return ok && p.Desc.ID == id && plugin.Name(p.Spec) == pkg
}

// dropMoved is list without the built-ins a plugin listed serves.
func dropMoved(list []*Agent) []*Agent {
	moved := map[string]bool{}
	for id := range agentMovers {
		if movedAgent(id) != nil {
			moved[id] = true
		}
	}
	if len(moved) == 0 {
		return list
	}
	out := list[:0:0]
	for _, a := range list {
		if !moved[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

// handovers turn what a built-in agent wired at a place into the plugin's
// stash of its file f, by id; false when the built-in hadn't wired it.
var handovers = map[string]func(at place, f pluggedFile) (pluggedStash, bool){"agy": agyHandover}

// handedOver is the stash of the plugin spec's agent id at f, under key,
// made from what the built-in had wired there, and saved; false when
// there is nothing to hand over.
func handedOver(id string, at place, f pluggedFile, spec, key string) (pluggedStash, bool) {
	h := handovers[id]
	if h == nil || agentMovers[id] != plugin.Name(spec) {
		return pluggedStash{}, false
	}
	st, ok := h(at, f)
	if !ok {
		return st, false
	}
	st.Spec, st.Format = spec, f.format
	b, _ := json.Marshal(st)
	stash(map[string]string{key: string(b)})
	return st, true
}

// agyHandover is the built-in agy's wiring as the agent-agy plugin's,
// whose config is magpie's own settings file too (agyOverride). A
// settings.json an older built-in wired is given back first, as the
// built-in's Sync does (migrate), its model moved into magpie's file;
// then, while that file's model is magpie's, the file is the plugin's:
// every key it has was magpie's, so stepping out takes the file away.
func agyHandover(at place, f pluggedFile) (pluggedStash, bool) {
	if filepath.Base(f.path) != agyOverride {
		return pluggedStash{}, false
	}
	b := agyIn(at)
	if usesMagpie(jsonGet(b.Path, "model")()) {
		if err := b.Sync(); err != nil {
			return pluggedStash{}, false
		}
	}
	model, _ := f.get("model")
	if !usesMagpie(model) {
		return pluggedStash{}, false
	}
	had := map[string]string{}
	for _, k := range []string{"model", "modelProvider", agyModels} {
		had[k] = "\x00"
	}
	return pluggedStash{
		Had:   had,
		Ref:   strings.TrimPrefix(model, magpieID+"/"),
		Value: model,
		Wrote: map[string]string{"modelProvider": "gemini"},
	}, true
}
