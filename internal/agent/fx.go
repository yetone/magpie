package agent

// fx (fx.sh, Vercel Labs) keeps its settings in ~/.fx/settings.json: the
// provider it talks to under "provider" (gateway, codex, grok or one of
// the user's own), each provider's model under "models", and providers of
// the user's own under "providers". magpie adds itself there as the
// provider "magpie", keyless (auth none), speaking chat completions, with
// the catalog as its model_metadata; a model through magpie is
// "magpie/<provider>/<model>" here, provider "magpie" and models.magpie
// "<provider>/<model>" in fx's settings. fx sends no effort to a provider
// of the user's own, so there is no effort to pick.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// fxMaxModels is how many models fx takes in a provider's model_metadata;
// past it fx refuses the whole settings file.
const fxMaxModels = 256

func fx(home string) *Agent { return fxIn(here(home)) }

// fxIn is fx at a place: this machine's home, or a WSL distro's (see
// wsl.go), its provider naming the gateway as the distro reaches it.
func fxIn(at place) *Agent {
	fxProvider := func(cur string) any { return fxProviderAt(cur, at.v1()) }
	dir := filepath.Join(at.home, ".fx")
	path := filepath.Join(dir, "settings.json")
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
		ID: "fx", Name: "fx", Icon: "fx", Spelled: prefixed,
		Dir: dir, Path: path,
		// ~/.fx is where its settings are. No command: `fx` is a common name
		// (the JSON viewer has it too), as in WSL (wsl.go).
		detect: func() bool { return isDir(dir) },
		Sync: func() error {
			return syncJSON(path, "providers."+magpieID, func() any { return fxProvider(fxModels(path)[magpieID]) })
		},
		Notice: func() string {
			if Running(`(^|/)fx( |$)`) {
				return "fx reads its settings at start-up — restart open fx sessions to use this."
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
				return append(out, viaMagpie("fx", magpieID+"/")...)
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
// cut at fx's limit.
func fxProviderAt(cur, v1 string) any {
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
		ms[m.ID] = e
	}
	return map[string]any{
		"protocol": "openai-chat-completions", "base_url": v1, "auth": map[string]any{"type": "none"},
		"tool_choice_mode": "send", "model_metadata": ms,
	}
}
