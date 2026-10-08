package agent

// JoyCode (JD's VS Code–like IDE) keeps an OpenAI-compatible provider map in
// ~/.joycode/model-providers.json and the model a session starts on in
// ~/.joycode/config.toml (`model = "…"`). Custom providers are keyed under
// "providers"; each has name, base_url (the gateway origin — JoyCode appends
// v1/chat/completions itself), models_url, api_key and enabled_models.
// Models from a provider are named "<provider id>/<model id>" in config.toml.
//
// magpie adds providers.magpie at the gateway and sets model to
// magpie/<catalog id>. Other providers and the rest of config.toml stay as
// they are. Disconnect puts the previous model back and drops providers.magpie.

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
	providerJSON := func() any { return joyProviderJSONAt(at) }
	keptProvider := func() any {
		return theirsKept(providers, "providers."+magpieID, providerJSON, "enabled_models")()
	}
	model := func() string {
		v, _ := edit.GetTOMLTop(cfg, "model")
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
	putModel := func(v string) error {
		if v == "" {
			if !isFile(cfg) {
				return nil
			}
			return edit.DelTOMLTop(cfg, "model")
		}
		return edit.SetTOMLTop(cfg, edit.KV{Path: "model", Value: v})
	}
	return atomic(&Agent{
		ID: "joycode", Name: "JoyCode", Icon: "joycode-color", Aliases: []string{"joy-code", "jd-code"}, Spelled: prefixed,
		UA:  []string{"joycode"},
		Dir: dir, Path: cfg,
		Sync: func() error {
			return syncJSON(providers, "providers."+magpieID, keptProvider)
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
						forget(keyModel)
						stash(map[string]string{keyModel: model()})
					}
					if err := putProvider(); err != nil {
						return err
					}
					return putModel(v)
				}
				if on() || ours() {
					if v == "" {
						v = unstash(keyModel)
					} else {
						forget(keyModel)
					}
					if err := dropMagpie(); err != nil {
						return err
					}
				}
				return putModel(v)
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
