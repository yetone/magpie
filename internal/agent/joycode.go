package agent

// JoyCode (JD's VS Code–like IDE) keeps an OpenAI-compatible provider map in
// ~/.joycode/model-providers.json and the model a session starts on in
// ~/.joycode/config.toml (`model = "…"`). Custom providers are keyed under
// "providers"; each has name, base_url (the gateway origin — JoyCode appends
// v1/chat/completions itself), models_url, api_key and enabled_models.
// Models from a provider are named "<provider id>/<model id>" in config.toml.
//
// magpie adds providers.magpie at the gateway and sets model to
// magpie/<catalog id>, with model_provider = "magpie". JoyCode's pro-kernel
// model-repair rewrites any model not in its official catalog to JoyAI on
// every open — unless model_provider is set and is not "jdcloud". Setting it
// to "magpie" skips that rewrite. Joined keeps the connection while the
// session model is JoyCode's own (as Hermes does), so a repair can't show
// 未接入 and wipe the provider. Disconnect restores the previous model and
// drops providers.magpie.

import (
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

func joycode(home string) *Agent { return joycodeIn(here(home)) }

// joycodeIn is JoyCode at a place: this machine's home, or a WSL distro's.
func joycodeIn(at place) *Agent {
	dir := filepath.Join(at.home, ".joycode")
	providers := filepath.Join(dir, "model-providers.json")
	cfg := filepath.Join(dir, "config.toml")
	keyModel := at.key("joycode.model")
	keyProv := at.key("joycode.model_provider")
	providerJSON := func() any { return joyProviderJSONAt(at) }
	keptProvider := func() any {
		return theirsKept(providers, "providers."+magpieID, providerJSON, "enabled_models")()
	}
	model := func() string {
		v, _ := edit.GetTOMLTop(cfg, "model")
		return v
	}
	modelProvider := func() string {
		v, _ := edit.GetTOMLTop(cfg, "model_provider")
		return v
	}
	ours := func() bool {
		_, ok := edit.GetJSON(providers, "providers."+magpieID)
		return ok
	}
	on := func() bool {
		ref, ok := strings.CutPrefix(model(), magpieID+"/")
		return ok && isMagpie(ref)
	}
	dropMagpie := func() error {
		if !isFile(providers) {
			return nil
		}
		return edit.DelJSON(providers, "providers."+magpieID)
	}
	putProvider := func() error {
		v := keptProvider()
		if !isFile(providers) {
			return edit.SetJSON(providers,
				edit.KV{Path: "version", Value: 1},
				edit.KV{Path: "models", Value: []any{}},
				edit.KV{Path: "providers." + magpieID, Value: v},
			)
		}
		return edit.SetJSON(providers, edit.KV{Path: "providers." + magpieID, Value: v})
	}
	// setModel writes config.toml's model and model_provider. provider ""
	// deletes the key (JoyCode then uses its official catalog).
	setModel := func(modelVal, providerVal string, delProvider bool) error {
		if modelVal == "" {
			if isFile(cfg) {
				if err := edit.DelTOMLTop(cfg, "model"); err != nil {
					return err
				}
			}
		} else if err := edit.SetTOMLTop(cfg, edit.KV{Path: "model", Value: modelVal}); err != nil {
			return err
		}
		if delProvider {
			if isFile(cfg) && modelProvider() != "" {
				return edit.DelTOMLTop(cfg, "model_provider")
			}
			return nil
		}
		if providerVal == "" {
			return nil
		}
		return edit.SetTOMLTop(cfg, edit.KV{Path: "model_provider", Value: providerVal})
	}
	return atomic(&Agent{
		ID: "joycode", Name: "JoyCode", Icon: "joycode-color", Aliases: []string{"joy-code", "jd-code"}, Spelled: prefixed,
		UA:  []string{"joycode"},
		Dir: dir, Path: cfg,
		Sync: func() error {
			return syncJSON(providers, "providers."+magpieID, keptProvider)
		},
		Joined: ours,
		Unwire: func() error {
			// clear magpie's model_provider even when the session model is
			// already JoyCode's own (after Pro repaired it, or the user
			// picked an own model while still joined)
			if isFile(cfg) && modelProvider() == magpieID {
				if err := edit.DelTOMLTop(cfg, "model_provider"); err != nil {
					return err
				}
			}
			if !ours() {
				return nil
			}
			return dropMagpie()
		},
		Notice: func() string {
			if Running(`(?i)JoyCode`) {
				return "JoyCode reads its config at start-up — restart open JoyCode windows to use this."
			}
			return ""
		},
		Check: func() string {
			if !on() {
				return ""
			}
			if !ours() {
				return "JoyCode's magpie provider (model-providers.json) is gone, so it no longer reaches magpie"
			}
			if p := modelProvider(); p != magpieID {
				return "JoyCode's model_provider (config.toml) is " + orDefault(p) + ", so Pro may reset the model on open"
			}
			return wiringOff("JoyCode", providers, func(k string) (string, bool) {
				return edit.GetJSON(providers, "providers."+magpieID+"."+k)
			}, "base_url", at.gw(), "models_url", at.v1()+"/models")
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: model,
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !on() {
						forget(keyModel, keyProv)
						wasProv := modelProvider()
						if wasProv == magpieID {
							wasProv = ""
						}
						stash(map[string]string{
							keyModel: model(),
							keyProv:  wasProv,
						})
					}
					if err := putProvider(); err != nil {
						return err
					}
					return setModel(v, magpieID, false)
				}
				if v == "" {
					if on() {
						wasModel, wasProv := unstash(keyModel), unstash(keyProv)
						if err := setModel(wasModel, wasProv, wasProv == ""); err != nil {
							return err
						}
						if ours() {
							return dropMagpie()
						}
						return nil
					}
					return setModel("", "", true)
				}
				// own model: keep providers.magpie for Joined (Disconnect's
				// Unwire drops it). Restore the stash as Hermes does, then
				// write the pick so the picker still offers magpie's models.
				if on() {
					unstash(keyModel)
					wasProv := unstash(keyProv)
					return setModel(v, wasProv, wasProv == "")
				}
				if ours() && modelProvider() == magpieID {
					return setModel(v, "", true)
				}
				return setModel(v, "", false)
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c)})
				}
				return append(group("JoyCode", own), viaMagpie("joycode", magpieID+"/")...)
			},
		}},
	}, providers, cfg)
}

// joyProviderJSONAt is magpie's entry in model-providers.json. base_url is
// the gateway origin (no /v1): JoyCode joins v1/chat/completions onto it,
// as it does for DongColor's llm-gw.jd.local. models_url is the catalog;
// enabled_models lists magpie's catalog ids so JoyCode shows them (and so
// a pick is visible in that file for WSL wiring checks).
// The key names the agent (magpie-joycode) so Usage attributes it.
func joyProviderJSONAt(at place) any {
	var ids []string
	for _, m := range magpieModels("joycode") {
		ids = append(ids, m.ID)
	}
	if ids == nil {
		ids = []string{}
	}
	return map[string]any{
		"name":           "magpie",
		"base_url":       at.gw(),
		"models_url":     at.v1() + "/models",
		"api_key":        agentKeyAt("joycode", at.gw()),
		"enabled_models": ids,
	}
}
