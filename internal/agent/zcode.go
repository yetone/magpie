package agent

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// ZCode (Zhipu's desktop app) keeps its model providers in
// ~/.zcode/v2/config.json (under the folder the user moved its data to, when
// they did: appdir.ZCodeDataIn), OpenCode's provider shape with a kind of its own:
//
//	{"provider":{"magpie":{"name":"magpie","kind":"anthropic",
//	  "options":{"apiKey":"magpie","baseURL":"http://127.0.0.1:3425"},
//	  "enabled":true,"source":"custom",
//	  "models":{"<id>":{"name":…,"limit":{"context":…,"output":…},"modalities":{…},
//	    "reasoning":{"enabled":true,"levels":[…],"defaultLevel":…,
//	      "providerOptionsByLevel":{"<level>":{"thinking":{"type":"enabled","budget_tokens":…}}}}}}}}}
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
//	      "inputFormat":{"supportsImage":…},"supportsNativeWebSearch":true},
//	      "optionSpecs":{"maxOutputTokens":{"max":…},"reasoningLevel":{"values":[…]}}}}],
//	    "manualProviderModelRules":[…]}}}
//
// magpie writes both files, so an older ZCode sees its models too. A model
// the user set by hand in ZCode (a manual rule) keeps what they set.
//
// magpie's gateway answers all three of ZCode's API formats at the same
// address (/v1/messages, /chat/completions, /responses), so the format the
// user picked in ZCode's provider settings is theirs: a sync keeps it in both
// files (zcodeFormat), and only a provider magpie adds new starts on
// Anthropic Messages.

// zcodeOS is the OS ZCode's home is found for, so a test can ask for
// Windows' (appdir.ZCodeHomeOn).
var zcodeOS = runtime.GOOS

func zcode(home string) *Agent {
	// where ZCode keeps its data, moved there in its settings or not, under
	// the home ZCode reads (HOME first, on Windows too)
	dir := filepath.Join(appdir.ZCodeDataIn(appdir.ZCodeHomeOn(zcodeOS, home)), ".zcode")
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
		// its picker lists the models picked for it, in both files (#1508)
		ListsModels: true,
		Notice: func() string {
			if Running(`ZCode\.app/`, `(^|/)ZCode( |$)`) {
				return noticeZcode.String()
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
				if err := zcodeRules(rules, true, true); err != nil {
					return err
				}
			}
			return edit.SetJSON(path, edit.KV{Path: key, Value: zcodeProviderJSON(path, true)})
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
				if err := zcodeRules(rules, v != "", false); err != nil {
					return err
				}
				if v == "" {
					if _, ok := stashShared()[zcodeSearchedKey]; ok {
						forget(zcodeSearchedKey)
					}
					return edit.DelJSON(path, key)
				}
				return edit.SetJSON(path, edit.KV{Path: key, Value: zcodeProviderJSON(path, false)})
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in ZCode's picker"}}
			},
		}},
	}
}

// zcodeProviderJSON is magpie's provider in config.json at path; one
// turned off in ZCode stays off. A sync (keep) leaves an address on another
// machine as it found it (zcodeAddress).
func zcodeProviderJSON(path string, keep bool) any {
	was, own := zcodeDefaultLevels(path), zcodeLevelOptions(path)
	ms := map[string]any{}
	format := zcodeFormat(filepath.Dir(path))
	for _, m := range sharedModels("zcode") {
		window := m.Context
		if window == 0 {
			window = unknownWindow(200000)
		}
		in := zcodeInputs(m, format)
		limit := map[string]any{"context": window}
		// without it ZCode caps every reply at 32000 tokens
		if out := outputOr(m, zcodeOutput(maxTokens(m))); out > 0 {
			limit["output"] = out
		}
		e := map[string]any{"name": m.Name, "limit": limit,
			"modalities": map[string]any{"input": in, "output": []string{"text"}}}
		if levels := zcodeLevels(m.Efforts); levels != nil {
			e["reasoning"] = map[string]any{"enabled": true, "levels": levels,
				"defaultLevel":           keptEffort(was[m.ID], levels, zcodeDefaultLevel(levels)),
				"providerOptionsByLevel": zcodeOptionsByLevel(levels, own[m.ID])}
		}
		ms[m.ID] = e
	}
	zcodeOutputsKept(path, ms)
	on := true
	if v, ok := edit.GetJSON(path, "provider."+magpieID+".enabled"); ok && v == "false" {
		on = false
	}
	base, token := gateway.URL(), hereKey()
	if keep {
		base, token = zcodeKept(filepath.Dir(path))
	}
	return map[string]any{"name": "magpie", "kind": zcodeKinds[format], "enabled": on, "source": "custom",
		"options": map[string]any{"apiKey": token, "baseURL": base}, "models": ms}
}

// zcodeInputs are the kinds ZCode is told model m takes on format: text,
// an image when it takes one, and a video when its source lists one and
// format is Messages or Chat. ZCode attaches a video only to a model whose
// rule says supportsVideo (its own built-in rules say it per model), and
// sends it as a Messages video block or a Chat video_url, both of which the
// gateway carries on (video_input.go); on Responses ZCode refuses a video
// before asking. A PDF is not offered: a translated request still loses one
// (#5452). Flandern1211 on X, #5098.
func zcodeInputs(m catalog.Model, format string) []string {
	in := []string{"text"}
	if m.Images {
		in = append(in, "image")
	}
	if (format == "anthropic-messages" || format == "openai-chat-completions") && slices.Contains(m.InputModalities, "video") {
		in = append(in, "video")
	}
	return in
}

// zcodeOutputsKept keeps a limit.output the user typed into one of
// magpie's models in config.json at path, in ms, the models magpie writes
// (outputsKept).
func zcodeOutputsKept(path string, ms map[string]any) {
	want := map[string]int{}
	for id, e := range ms {
		m, _ := e.(map[string]any)
		l, _ := m["limit"].(map[string]any)
		if n := numberOf(l["output"]); n > 0 {
			want[id] = n
		}
	}
	if len(want) == 0 {
		return
	}
	have := map[string]int{}
	if raw, ok := edit.GetJSON(path, "provider."+magpieID+".models"); ok {
		var cur map[string]struct {
			Limit struct {
				Output int `json:"output"`
			} `json:"limit"`
		}
		if json.Unmarshal([]byte(raw), &cur) == nil {
			for id, c := range cur {
				have[id] = c.Limit.Output
			}
		}
	}
	kept := outputsKept(path, want, have)
	for id, e := range ms {
		m, _ := e.(map[string]any)
		if l, _ := m["limit"].(map[string]any); l != nil && kept[id] > 0 {
			l["output"] = kept[id]
		}
	}
}

// zcodeDefaultLevels is the default level each of magpie's models has in
// config.json at path, which one set by hand keeps (keptEffort). A default
// a magpie before #3852 wrote as defaultVariant is carried over: ZCode never
// read that key, only magpie wrote it.
func zcodeDefaultLevels(path string) map[string]string {
	var c struct {
		Provider map[string]struct {
			Models map[string]struct {
				Reasoning struct {
					DefaultLevel   string `json:"defaultLevel"`
					DefaultVariant string `json:"defaultVariant"`
				} `json:"reasoning"`
			} `json:"models"`
		} `json:"provider"`
	}
	b, _ := edit.Read(path)
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	out := map[string]string{}
	for id, m := range c.Provider[magpieID].Models {
		out[id] = m.Reasoning.DefaultLevel
		if out[id] == "" {
			out[id] = m.Reasoning.DefaultVariant
		}
	}
	return out
}

// zcodeAddress is the address and key magpie's provider is synced with:
// the gateway's, unless the user pointed the provider at a magpie on another
// machine (a NAS's), which a sync, there to bring the models up to date,
// keeps with its key. An address on this machine's loopback is magpie's own
// and follows the gateway.
func zcodeAddress(was, wasKey string) (string, string) {
	if !onAnotherMachine(was) || sameGateway(was, gateway.URL()) {
		return gateway.URL(), hereKey()
	}
	if wasKey == "" {
		wasKey = gateway.Token
	}
	return was, wasKey
}

// zcodeKept is the address a sync gives magpie's provider in both of
// ZCode's files in dir: one on another machine found in either file, so an
// edit ZCode made to one of them is carried to the other (ZCode's own
// settings write provider_config.json, and a config.json left on this
// machine's magpie took the provider back there), else the gateway's.
func zcodeKept(dir string) (string, string) {
	var r struct {
		Config struct {
			ProviderConfigRules struct {
				ProviderRules []struct {
					ProviderID string `json:"providerId"`
					Config     struct {
						Access struct {
							APIKey string `json:"apiKey"`
						} `json:"access"`
						API struct {
							BaseURL string `json:"baseUrl"`
						} `json:"api"`
					} `json:"config"`
				} `json:"providerRules"`
			} `json:"providerConfigRules"`
		} `json:"config"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "provider_config.json"))
	json.Unmarshal(b, &r)
	for _, p := range r.Config.ProviderConfigRules.ProviderRules {
		if p.ProviderID == magpieID && onAnotherMachine(p.Config.API.BaseURL) {
			return zcodeAddress(p.Config.API.BaseURL, p.Config.Access.APIKey)
		}
	}
	path := filepath.Join(dir, "config.json")
	was, _ := edit.GetJSON(path, "provider."+magpieID+".options.baseURL")
	wasKey, _ := edit.GetJSON(path, "provider."+magpieID+".options.apiKey")
	return zcodeAddress(was, wasKey)
}

// zcodeKinds are ZCode's API formats in provider_config.json (api.type) and
// the kind each is in config.json, as ZCode maps one to the other.
var zcodeKinds = map[string]string{
	"anthropic-messages":      "anthropic",
	"openai-chat-completions": "openai-compatible",
	"openai-responses":        "openai",
}

// zcodeFormat is the API format magpie's provider has in ZCode's files in
// dir: the one the user picked in ZCode's settings, else Anthropic Messages.
// magpie writes both files together, so where they differ the user changed
// one of them in ZCode (provider_config.json from 3.14 on, config.json
// before): the one written last has their choice, which is carried to the
// other.
func zcodeFormat(dir string) string {
	rules, kind := zcodeRuleFormat(dir), ""
	path := filepath.Join(dir, "config.json")
	if k, _ := edit.GetJSON(path, "provider."+magpieID+".kind"); k != "" {
		for f, fk := range zcodeKinds {
			if fk == k {
				kind = f
			}
		}
	}
	switch {
	case kind == "" && rules == "":
		return "anthropic-messages"
	case kind == "":
		return rules
	case rules == "" || rules == kind:
		return kind
	}
	a, errA := os.Stat(filepath.Join(dir, "provider_config.json"))
	b, errB := os.Stat(path)
	if errA == nil && errB == nil && b.ModTime().After(a.ModTime()) {
		return kind
	}
	return rules
}

// zcodeRuleFormat is magpie's provider's API format in provider_config.json
// in dir, or "" when it has none ZCode knows.
func zcodeRuleFormat(dir string) string {
	var r struct {
		Config struct {
			ProviderConfigRules struct {
				ProviderRules []struct {
					ProviderID string `json:"providerId"`
					Config     struct {
						API struct {
							Type string `json:"type"`
						} `json:"api"`
					} `json:"config"`
				} `json:"providerRules"`
			} `json:"providerConfigRules"`
		} `json:"config"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "provider_config.json"))
	json.Unmarshal(b, &r)
	for _, p := range r.Config.ProviderConfigRules.ProviderRules {
		if _, ok := zcodeKinds[p.Config.API.Type]; ok && p.ProviderID == magpieID {
			return p.Config.API.Type
		}
	}
	return ""
}

// onAnotherMachine says base is an http(s) address whose host isn't this
// machine's loopback.
func onAnotherMachine(base string) bool {
	if loopbackAuthority(base) {
		return false
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return false
	}
	ip := net.ParseIP(h)
	return ip == nil || !ip.IsLoopback() && !ip.IsUnspecified()
}

// loopbackAuthority is the common case of onAnotherMachine without
// url.Parse: an http(s) base on localhost, 127.0.0.1 or [::1], no user
// info, which a state poll asks of each agent's gateway base several
// hundred times. Anything else is left to url.Parse.
func loopbackAuthority(base string) bool {
	rest, ok := strings.CutPrefix(base, "http://")
	if !ok {
		if rest, ok = strings.CutPrefix(base, "https://"); !ok {
			return false
		}
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	host := rest
	if i := strings.LastIndexByte(rest, ':'); i >= 0 && !strings.HasSuffix(rest, "]") {
		host = rest[:i]
		for _, c := range rest[i+1:] {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	switch host {
	case "localhost", "127.0.0.1", "[::1]":
		return true
	}
	return false
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

// zcodeOptionsByLevel are the provider options ZCode sends for each of a
// model's reasoning levels: thinking off for the level named "disabled",
// on for every other (#3852 — with no provider options for a level ZCode
// asks the model without thinking, and an always-thinking model refuses).
// Thinking on carries the level's budget, as Anthropic's Messages API asks
// of a Claude before 4.6 (budget_tokens is required with "enabled"); the
// gateway makes it adaptive for a later Claude and fits it under the
// reply's limit. A level's options already in config.json are the user's
// and stay: no magpie before #3852 wrote any.
func zcodeOptionsByLevel(levels []string, own map[string]any) map[string]any {
	out := map[string]any{}
	for _, l := range levels {
		if o, ok := own[l]; ok {
			out[l] = o
			continue
		}
		th := map[string]any{"type": "disabled"}
		if l != "disabled" {
			th = map[string]any{"type": "enabled", "budget_tokens": gateway.ThinkingBudget(l)}
		}
		out[l] = map[string]any{"thinking": th}
	}
	return out
}

// zcodeLevelOptions are the providerOptionsByLevel each of magpie's models
// has in config.json at path, which zcodeOptionsByLevel keeps.
func zcodeLevelOptions(path string) map[string]map[string]any {
	var c struct {
		Provider map[string]struct {
			Models map[string]struct {
				Reasoning struct {
					Options map[string]any `json:"providerOptionsByLevel"`
				} `json:"reasoning"`
			} `json:"models"`
		} `json:"provider"`
	}
	b, _ := edit.Read(path)
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	out := map[string]map[string]any{}
	for id, m := range c.Provider[magpieID].Models {
		if len(m.Reasoning.Options) > 0 {
			out[id] = m.Reasoning.Options
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

// zcodePart is what ZCode reads of magpie's provider when it starts: the
// provider in config.json and magpie's rules in provider_config.json, not
// the key (AppBehind).
func zcodePart(path, rules string) string {
	var b strings.Builder
	if raw, ok := edit.GetJSONRaw(path, "provider."+magpieID); ok {
		var prov map[string]any
		if json.Unmarshal([]byte(raw), &prov) == nil {
			if opts, _ := prov["options"].(map[string]any); opts != nil {
				delete(opts, "apiKey")
			}
			if v, err := json.Marshal(prov); err == nil {
				b.Write(v)
			}
		}
	}
	b.WriteByte('\n')
	b.WriteString(zcodeRulesPart(rules))
	return b.String()
}

// zcodeRulesPart is magpie's provider rule and model rules in
// provider_config.json at path, as ZCode reads them at start. A rule of
// another provider is left out, so ZCode's own writes to one are no change
// of magpie's.
func zcodeRulesPart(path string) string {
	var c struct {
		Config struct {
			ProviderConfigRules struct {
				ProviderRules []json.RawMessage `json:"providerRules"`
			} `json:"providerConfigRules"`
			ModelConfigRules struct {
				ProviderModelRules       []json.RawMessage `json:"providerModelRules"`
				ManualProviderModelRules []json.RawMessage `json:"manualProviderModelRules"`
			} `json:"modelConfigRules"`
		} `json:"config"`
	}
	b, _ := os.ReadFile(path)
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	var out strings.Builder
	for _, rs := range [][]json.RawMessage{
		c.Config.ProviderConfigRules.ProviderRules,
		c.Config.ModelConfigRules.ProviderModelRules,
		c.Config.ModelConfigRules.ManualProviderModelRules,
	} {
		for _, r := range rs {
			var m map[string]any
			if json.Unmarshal(r, &m) != nil || m["providerId"] != magpieID {
				continue
			}
			// the key is not what ZCode's picker reads, and a rotation of
			// it is no change of the list
			if cfg, _ := m["config"].(map[string]any); cfg != nil {
				if access, _ := cfg["access"].(map[string]any); access != nil {
					delete(access, "apiKey")
				}
			}
			if v, err := json.Marshal(m); err == nil {
				out.Write(v)
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
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
// key as ZCode wrote it. A sync (keep) leaves an address on another machine
// as it found it (zcodeAddress).
func zcodeRules(path string, on, keep bool) error {
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
	// magpie's model rules as they are now, which ZCode's own settings
	// write a model's edits into while it keeps its recommended config
	ours := map[any]map[string]any{}
	for _, r := range zcodeList(mcr, "providerModelRules") {
		if !mine(r) {
			models = append(models, r)
			continue
		}
		m := r.(map[string]any)
		ours[m["modelId"]] = m
	}

	if on {
		format := zcodeFormat(filepath.Dir(path))
		searched, known := zcodeSearched()
		var ids []string
		// a maxOutputTokens typed into a model's rule in ZCode's settings
		// stays; magpie's is put where the user set none (outputsKept)
		wantOut, haveOut := map[string]int{}, map[string]int{}
		for _, m := range sharedModels("zcode") {
			if n := outputOr(m, zcodeOutput(maxTokens(m))); n > 0 && !byHand[m.ID] {
				wantOut[m.ID] = n
			}
			if r := ours[m.ID]; r != nil {
				c, _ := r["config"].(map[string]any)
				s, _ := c["optionSpecs"].(map[string]any)
				o, _ := s["maxOutputTokens"].(map[string]any)
				haveOut[m.ID] = numberOf(o["max"])
			}
		}
		keptOut := outputsKept(path, wantOut, haveOut)
		for _, m := range sharedModels("zcode") {
			ids = append(ids, m.ID)
			if byHand[m.ID] {
				// set by hand in ZCode: its own settings, and the default
				// again should it go back to its recommended config
				delete(searched, m.ID)
				continue
			}
			inputFormat := map[string]any{"supportsImage": m.Images}
			if slices.Contains(zcodeInputs(m, format), "video") {
				inputFormat["supportsVideo"] = true
			}
			props := map[string]any{"inputFormat": inputFormat}
			if m.Context > 0 {
				props["contextWindow"] = m.Context
			}
			if v, set := zcodeWebSearch(ours[m.ID], m.ID, format == "anthropic-messages" && known, searched); set {
				props["supportsNativeWebSearch"] = v
			}
			config := map[string]any{"properties": props}
			// ZCode's own rules give a model it doesn't know 32000 tokens
			// out and thinking only on or off
			specs := map[string]any{}
			if out := keptOut[m.ID]; out > 0 {
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
		if known {
			zcodeSearchedPut(searched, ids)
		}
		base, token := gateway.URL(), hereKey()
		if keep {
			base, token = zcodeKept(filepath.Dir(path))
		}
		rule := map[string]any{"providerId": magpieID, "providerName": "magpie", "enabled": true,
			"config": map[string]any{
				"group":            "standard-personal",
				"access":           map[string]any{"type": "api-key", "apiKey": token},
				"api":              map[string]any{"type": format, "baseUrl": base},
				"personalModelIds": ids, "modelOrder": ids,
			}}
		// turned off in ZCode, it stays off
		if e, ok := old["enabled"].(bool); ok {
			rule["enabled"] = e
		}
		// What the user set on it in ZCode stays (#2030): its name, logo,
		// visibility, a header or the key's management link
		// (packages/provider/src/config/provider-data-schema.ts). ZCode
		// checks the file strictly, so only keys it already took are kept;
		// the address, key, format and model lists are magpie's.
		if old != nil {
			if name, _ := old["providerName"].(string); strings.TrimSpace(name) != "" {
				rule["providerName"] = name
			}
			if oc, _ := old["config"].(map[string]any); oc != nil {
				rule["config"] = mergeKept(oc, rule["config"].(map[string]any), []string{"group", "personalModelIds", "modelOrder"})
			}
			rule = mergeKept(old, rule, nil)
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

// ZCode offers a model its WebSearch tool only where the model's rule says
// it has native web search (properties.supportsNativeWebSearch, 原生联网搜索
// in a model's settings), off by default. Its tool then asks the model
// again with Anthropic's web_search tool, which only an Anthropic Messages
// provider is sent: on another API format ZCode fails the search. magpie's
// gateway runs that tool for any of its models, so it turns native web
// search on for each model it lists while the format is Anthropic Messages
// (Gearclickist on Discord: every magpie model had to be ticked by hand).
//
// ZCode writes a change to a model with its recommended config into the
// model's rule, magpie's, and leaves a value equal to ZCode's default out of
// it: unticking native web search takes the key out. So the models magpie
// turned it on for are kept in the stash (zcodeSearchedKey), and one of
// them whose rule has no key now was unticked in ZCode and stays off. A
// value in the rule is kept as it is.
const zcodeSearchedKey = "zcode.web_search"

// zcodeWebSearch is what magpie's rule for model id, which says old now,
// says of native web search: v, or nothing when set is false. anthropic is
// whether ZCode sends the model's provider-native search (the provider's
// format is Anthropic Messages, and the stash could be read). searched holds
// the models magpie turned it on for and is updated.
func zcodeWebSearch(old map[string]any, id string, anthropic bool, searched map[string]bool) (v, set bool) {
	props, _ := old["config"].(map[string]any)
	props, _ = props["properties"].(map[string]any)
	was, had := props["supportsNativeWebSearch"].(bool)
	switch {
	case !anthropic:
		// magpie's on can't be used there; the user's own value stays
		if had && was && searched[id] {
			delete(searched, id)
			return false, false
		}
		return was, had
	case had:
		if was {
			searched[id] = true
		}
		return was, true
	case searched[id]:
		// unticked in ZCode
		return false, false
	}
	searched[id] = true
	return true, true
}

// zcodeSearched is the models magpie turned ZCode's native web search on
// for, and whether the stash could be read: a stash there that can't be
// read or parsed is unknown, and nothing is turned on.
func zcodeSearched() (map[string]bool, bool) {
	out := map[string]bool{}
	b, err := os.ReadFile(stashPath())
	if errors.Is(err, fs.ErrNotExist) {
		return out, true
	}
	var m map[string]string
	if err != nil || json.Unmarshal(b, &m) != nil {
		return out, false
	}
	if v := m[zcodeSearchedKey]; v != "" {
		for _, id := range strings.Split(v, "\n") {
			out[id] = true
		}
	}
	return out, true
}

// zcodeSearchedPut keeps searched in the stash, only for models magpie
// still lists (ids), and writes it only when that changes it.
func zcodeSearchedPut(searched map[string]bool, ids []string) {
	var keep []string
	for _, id := range ids {
		if searched[id] {
			keep = append(keep, id)
		}
	}
	v := strings.Join(keep, "\n")
	if stashShared()[zcodeSearchedKey] != v {
		stash(map[string]string{zcodeSearchedKey: v})
	}
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

// what zcode says after a change (notice.go)
var (
	noticeZcode = newNotice("ZCode reads its providers at start-up — restart ZCode to see magpie's models in its picker.")
)
