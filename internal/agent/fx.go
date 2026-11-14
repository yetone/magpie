package agent

// fx (fx.sh, Vercel Labs) keeps its settings in ~/.fx/settings.json: the
// provider it talks to under "provider" (gateway, codex, grok or one of
// the user's own), each provider's model under "models", and providers of
// the user's own under "providers". magpie adds itself there as the
// provider "magpie", keyless (auth none), speaking chat completions, with
// the catalog as its model_metadata; a model through magpie is
// "magpie/<provider>/<model>" here, provider "magpie" and models.magpie
// "<provider>/<model>" in fx's settings. Each model's reasoning levels go
// in its reasoning_efforts, which fx 0.0.17 on lists in /effort and Ctrl+T
// and sends as reasoning_effort; with none picked ("default") it sends
// none. fx's own effort setting, one for every provider, stays the user's.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
)

// fxMaxModels is how many models fx takes in a provider's model_metadata;
// past it fx refuses the whole settings file.
const fxMaxModels = 256

// fxEffortsSince is the first fx that reads a model's reasoning_efforts
// (src/core/config/configured_provider.zig). An older one refuses a key
// it doesn't know in model_metadata, and with it the whole settings file.
const fxEffortsSince = "0.0.17"

// fxMaxEfforts is how many levels fx takes for one model; past it fx
// refuses the settings file.
const fxMaxEfforts = 16

// fxEffortName is a level's name as fx takes it: letters, digits, -, _
// and ., and never auto, adaptive or default, its words for no level.
var fxEffortName = lazyre.New(`^[A-Za-z0-9._-]{1,64}$`)

// fxVersion is the version of the fx agent at home: the installer's
// ~/.local/bin/fx, else the fx on PATH. A version that isn't 0.x is
// another program's (the JSON viewer's fx is 30 on); "" when unknown. A
// var so tests can set it.
var fxVersion = func(home string) string {
	for _, bin := range []string{filepath.Join(home, ".local", "bin", "fx"), binAt("fx")} {
		if bin == "" || !isFile(bin) {
			continue
		}
		if v := installedVersion(bin); strings.HasPrefix(v, "0.") {
			return v
		}
	}
	return ""
}

// fxTakesEfforts says whether fx at version v reads reasoning_efforts. One
// whose version isn't known is taken for an older one: a key it refuses
// costs every setting it has, a level left out only the pick.
func fxTakesEfforts(v string) bool {
	return v != "" && !Newer(fxEffortsSince, v)
}

// fxVersion is fx's version at the place: a distro's as its probe found it
// (none is asked yet, so "": #6636), this machine's asked of its binary.
func (p place) fxVersion() string {
	if p.spell != nil {
		return p.version
	}
	return fxVersion(p.home)
}

// fxEfforts is a model's reasoning_efforts for fx: each level the catalog
// gives it, weakest first as fx lists them, asked as reasoning_effort; nil
// for a model without levels.
func fxEfforts(efforts []string) orderedJSON {
	var out orderedJSON
	for _, e := range gateway.ByStrength(efforts) {
		switch strings.ToLower(e) {
		case "auto", "adaptive", "default":
			continue
		}
		if !fxEffortName.MatchString(e) || len(out) == fxMaxEfforts || slices.ContainsFunc(out, func(p jsonPair) bool { return p.k == e }) {
			continue
		}
		out = append(out, jsonPair{e, map[string]any{"reasoning_effort": e}})
	}
	return out
}

func fx(home string) *Agent { return fxIn(here(home)) }

// fxIn is fx at a place: this machine's home, or a WSL distro's (see
// wsl.go), its provider naming the gateway as the distro reaches it.
func fxIn(at place) *Agent {
	dir := at.join(".fx")
	path := filepath.Join(dir, "settings.json")
	// magpie's provider, with what the user set on it in fx kept
	fxProvider := func(cur string) any {
		return fxKept(path, fxProviderAt(cur, at.v1(), fxTakesEfforts(at.fxVersion())))
	}
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	// the provider fx talks to; unset, it is Vercel's AI Gateway
	prov := func() string {
		if p := get("provider"); p != "" {
			return p
		}
		return "gateway"
	}
	// own is the model fx has for one of its own providers: the gateway's
	// is also kept as the top-level "model", which models.gateway overrides
	own := func(p string) string {
		if v := fxModels(path)[p]; v != "" || p != "gateway" {
			return v
		}
		return get("model")
	}
	dropMagpie := func() error {
		keys := []string{"providers." + magpieID, "models." + magpieID}
		if get("provider") == magpieID {
			keys = append(keys, "provider")
		}
		return edit.DelJSON(path, keys...)
	}
	return &Agent{
		ID: "fx", Name: "fx", Icon: "fx", Spelled: prefixed, NoNames: true,
		Dir: dir, Path: path,
		// ~/.fx is where its settings are. No command: `fx` is a common name
		// (the JSON viewer has it too), as in WSL (wsl.go).
		detect:   func() bool { return agentDir(dir) },
		ListOnly: true,
		Sync: func() error {
			return syncJSON(path, "providers."+magpieID, func() any { return fxProvider(fxModels(path)[magpieID]) })
		},
		Notice: func() string {
			if Running(`(^|/)fx( |$)`) {
				return noticeFx.String()
			}
			return ""
		},
		Check: func() string {
			if prov() != magpieID {
				return ""
			}
			return wiringOff("fx", path, func(k string) (string, bool) { return edit.GetJSON(path, "providers."+magpieID+"."+k) },
				"base_url", at.v1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				if p := prov(); p != magpieID {
					return own(p)
				}
				if m := fxModels(path)[magpieID]; m != "" {
					return magpieID + "/" + m
				}
				return ""
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					return edit.SetJSON(path,
						edit.KV{Path: "providers." + magpieID, Value: fxProvider(ref)},
						edit.KV{Path: "provider", Value: magpieID},
						edit.KV{Path: "models." + magpieID, Value: ref})
				}
				// fx's own: magpie steps out, back to the provider fx
				// talks to without it
				if err := dropMagpie(); err != nil {
					return err
				}
				p := prov()
				if v == "" {
					keys := []string{"models." + p}
					if p == "gateway" {
						keys = append(keys, "model")
					}
					return edit.DelJSON(path, keys...)
				}
				return edit.SetJSON(path, edit.KV{Path: "models." + p, Value: v})
			},
			Options: func(cur map[string]string) []Option {
				var out []Option
				if v := cur["model"]; v != "" && !usesMagpie(v) {
					out = append(out, Option{Value: v, Icon: modelIcon("", v)})
				}
				return withVia(out, "fx", magpieID+"/")
			},
		}},
	}
}

// fxModels is settings.json's models, provider to model.
func fxModels(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var c struct {
		Models map[string]any `json:"models"`
	}
	if json.Unmarshal(b, &c) != nil {
		return out
	}
	for k, v := range c.Models {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// fxProviderAt is magpie's entry in settings.json's providers, for an fx
// reaching the gateway's /v1 at v1. Its model_metadata is what fx's model
// picker lists: the catalog, the model in use first so it is never the one
// cut at fx's limit. efforts: fx reads each model's reasoning levels.
func fxProviderAt(cur, v1 string, efforts bool) map[string]any {
	all := magpieModels("fx")
	slices.SortStableFunc(all, func(a, b catalog.Model) int {
		switch {
		case a.ID == cur && b.ID != cur:
			return -1
		case b.ID == cur && a.ID != cur:
			return 1
		}
		return 0
	})
	ms := map[string]any{}
	for _, m := range all {
		if len(ms) == fxMaxModels {
			break
		}
		e := map[string]any{"supports_tool_use": true, "supports_vision": m.Images}
		if m.Context > 0 {
			e["context_window"] = m.Context
		}
		// fx refuses an output limit that isn't below the context window
		if m.Output > 0 && (m.Context == 0 || m.Output < m.Context) {
			e["max_output_tokens"] = m.Output
		}
		if levels := fxEfforts(m.Efforts); efforts && len(levels) > 0 {
			e["reasoning_efforts"] = levels
		}
		ms[m.ID] = e
	}
	return map[string]any{
		"protocol": "openai-chat-completions", "base_url": v1, "auth": map[string]any{"type": "none"},
		"tool_choice_mode": "send", "model_metadata": ms,
	}
}

// fxKept is want, magpie's provider in fx's settings.json at path, with
// what the user set on the one there kept: fx takes a reviewer_model on a
// provider (the model its auto permission mode asks), which magpie doesn't
// write, and a tool_choice_mode, which magpie writes "send" and the user
// may turn to "omit" (src/core/config/configured_provider.zig); a sync wrote
// both away on every start, as T3 Code's did (#2030). Its protocol,
// address, auth and model_metadata are magpie's; auth is written whole, as
// fx refuses an env beside type none.
func fxKept(path string, want map[string]any) any {
	key := "providers." + magpieID
	if mode, ok := edit.GetJSON(path, key+".tool_choice_mode"); ok && (mode == "omit" || mode == "send") {
		want["tool_choice_mode"] = mode
	}
	return theirsKept(path, key, func() any { return want }, "model_metadata", "auth")()
}

// what fx says after a change (notice.go)
var (
	noticeFx = newNotice("fx reads its settings at start-up — restart open fx sessions to use this.")
)
