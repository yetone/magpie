package agent

// Command Code (commandcode.ai) keeps its settings in ~/.commandcode/
// settings.json, the model as "provider/model", and takes providers of the
// user's own in ~/.commandcode/providers.json. magpie adds itself there as
// the provider "magpie", keyless, with the catalog as its models; a model
// through magpie is "magpie/<provider>/<model>", which Command Code splits
// at the first "/". Command Code still wants its own sign-in for these.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// ccEfforts are the reasoning levels Command Code knows.
var ccEfforts = []string{"low", "medium", "high", "xhigh", "max"}

func commandCode(home string) *Agent { return commandCodeIn(here(home)) }

// commandCodeIn is Command Code at a place: this machine's home, or a WSL
// distro's (see wsl.go), its provider naming the gateway as the distro
// reaches it.
func commandCodeIn(at place) *Agent {
	ccProviderJSON := func() any { return ccProviderJSONAt(at.v1()) }
	dir := filepath.Join(at.home, ".commandcode")
	path := filepath.Join(dir, "settings.json")
	providers := filepath.Join(dir, "providers.json")
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	dropMagpie := func() error {
		if _, err := os.Stat(providers); err != nil {
			return nil
		}
		return edit.DelJSON(providers, "provider."+magpieID)
	}
	return &Agent{
		ID: "commandcode", Name: "Command Code", Icon: "commandcode", Aliases: []string{"command-code", "cmd"}, Spelled: prefixed,
		UA:  []string{"command-code", "commandcode"},
		Bin: "command-code", Dir: dir, Path: path,
		Sync: func() error {
			return syncJSON(providers, "provider."+magpieID, theirsKept(providers, "provider."+magpieID, ccProviderJSON, "models"))
		},
		Notice: func() string {
			notes := []string{"Command Code wants its own sign-in (cmd login) even for models through magpie."}
			if Running(`(^|/)(cmd|cmdc|command-code|commandcode)( |$)`) {
				notes = append(notes, "It reads its settings at start-up — restart open Command Code sessions to use this.")
			}
			return strings.Join(notes, " ")
		},
		Check: func() string {
			if !usesMagpie(get("model")) {
				return ""
			}
			if p := get("modelProvider"); p != magpieID {
				return "Command Code's modelProvider (settings.json) is " + orDefault(p) + ", so it no longer asks magpie"
			}
			return wiringOff("Command Code", providers, func(k string) (string, bool) { return edit.GetJSON(providers, "provider."+magpieID+"."+k) },
				"baseURL", at.v1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string { return get("model") },
			Set: func(v string) error {
				if v == "" {
					keys := []string{"model"}
					if get("modelProvider") == magpieID {
						keys = append(keys, "modelProvider")
					}
					if err := edit.DelJSON(path, keys...); err != nil {
						return err
					}
					return dropMagpie()
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if err := edit.SetJSON(providers, edit.KV{Path: "provider." + magpieID, Value: theirsKept(providers, "provider."+magpieID, ccProviderJSON, "models")()}); err != nil {
						return err
					}
					return edit.SetJSON(path, edit.KV{Path: "model", Value: v}, edit.KV{Path: "modelProvider", Value: magpieID})
				}
				if get("modelProvider") == magpieID {
					if err := edit.DelJSON(path, "modelProvider"); err != nil {
						return err
					}
				}
				if err := edit.SetJSON(path, edit.KV{Path: "model", Value: v}); err != nil {
					return err
				}
				return dropMagpie()
			},
			Options: func(cur map[string]string) []Option {
				var out []Option
				if v := cur["model"]; v != "" && !usesMagpie(v) {
					out = append(out, Option{Value: v, Icon: modelIcon("", v)})
				}
				return append(out, viaMagpie("commandcode", magpieID+"/")...)
			},
		}, {
			// settings.json's reasoningEffort keeps an effort for each model
			// (Command Code's /effort saves it); this is the current model's
			Key: "effort", Label: "effort",
			Get: func() string { return ccEffortMap(path)[get("model")] },
			Set: func(v string) error {
				model := get("model")
				if model == "" {
					return fmt.Errorf("pick Command Code's model first; it keeps an effort for each model")
				}
				efforts := ccEffortMap(path)
				if v == "" {
					delete(efforts, model)
				} else {
					efforts[model] = v
				}
				if len(efforts) == 0 {
					return edit.DelJSON(path, "reasoningEffort")
				}
				// written whole, as model ids hold dots
				return edit.SetJSON(path, edit.KV{Path: "reasoningEffort", Value: efforts})
			},
			Options: func(cur map[string]string) []Option {
				if ref, ok := strings.CutPrefix(cur["model"], magpieID+"/"); ok {
					for _, m := range magpieModels("commandcode") {
						if m.ID != ref {
							continue
						}
						var efforts []string
						for _, x := range ccEfforts {
							if slices.Contains(m.Efforts, x) {
								efforts = append(efforts, x)
							}
						}
						if len(efforts) > 0 {
							return static(efforts...)
						}
					}
				}
				return static(ccEfforts...)
			},
		}},
	}
}

// ccProviderJSONAt is magpie's entry in providers.json, for a Command Code
// reaching the gateway's /v1 at v1. The key is false: the gateway takes
// any, and Command Code refuses one written out.
func ccProviderJSONAt(v1 string) any {
	ms := map[string]any{}
	for _, m := range magpieModels("commandcode") {
		e := map[string]any{"name": m.Name}
		var efforts []string
		for _, x := range m.Efforts {
			if slices.Contains(ccEfforts, x) {
				efforts = append(efforts, x)
			}
		}
		if len(efforts) > 0 {
			e["reasoning"] = true
			e["reasoningEfforts"] = efforts
		}
		ms[m.ID] = e
	}
	return map[string]any{"name": "magpie", "api": "openai-completions", "baseURL": v1, "apiKey": false, "models": ms}
}

// ccEffortMap is settings.json's reasoningEffort, model id to effort.
func ccEffortMap(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var c struct {
		Efforts map[string]any `json:"reasoningEffort"`
	}
	if json.Unmarshal(b, &c) != nil {
		return out
	}
	for k, v := range c.Efforts {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
