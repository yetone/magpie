package agent

// Qoder's CLI (1.1) keeps its settings in $QODER_CONFIG_DIR/settings.json,
// ~/.qoder by default. A provider of one's own is an entry of "providers",
// its models picked as "<provider>/<model>":
//
//	{"providers":{"magpie":{"displayName":"magpie","protocol":"openai",
//	   "baseUrl":"http://127.0.0.1:3425/v1","apiKey":"magpie","model":…,
//	   "models":[{"model":…,"displayName":…,"contextWindow":…,"maxOutputTokens":…,
//	     "capabilities":{"tools":true,"vision":…,"thinking":{"modes":["enabled"],
//	       "supportsEffort":true,"supportedEffortLevels":[…]}}}]}},
//	 "model":{"name":"magpie/<model>","reasoningEffort":…,
//	   "preferences":{"magpie/<model>":{"reasoning":{"effort":…}}}}}
//
// Each model's effort is its own, in model.preferences (1.1.6x); the one in
// model.reasoningEffort is asked for only by a model with none there, and
// Qoder moves it into the preferences of the model it starts on, and drops
// it, so it counts once. magpie sets the model's preference, and keeps
// reasoningEffort for the Qoders before preferences.
//
// An openai provider is asked at baseUrl + /chat/completions, with
// reasoning_effort when the model says it takes one. Qoder offers custom
// providers only to a signed-in account whose plan has BYOK. Its requests
// say only undici, so they are not told apart from other clients'.
//
// Qoder CN (qoderclicn, @qodercn-ai/qoderclicn) is the same CLI for the
// China site, its own accounts and settings in $QODERCN_CONFIG_DIR,
// ~/.qoder-cn by default.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

func qoder(home string) *Agent {
	return qoderSite(home, qoderGlobal)
}

func qoderCN(home string) *Agent {
	return qoderSite(home, qoderChina)
}

// qoderBuild is what tells Qoder's two builds apart.
type qoderBuild struct {
	id, name, env, dir, bin string
	aliases, procs          []string
}

var (
	qoderGlobal = qoderBuild{id: "qoder", name: "Qoder", env: "QODER_CONFIG_DIR", dir: ".qoder", bin: "qodercli",
		aliases: []string{"qodercli", "qoder-cli"}, procs: []string{`(^|/)qodercli( |$)`, `(^|/)qoder( |$)`}}
	qoderChina = qoderBuild{id: "qoder-cn", name: "Qoder CN", env: "QODERCN_CONFIG_DIR", dir: ".qoder-cn", bin: "qoderclicn",
		aliases: []string{"qoderclicn", "qodercn", "qodercn-cli"}, procs: []string{`(^|/)qoderclicn( |$)`, `(^|/)qodercn( |$)`}}
)

func qoderSite(home string, b qoderBuild) *Agent {
	dir := os.Getenv(b.env)
	if dir == "" {
		dir = filepath.Join(home, b.dir)
	}
	path := filepath.Join(dir, "settings.json")
	key := b.id + ":" + path + ":"
	slot := "providers." + magpieID
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	// effort is what the model in use asks for: its preference, else
	// reasoningEffort
	effort := func() string {
		if v := qoderEffort(get("model.preferences"), get("model.name")); v != "" {
			return v
		}
		return get("model.reasoningEffort")
	}
	setEffort := func(model, v string) error {
		kvs := []edit.KV{}
		if model != "" {
			prefs, drop := qoderPreferences(get("model.preferences"), model, v)
			if drop {
				if err := edit.DelJSON(path, "model.preferences"); err != nil {
					return err
				}
			} else {
				kvs = append(kvs, edit.KV{Path: "model.preferences", Value: prefs})
			}
		}
		if v == "" {
			if err := edit.DelJSON(path, "model.reasoningEffort"); err != nil {
				return err
			}
		} else {
			kvs = append(kvs, edit.KV{Path: "model.reasoningEffort", Value: v})
		}
		if len(kvs) == 0 {
			return nil
		}
		return edit.SetJSON(path, kvs...)
	}
	onMagpie := func() bool {
		_, ok := cutMagpie(get("model.name"))
		return ok && qoderKeyed(get(slot+".apiKey"), b.id)
	}
	return &Agent{
		ID: b.id, Name: b.name, Icon: "qoder", Aliases: b.aliases,
		Bin: b.bin, Dir: dir, Path: path,
		Sync: func() error {
			ref, ok := cutMagpie(get("model.name"))
			if !ok {
				return nil
			}
			return syncJSON(path, slot, func() any { return qoderProvider(b.id, ref) })
		},
		Notice: func() string {
			if Running(b.procs...) {
				return b.name + " reads its settings as a session starts — open sessions keep the model they have; new ones use this."
			}
			return ""
		},
		Check: func() string {
			if !onMagpie() {
				return ""
			}
			return wiringOff(b.name, path, func(k string) (string, bool) { return edit.GetJSON(path, slot+"."+k) },
				"baseUrl", gatewayV1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string { return get("model.name") },
			Set: func(v string) error {
				if ref, ok := cutMagpie(v); ok {
					if !onMagpie() {
						stash(map[string]string{key + "model": get("model.name")})
					}
					// the effort in use goes with it to a model with none of
					// its own
					was := effort()
					if err := edit.SetJSON(path,
						edit.KV{Path: slot, Value: qoderProvider(b.id, ref)},
						edit.KV{Path: "model.name", Value: v}); err != nil {
						return err
					}
					if was == "" || qoderEffort(get("model.preferences"), v) != "" {
						return nil
					}
					return setEffort(v, was)
				}
				// out of magpie: its provider goes, and the model the user
				// had comes back when none is asked for
				if onMagpie() || qoderKeyed(get(slot+".apiKey"), b.id) {
					if err := edit.DelJSON(path, slot); err != nil {
						return err
					}
					if v == "" {
						v = unstash(key + "model")
					}
				}
				if v == "" {
					return edit.DelJSON(path, "model.name")
				}
				return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				return append(ownOptions("", cur["model"]), viaMagpie(b.id, magpieID+"/")...)
			},
		}, {
			// the effort of the model in use, in its model.preferences
			Key: "effort", Label: "effort",
			Get: effort,
			Set: func(v string) error { return setEffort(get("model.name"), v) },
			Options: func(map[string]string) []Option {
				return static("low", "medium", "high", "xhigh", "max")
			},
		}},
	}
}

// qoderEffort is the effort model's preference in prefs (model.preferences)
// asks for, from its reasoning or an older generation.reasoning.
func qoderEffort(prefs, model string) string {
	var m map[string]struct {
		Reasoning  *struct{ Effort string } `json:"reasoning"`
		Generation struct {
			Reasoning *struct{ Effort string } `json:"reasoning"`
		} `json:"generation"`
	}
	if model == "" || json.Unmarshal([]byte(prefs), &m) != nil {
		return ""
	}
	p := m[model]
	if p.Reasoning != nil {
		return p.Reasoning.Effort
	}
	if p.Generation.Reasoning != nil {
		return p.Generation.Reasoning.Effort
	}
	return ""
}

// qoderPreferences is prefs (model.preferences) with model's effort set to
// effort, or taken out when it is ""; drop when nothing is left.
func qoderPreferences(prefs, model, effort string) (out map[string]any, drop bool) {
	out = map[string]any{}
	json.Unmarshal([]byte(prefs), &out)
	if out == nil {
		out = map[string]any{}
	}
	p, _ := out[model].(map[string]any)
	if p == nil {
		p = map[string]any{}
	}
	if g, ok := p["generation"].(map[string]any); ok {
		// Qoder's own migration lifts generation's fields up; the effort
		// set here is the one to keep
		delete(g, "reasoning")
		if len(g) == 0 {
			delete(p, "generation")
		}
	}
	r, _ := p["reasoning"].(map[string]any)
	if r == nil {
		r = map[string]any{}
	}
	if effort == "" {
		delete(r, "effort")
	} else {
		r["effort"] = effort
		if r["enabled"] == false {
			delete(r, "enabled")
		}
	}
	if len(r) == 0 {
		delete(p, "reasoning")
	} else {
		p["reasoning"] = r
	}
	if len(p) == 0 {
		delete(out, model)
	} else {
		out[model] = p
	}
	return out, len(out) == 0
}

// qoderLevels are the efforts Qoder can ask for.
var qoderLevels = []string{"low", "medium", "high", "xhigh", "max"}

// qoderProvider is magpie's entry in Qoder's providers, every magpie model
// in it, model the one it starts on.
func qoderProvider(agent, model string) map[string]any {
	var ms []map[string]any
	for _, m := range magpieModels(agent) {
		caps := map[string]any{"tools": true, "vision": m.Images}
		var levels []string
		for _, e := range m.Efforts {
			if slices.Contains(qoderLevels, e) && !slices.Contains(levels, e) {
				levels = append(levels, e)
			}
		}
		if len(levels) > 0 {
			caps["thinking"] = map[string]any{"modes": []string{"enabled"}, "supportsEffort": true,
				"supportedEffortLevels": levels, "requiresBudgetForEnabled": false}
		}
		e := map[string]any{"model": m.ID, "displayName": m.Name, "capabilities": caps}
		if m.Context > 0 {
			e["contextWindow"] = m.Context
		}
		if m.Output > 0 {
			e["maxOutputTokens"] = maxTokens(m)
		}
		ms = append(ms, e)
	}
	return map[string]any{"displayName": "magpie", "protocol": "openai", "baseUrl": gatewayV1(),
		"apiKey": gateway.TokenFor(agent), "model": model, "models": ms}
}

// qoderKeyed: key is one magpie gives Qoder's provider — its own,
// gateway.TokenFor, since Qoder's requests carry Bun's User-Agent and
// nothing of Qoder's, or gateway.Token, which it was given before; a sync
// gives it the new one.
func qoderKeyed(key, agent string) bool {
	return key == gateway.TokenFor(agent) || key == gateway.Token
}
