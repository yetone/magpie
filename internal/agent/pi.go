package agent

import (
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// Pi's enabledModels (settings.json) is its Ctrl+P cycle and /scoped-models
// list. While it names anything, a new session starts on defaultModel only
// when the list takes that model in, and on the list's first model when it
// doesn't — so a model magpie picks outside it would never be used. The list
// is the user's: magpie adds the model it picks to it, as Pi's own model
// picker does, and never makes one where there is none.

// piLevels are the thinking levels a pattern may end in (provider/model:high).
var piLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// piScope is settings.json's enabledModels; nil when it is missing, empty
// or not a list of strings.
func piScope(settings string) []string {
	raw, ok := edit.GetJSON(settings, "enabledModels")
	if !ok {
		return nil
	}
	var pats []string
	if json.Unmarshal([]byte(raw), &pats) != nil || len(pats) == 0 {
		return nil
	}
	return pats
}

// piTakes reports whether Pi's scope pattern surely takes in ref, a
// provider/model: the same reference, a :level after it or not, or a glob
// (Pi's *, ? and [..], matched without case) that ref or its model id fits.
// A bare model id or part of one, which Pi resolves against every model it
// knows, is not counted: adding ref beside it costs nothing, since Pi drops
// a model listed twice.
func piTakes(pattern, ref string) bool {
	glob := strings.ContainsAny(pattern, "*?[")
	if i := strings.LastIndex(pattern, ":"); i >= 0 && slices.Contains(piLevels, pattern[i+1:]) {
		pattern = pattern[:i]
	}
	pattern, ref = strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(ref)
	if pattern == ref {
		return true
	}
	if !glob {
		return false
	}
	_, id, _ := strings.Cut(ref, "/")
	for _, s := range []string{ref, id} {
		if ok, _ := path.Match(pattern, s); ok {
			return true
		}
	}
	return false
}

// piScopeWith adds ref to Pi's enabledModels, when there is such a list and
// nothing in it takes ref in; the user's entries stay as they are.
func piScopeWith(settings, ref string) error {
	pats := piScope(settings)
	if pats == nil || slices.ContainsFunc(pats, func(p string) bool { return piTakes(p, ref) }) {
		return nil
	}
	return edit.SetJSON(settings, edit.KV{Path: "enabledModels", Value: append(pats, ref)})
}

// piScopeWithout takes magpie's models out of Pi's enabledModels once
// magpie's provider is gone from models.json, where they would name nothing.
// A list left with nothing is removed, never written empty.
func piScopeWithout(settings string) error {
	pats := piScope(settings)
	if pats == nil {
		return nil
	}
	kept := slices.DeleteFunc(slices.Clone(pats), func(p string) bool {
		return !strings.ContainsAny(p, "*?[") && strings.HasPrefix(strings.ToLower(p), magpieID+"/")
	})
	switch {
	case len(kept) == len(pats):
		return nil
	case len(kept) == 0:
		return edit.DelJSON(settings, "enabledModels")
	}
	return edit.SetJSON(settings, edit.KV{Path: "enabledModels", Value: kept})
}

// piStartup is the model a new Pi session starts on, as the model field
// shows it: the default when enabledModels takes it in (or there is no such
// list), else the list's first entry when that names one model outright.
func piStartup(settings, def string) string {
	pats := piScope(settings)
	if def == "" || pats == nil || slices.ContainsFunc(pats, func(p string) bool { return piTakes(p, def) }) {
		return def
	}
	first := strings.TrimSpace(pats[0])
	if i := strings.LastIndex(first, ":"); i >= 0 && slices.Contains(piLevels, first[i+1:]) {
		first = first[:i]
	}
	if strings.ContainsAny(first, "*?[") || !strings.Contains(first, "/") {
		return def
	}
	return first
}

// piOffered are the thinking levels Pi offers for model, a provider/model
// as the model field shows it, when it is one of magpie's: what Pi's
// /thinking lists for the entry magpie writes for it in models.json. nil
// for any other model, whose levels are Pi's own business.
//
// Kept per agent and model as the model lists are (stampedLists): Pi's
// Values asked it on every Drift of the window's poll, each walking the
// list and building the model's whole models.json entry, settings read
// again under it, 23 MB of what three minutes with the window up
// allocated with real accounts.
func piOffered(agentID, model string) []string {
	ref, ok := strings.CutPrefix(model, magpieID+"/")
	if !ok {
		return nil
	}
	return builtOffered.shared(agentID+"\x00"+ref, func() []string { return piOfferedNow(agentID, ref) })
}

var builtOffered stampedLists[string]

// piOfferedNow is piOffered built afresh, for ref, the model's id without
// magpie's provider.
func piOfferedNow(agentID, ref string) []string {
	for _, m := range sharedModels(agentID) {
		if m.ID == ref {
			return piSupported(piModelJSON(piAsked(agentID, m), "", true))
		}
	}
	return nil
}

// piSupported are the thinking levels Pi offers for a models.json entry, as
// pi-ai's getSupportedThinkingLevels reads it: off alone for a model not
// marked reasoning; else each of its levels not mapped to null, xhigh and
// max only where they are mapped.
func piSupported(e map[string]any) []string {
	if r, _ := e["reasoning"].(bool); !r {
		return []string{"off"}
	}
	levels, _ := e["thinkingLevelMap"].(map[string]any)
	var out []string
	for _, l := range piLevels {
		v, set := levels[l]
		if set && v == nil || !set && (l == "xhigh" || l == "max") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// piClamp is the level Pi runs a model at when it is set to level, as
// pi-ai's clampThinkingLevel picks it among the levels it offers: level
// itself, else the nearest above it, else the nearest below.
func piClamp(level string, offered []string) string {
	if slices.Contains(offered, level) {
		return level
	}
	at := slices.Index(piLevels, level)
	if at < 0 {
		if len(offered) > 0 {
			return offered[0]
		}
		return "off"
	}
	for _, l := range piLevels[at:] {
		if slices.Contains(offered, l) {
			return l
		}
	}
	for i := at - 1; i >= 0; i-- {
		if slices.Contains(offered, piLevels[i]) {
			return piLevels[i]
		}
	}
	return "off"
}
