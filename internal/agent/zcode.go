package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// ZCode (Zhipu's desktop app) keeps its model providers in
// ~/.zcode/v2/config.json, OpenCode's provider shape with a kind of its own:
//
//	{"provider":{"magpie":{"name":"magpie","kind":"anthropic",
//	  "options":{"apiKey":"magpie","baseURL":"http://127.0.0.1:3425"},
//	  "enabled":true,"source":"custom",
//	  "models":{"<id>":{"name":…,"limit":{"context":…,"output":…},"modalities":{…},
//	    "reasoning":{"enabled":true,"variants":[…],"defaultVariant":…}}}}}}
//
// An anthropic provider is asked at baseURL + /v1/messages. The model is
// picked per task in ZCode's own picker and kept in its window, not in a
// file, so what magpie sets is whether its models are in that picker.
//
// ZCode 3.14 moved its providers to ~/.zcode/v2/provider_config.json and
// reads config.json's only once, to import them, so a provider added there
// later never reached its picker. There a provider is a rule, and a model's
// context window and inputs are rules of their own:
//
//	{"schemaVersion":1,"config":{
//	  "providerConfigRules":{"providerRules":[{"providerId":"magpie",
//	    "providerName":"magpie","enabled":true,"config":{
//	      "group":"standard-personal",
//	      "access":{"type":"api-key","apiKey":"magpie"},
//	      "api":{"type":"anthropic-messages","baseUrl":"http://127.0.0.1:3425"},
//	      "personalModelIds":[…],"modelOrder":[…]}}]},
//	  "modelConfigRules":{"providerModelRules":[{"providerId":"magpie",
//	    "modelId":…,"config":{"properties":{"contextWindow":…,
//	      "inputFormat":{"supportsImage":…}},
//	      "optionSpecs":{"maxOutputTokens":{"max":…},"reasoningLevel":{"values":[…]}}}}],
//	    "manualProviderModelRules":[…]}}}
//
// magpie writes both files, so an older ZCode sees its models too. A model
// the user set by hand in ZCode (a manual rule) keeps what they set.

func zcode(home string) *Agent {
	dir := filepath.Join(home, ".zcode")
	path := filepath.Join(dir, "v2", "config.json")
	rules := filepath.Join(dir, "v2", "provider_config.json")
	key := "provider." + magpieID
	wired := func() bool {
		_, ok := edit.GetJSON(path, key)
		return ok || zcodeRuled(rules)
	}
	return &Agent{
		ID: "zcode", Name: "ZCode", Icon: "zcode", Aliases: []string{"z-code"},
		UA:  []string{"zcode"},
		Dir: dir, Path: path,
		Notice: func() string {
			if Running(`ZCode\.app/`, `(^|/)ZCode( |$)`) {
				return "ZCode reads its providers at start-up — restart ZCode to see magpie's models in its picker."
			}
			return ""
		},
		Sync: func() error {
			if !wired() {
				return nil
			}
			// a ZCode that keeps its providers as rules, with magpie's taken
			// out there, had it removed in ZCode: it isn't put back
			if _, err := os.Stat(rules); err != nil || zcodeRuled(rules) {
				if err := zcodeRules(rules, true); err != nil {
					return err
				}
			}
			return edit.SetJSON(path, edit.KV{Path: key, Value: zcodeProviderJSON(path)})
		},
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if wired() {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				if err := zcodeRules(rules, v != ""); err != nil {
					return err
				}
				if v == "" {
					return edit.DelJSON(path, key)
				}
				return edit.SetJSON(path, edit.KV{Path: key, Value: zcodeProviderJSON(path)})
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in ZCode's picker"}}
			},
		}},
	}
}

// zcodeProviderJSON is magpie's provider in config.json at path; one
// turned off in ZCode stays off.
func zcodeProviderJSON(path string) any {
	ms := map[string]any{}
	for _, m := range magpieModels("zcode") {
		window := m.Context
		if window == 0 {
			window = 200000
		}
		in := []string{"text"}
		if m.Images {
			in = append(in, "image")
		}
		limit := map[string]any{"context": window}
		// without it ZCode caps every reply at 32000 tokens
		if out := zcodeOutput(maxTokens(m)); out > 0 {
			limit["output"] = out
		}
		e := map[string]any{"name": m.Name, "limit": limit,
			"modalities": map[string]any{"input": in, "output": []string{"text"}}}
		if levels := zcodeLevels(m.Efforts); levels != nil {
			e["reasoning"] = map[string]any{"enabled": true, "variants": levels, "defaultVariant": zcodeDefaultLevel(levels)}
		}
		ms[m.ID] = e
	}
	on := true
	if v, ok := edit.GetJSON(path, "provider."+magpieID+".enabled"); ok && v == "false" {
		on = false
	}
	return map[string]any{"name": "magpie", "kind": "anthropic", "enabled": on, "source": "custom",
		"options": map[string]any{"apiKey": gateway.Token, "baseURL": gateway.URL()}, "models": ms}
}

// zcodeMaxOutput caps a model's output limit: some vendors report their
// context window there (grok's 500000), and ZCode would offer that much.
const zcodeMaxOutput = 128000

func zcodeOutput(n int) int { return min(n, zcodeMaxOutput) }

// zcodeLevels are a model's reasoning levels as ZCode names them: "disabled"
// turns thinking off, any other is sent as the effort.
func zcodeLevels(efforts []string) []string {
	var out []string
	for _, e := range efforts {
		if e == "none" {
			e = "disabled"
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// zcodeDefaultLevel is medium where the model has it, else its middle level
// that thinks.
func zcodeDefaultLevel(levels []string) string {
	if slices.Contains(levels, "medium") {
		return "medium"
	}
	on := slices.DeleteFunc(slices.Clone(levels), func(l string) bool { return l == "disabled" })
	if len(on) == 0 {
		return levels[0]
	}
	return on[len(on)/2]
}

// zcodeRuled reports whether provider_config.json has magpie's provider.
func zcodeRuled(path string) bool {
	var c struct {
		Config struct {
			ProviderConfigRules struct {
				ProviderRules []struct {
					ProviderID string `json:"providerId"`
				} `json:"providerRules"`
			} `json:"providerConfigRules"`
		} `json:"config"`
	}
	b, _ := os.ReadFile(path)
	if json.Unmarshal(b, &c) != nil {
		return false
	}
	for _, r := range c.Config.ProviderConfigRules.ProviderRules {
		if r.ProviderID == magpieID {
			return true
		}
	}
	return false
}

// zcodeRules puts magpie's provider and its models' rules into ZCode's
// provider_config.json (on) or takes them out, leaving every other rule and
// key as ZCode wrote it.
func zcodeRules(path string, on bool) error {
	b, err := edit.Read(path)
	if err != nil {
		return err
	}
	if b == nil && !on {
		return nil
	}
	doc := map[string]any{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			return err
		}
	}
	if doc["schemaVersion"] == nil {
		doc["schemaVersion"] = 1
	}
	cfg := zcodeObj(doc, "config")
	pcr := zcodeObj(cfg, "providerConfigRules")
	mcr := zcodeObj(cfg, "modelConfigRules")
	mine := func(r any) bool { m, _ := r.(map[string]any); return m != nil && m["providerId"] == magpieID }

	var old map[string]any
	var providers []any
	at := -1 // where magpie's was, which it keeps
	for _, r := range zcodeList(pcr, "providerRules") {
		if mine(r) {
			old, _ = r.(map[string]any)
			at = len(providers)
			continue
		}
		providers = append(providers, r)
	}
	var models, manual []any
	byHand := map[any]bool{}
	for _, r := range zcodeList(mcr, "manualProviderModelRules") {
		if mine(r) {
			if !on {
				continue
			}
			byHand[r.(map[string]any)["modelId"]] = true
		}
		manual = append(manual, r)
	}
	for _, r := range zcodeList(mcr, "providerModelRules") {
		if !mine(r) {
			models = append(models, r)
		}
	}

	if on {
		var ids []string
		for _, m := range magpieModels("zcode") {
			ids = append(ids, m.ID)
			if byHand[m.ID] {
				continue
			}
			props := map[string]any{"inputFormat": map[string]any{"supportsImage": m.Images}}
			if m.Context > 0 {
				props["contextWindow"] = m.Context
			}
			config := map[string]any{"properties": props}
			// ZCode's own rules give a model it doesn't know 32000 tokens
			// out and thinking only on or off
			specs := map[string]any{}
			if out := zcodeOutput(maxTokens(m)); out > 0 {
				specs["maxOutputTokens"] = map[string]any{"max": out}
			}
			if levels := zcodeLevels(m.Efforts); levels != nil {
				specs["reasoningLevel"] = map[string]any{"values": levels}
			}
			if len(specs) > 0 {
				config["optionSpecs"] = specs
			}
			models = append(models, map[string]any{"providerId": magpieID, "modelId": m.ID, "config": config})
		}
		if ids == nil {
			ids = []string{}
		}
		rule := map[string]any{"providerId": magpieID, "providerName": "magpie", "enabled": true,
			"config": map[string]any{
				"group":            "standard-personal",
				"access":           map[string]any{"type": "api-key", "apiKey": gateway.Token},
				"api":              map[string]any{"type": "anthropic-messages", "baseUrl": gateway.URL()},
				"personalModelIds": ids, "modelOrder": ids,
			}}
		// turned off in ZCode, it stays off
		if e, ok := old["enabled"].(bool); ok {
			rule["enabled"] = e
		}
		if at >= 0 {
			providers = slices.Insert(providers, at, any(rule))
		} else {
			providers = append(providers, rule)
		}
	}
	pcr["providerRules"] = zcodeNonNil(providers)
	mcr["providerModelRules"] = zcodeNonNil(models)
	mcr["manualProviderModelRules"] = zcodeNonNil(manual)

	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if string(out) == string(b) {
		return nil
	}
	if b == nil {
		// ZCode keeps it private
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return err
		}
	}
	return edit.WriteAtomic(path, out)
}

func zcodeObj(m map[string]any, k string) map[string]any {
	o, ok := m[k].(map[string]any)
	if !ok {
		o = map[string]any{}
		m[k] = o
	}
	return o
}

func zcodeList(m map[string]any, k string) []any { l, _ := m[k].([]any); return l }

func zcodeNonNil(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}
