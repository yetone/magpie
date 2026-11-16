package provider

import (
	"encoding/json"
	"maps"
	"strings"

	"github.com/yetone/magpie/internal/plugin"
)

// Subscriptions whose sign-ins a sync leaves out on one computer
// (Ethereal499 on X: Claude's accounts kept out, since Claude is wary of
// the place they are used from). Their accounts here never go up, and
// theirs on the server are never put in here; the server's are carried
// through as they are, for the computers that do sync them, and nothing
// here is taken away for their missing there.

// Except splits s by subscription: kept is s without the sign-ins of the
// subscriptions named in except (provider ids, a built-in's or its
// plugin's, either naming both), held is theirs alone. With none named,
// kept is s itself and held nil; a nil s gives nil for both.
func (s *SignIns) Except(except []string) (kept, held *SignIns) {
	if s == nil || len(except) == 0 {
		return s, nil
	}
	out := exceptIDs(except)
	if len(out) == 0 {
		return s, nil
	}
	kept, held = &SignIns{}, &SignIns{}
	for _, l := range s.Logins {
		if out[l.Agent] {
			held.Logins = append(held.Logins, l)
		} else {
			kept.Logins = append(kept.Logins, l)
		}
	}
	for k, raw := range s.Plugins {
		to := kept
		if out[plugin.ProviderOf(k)] {
			to = held
		}
		if to.Plugins == nil {
			to.Plugins = map[string]json.RawMessage{}
		}
		to.Plugins[k] = raw
	}
	if agent, _, _ := strings.Cut(s.Codex, "|"); s.Codex != "" && out[agent] {
		held.Codex = s.Codex
	} else {
		kept.Codex = s.Codex
	}
	if len(held.Logins) == 0 && held.Plugins == nil && held.Codex == "" {
		held = nil
	}
	return kept, held
}

// With is s with held's sign-ins put back beside its own, as Except took
// them out: held's plugins and Codex only where s has none of its own.
func (s *SignIns) With(held *SignIns) *SignIns {
	if held == nil {
		return s
	}
	out := &SignIns{}
	if s != nil {
		out.Logins, out.Codex = append(out.Logins, s.Logins...), s.Codex
		out.Plugins = maps.Clone(s.Plugins)
	}
	out.Logins = append(out.Logins, held.Logins...)
	for k, raw := range held.Plugins {
		if _, ok := out.Plugins[k]; ok {
			continue
		}
		if out.Plugins == nil {
			out.Plugins = map[string]json.RawMessage{}
		}
		out.Plugins[k] = raw
	}
	if out.Codex == "" {
		out.Codex = held.Codex
	}
	return out
}

// exceptIDs are the provider ids in except, each with its other id: the
// plugin signed in beside a subscription (claude-plugin) and the
// subscription a -plugin id stands beside.
func exceptIDs(except []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range except {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out[id], out[id+"-plugin"] = true, true
		if base, ok := strings.CutSuffix(id, "-plugin"); ok && base != "" {
			out[base] = true
		}
	}
	return out
}
