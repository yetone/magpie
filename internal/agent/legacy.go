package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// legacyID is what agents knew the gateway by before magpie was called magpie.
const legacyID = "dial"

// RenameLegacy rewrites what an older dial left in the agents' files so it
// says magpie: a model spelled dial/… becomes magpie/… (which also writes
// the magpie provider entry the agent needs), and then the dial entry,
// Codex's table and catalog file, and Claude Code's token go. It runs at
// start-up; once nothing says dial any more it changes nothing. Errors are
// swallowed on purpose — a file magpie cannot rewrite is one the user can
// still fix from the picker.
func RenameLegacy() {
	for _, a := range local() {
		if _, err := os.Stat(a.Path); err != nil {
			continue
		}
		if a.ID == "codex" {
			// Set() would stash "dial" as the provider to go back to; say
			// magpie first so it sees a config that is already routed.
			if v, _ := edit.GetTOMLTop(a.Path, "model_provider"); v == legacyID {
				_ = edit.SetTOMLTop(a.Path, edit.KV{Path: "model_provider", Value: magpieID})
				if f := a.Field("model"); f != nil {
					_ = f.Set(f.Get())
				}
			}
		}
		for i := range a.Fields {
			f := &a.Fields[i]
			if strings.HasSuffix(f.Key, "effort") {
				// a thinking level, never a model: Pi's is read against
				// the models it offers, the whole catalog built at start
				// up for it (~52 MB with a real account's providers)
				continue
			}
			if rest, ok := strings.CutPrefix(f.Get(), legacyID+"/"); ok {
				// dial/… is what magpie wrote under its old name: magpie set
				// the agent up (setUp)
				if f.Set(magpieID+"/"+rest) == nil {
					record(a.ID, f.Key, f.Get())
				}
			}
		}
		switch a.ID {
		case "opencode":
			_ = edit.DelJSON(a.Path, "provider."+legacyID)
		case "crush":
			_ = edit.DelJSON(a.Path, "providers."+legacyID)
		case "pi":
			_ = edit.DelJSON(filepath.Join(a.Dir, "models.json"), "providers."+legacyID)
		case "codex":
			if tables, err := edit.TOMLTables(a.Path); err == nil && slices.Contains(tables, "model_providers."+legacyID) {
				_ = edit.DelTOMLTable(a.Path, "model_providers."+legacyID)
			}
			os.Remove(filepath.Join(a.Dir, legacyID+"-models.json"))
		case "claude":
			if t, _ := edit.GetJSON(a.Path, "env.ANTHROPIC_AUTH_TOKEN"); t == legacyID {
				_ = edit.SetJSON(a.Path, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: hereKey()})
			}
		}
	}
}

// MoveCursorEfforts moves an agent set to one of Cursor's ids at an effort
// (cursor/grok-4.7-low), from before magpie offered each family as one
// model, to that model (cursor/grok-4.7) spelled the same way, and — when
// the agent's effort is unset and it has that one — to the effort the id
// was at. A model the picker doesn't offer is left: the gateway still takes
// Cursor's id. It runs at start-up; errors are swallowed as RenameLegacy's.
func MoveCursorEfforts() { moveEfforts("cursor", provider.CursorBase) }

// MoveAntigravityEfforts does the same for Antigravity's ids at a level
// (antigravity/gemini-3.7-flash-high → antigravity/gemini-3.7-flash, at
// high when the agent's effort is unset and it has that one).
func MoveAntigravityEfforts() { moveEfforts("antigravity", provider.AntigravityBase) }

func moveEfforts(pid string, base func(string) (string, string, bool)) {
	offers := func(opts []Option, v string) bool {
		return slices.ContainsFunc(opts, func(o Option) bool { return o.Value == v })
	}
	for _, a := range local() {
		if _, err := os.Stat(a.Path); err != nil {
			continue
		}
		var vals map[string]string
		for i := range a.Fields {
			f := &a.Fields[i]
			if f.Options == nil || f.Key == "effort" {
				continue
			}
			v := f.Get()
			pre, rest := "", v
			if r, ok := strings.CutPrefix(v, magpieID+"/"); ok {
				if a.handWired() {
					// the user's own provider named magpie: not ours to move
					continue
				}
				pre, rest = magpieID+"/", r
			}
			id, ok := strings.CutPrefix(rest, pid+"/")
			if !ok {
				continue
			}
			b, effort, ok := base(id)
			if !ok {
				continue
			}
			nv := pre + pid + "/" + b
			if vals == nil {
				vals = a.Values()
			}
			if !offers(f.Options(vals), nv) || f.Set(nv) != nil {
				continue
			}
			vals[f.Key] = nv
			if e := a.Field("effort"); f.Key == "model" && effort != "" && e != nil && e.Options != nil && e.Get() == "" && offers(e.Options(vals), effort) {
				_ = e.Set(effort)
			}
		}
	}
}
