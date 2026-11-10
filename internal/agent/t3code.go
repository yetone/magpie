package agent

// T3 Code (pingdotgg/t3code, a GUI that drives Claude Code, Codex, Cursor
// and others; KevinXC on Discord) keeps its settings in
// ~/.t3/userdata/settings.json ($T3CODE_HOME/userdata), a sparse JSON file
// its server watches and reloads on an outside edit. Each provider it lists
// is an instance of a driver, and besides the built-in ones (keyed by the
// driver, "claudeAgent", "codex", …) it takes instances of one's own:
//
//	{"providerInstances":{"<id>":{"driver":"claudeAgent","displayName":…,
//	  "enabled":true,"environment":[{"name":…,"value":…,"sensitive":false}],
//	  "config":{"customModels":[{"slug":…,"name":…,"capabilities":
//	    {"optionDescriptors":[{"id":"effort","type":"select",…}]}}]}}}}
//
// A claudeAgent instance runs Claude Code (Anthropic's Agent SDK, the
// user's ~/.claude settings read as Claude Code reads them) with the
// instance's environment added, and lists Claude's models and its custom
// models, each sent as Claude Code's model. So magpie is an instance of its
// own, "magpie": Claude Code pointed at the gateway (ANTHROPIC_BASE_URL and
// the token Claude Code is routed with, so its requests are Claude Code's
// to the gateway, tiers and all), every magpie model one of its custom
// models, a 1M one marked [1m] as magpie marks it for Claude Code, with
// a Reasoning pick of the levels it takes and a Thinking switch
// (t3Capabilities). It is
// magpie's alone: the user's own instances, their custom models and T3's
// other keys stay as they are, and off takes only it out. A binary path or
// Claude home the user gave T3's own Claude is carried over, so it runs the
// same Claude Code.
//
// T3 Code runs each thread on the harness of the instance its model is
// picked from, so with magpie on Claude Code alone every magpie model ran
// there (karissasel_ on X: "can't pick the harness, it's Claude Code").
// So magpie has a Codex instance too, "magpie-codex" (magpie · Codex): a
// codex instance whose launchArgs, which T3 passes to `codex app-server`
// and `codex exec` (codexLaunchArgs.ts), are Codex's own -c overrides
// making magpie its model provider and magpie's model catalog for T3
// (magpie-codex-models.json beside settings.json) its catalog, so T3's
// picker lists every magpie model under it from Codex's model/list. The
// user's ~/.codex config.toml isn't written: the overrides are this
// instance's alone, and T3's own Codex stays as it is. It is written with
// magpie's instance; one the user removed in T3 isn't brought back by a
// sync (t3CodexOffered), only by turning magpie on again. Its name, whether
// it is on, and the keys magpie doesn't write are the user's; the launch
// arguments and the catalog are magpie's.
//
// The settings environment (~/.claude/settings.json's env) is applied over
// the process', so a Claude Code routed elsewhere in its own settings
// takes its requests there; one routed through magpie or not routed at all
// asks the gateway. A provider switch on there (CLAUDE_CODE_USE_BEDROCK, …)
// takes it to that cloud, which Check says.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// t3Instance is the key of magpie's provider instance in T3 Code's
// settings.json, and t3CodexInstance that of its Codex one.
const (
	t3Instance      = "providerInstances." + magpieID
	t3CodexInstance = "providerInstances." + magpieID + "-codex"
)

// t3CodexOffered is the stash key that says magpie has written its Codex
// instance into T3 Code's settings: one gone after that the user removed in
// T3, and a sync doesn't bring it back.
const t3CodexOffered = "t3code.codex"

func t3code(home string) *Agent {
	base := appdir.Getenv("T3CODE_HOME")
	if base == "" {
		base = filepath.Join(home, ".t3")
	} else if rest, ok := strings.CutPrefix(base, "~"); ok {
		base = filepath.Join(home, rest)
	}
	path := filepath.Join(base, "userdata", "settings.json")
	claudeOn := func() bool { _, ok := edit.GetJSON(path, t3Instance); return ok }
	// either of magpie's instances: one the user removed in T3 leaves
	// magpie on in the other
	wired := func() bool {
		_, ok := edit.GetJSON(path, t3CodexInstance)
		return ok || claudeOn()
	}
	return &Agent{
		ID: "t3code", Name: "T3 Code", Icon: "t3code", Aliases: []string{"t3", "t3-code"},
		Dir: base, Path: path,
		// ~/.t3 is made at its first start; a Mac app never opened yet is
		// found by its bundle. No command: `t3` is a common name.
		detect:   func() bool { return isDir(base) || t3App(home) },
		ListOnly: true,
		Sync: func() error {
			if err := syncJSON(path, t3Instance, func() any { return t3Merged(path, gateway.URL()) }); err != nil {
				return err
			}
			return t3CodexSync(path, gateway.URL())
		},
		// its custom models are the models picked for it (#1508)
		ListsModels: true,
		// magpie's instance still on the gateway, as for every agent with
		// a Check (TestDriftUnwiredEveryAgent, TestPortMovesEveryAgent).
		// The user's ~/.claude settings env is put over the instance's, so
		// a provider switch on there (or in the shell T3 was started from)
		// takes magpie's instance to that cloud too; magpie only says so,
		// as that file is Claude Code's, wired or not
		Check: func() string {
			if !wired() {
				return ""
			}
			if why := t3CodexOff(path, gateway.URL()); why != "" {
				return why
			}
			if !claudeOn() {
				return ""
			}
			if why := wiringOff("T3 Code", path, func(k string) (string, bool) { return t3EnvOf(path, k) }, "ANTHROPIC_BASE_URL", gateway.URL()); why != "" {
				return why
			}
			at := here(home)
			return claudeCloud(at, filepath.Join(at.claudeDir(), "settings.json"))
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
				if v == "" {
					if err := edit.DelJSON(path, t3CodexInstance); err != nil {
						return err
					}
					os.Remove(t3CodexCatalog(path))
					forget(t3CodexOffered)
					return edit.DelJSON(path, t3Instance)
				}
				if err := edit.SetJSON(path, edit.KV{Path: t3Instance, Value: t3Merged(path, gateway.URL())}); err != nil {
					return err
				}
				return t3CodexPut(path, gateway.URL())
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in T3 Code twice: magpie on Claude Code, magpie · Codex on Codex; pick one in T3's model picker"}}
			},
		}},
	}
}

// t3App reports whether T3 Code's app is in a Mac's Applications, by the
// names its builds take.
func t3App(home string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	for _, d := range macAppFolders(home) {
		for _, n := range []string{"T3 Code (Alpha).app", "T3 Code.app"} {
			if _, err := os.Stat(filepath.Join(d, n)); err == nil {
				return true
			}
		}
	}
	return false
}

// t3InstanceJSON is magpie's provider instance in T3 Code's settings.json
// at path: a claudeAgent instance named magpie on the gateway at gw, with
// every magpie model as a custom model, and the binary path and Claude
// home of T3's own Claude when the user gave them one.
func t3InstanceJSON(path, gw string) map[string]any {
	mark := claude1MFor("t3code")
	models := []map[string]any{}
	for _, m := range sharedModels("t3code") {
		// Claude Code reads a dotted Claude version as Claude Opus 4
		e := map[string]any{"slug": provider.ClaudeSpelled(mark(m.ID)), "name": m.Name}
		if c := t3Capabilities(m.ID, m.Efforts); c != nil {
			e["capabilities"] = c
		}
		models = append(models, e)
	}
	config := map[string]any{"customModels": models}
	for _, k := range []string{"binaryPath", "homePath"} {
		// the user's Claude instance, else the setting it was before T3
		// had instances
		v, _ := edit.GetJSON(path, "providerInstances.claudeAgent.config."+k)
		if v == "" {
			v, _ = edit.GetJSON(path, "providers.claudeAgent."+k)
		}
		if v = strings.TrimSpace(v); v != "" {
			config[k] = v
		}
	}
	environment := []map[string]any{
		{"name": "ANTHROPIC_BASE_URL", "value": gw, "sensitive": false},
		{"name": "ANTHROPIC_AUTH_TOKEN", "value": keyAt(gw), "sensitive": false},
	}
	// auto mode's review off unless every model it lists is one whose
	// review comes back, as for Claude Code itself (claudeReviewEnv); a value in the
	// user's ~/.claude settings comes after it
	if !claudeReviewed([]string{"t3code"}) {
		environment = append(environment, map[string]any{"name": claudeReviewEnv, "value": "0", "sensitive": false})
	}
	// tool search on, as for Claude Code itself (claudeSearchEnv): the
	// Claude Code T3 runs may be one from before 2.1.304, which sends every
	// MCP tool's definition on each request behind magpie
	environment = append(environment, map[string]any{"name": claudeSearchEnv, "value": "true", "sensitive": false})
	return map[string]any{
		"driver": "claudeAgent", "displayName": "magpie", "enabled": true,
		"environment": environment,
		"config":      config,
	}
}

// t3Env is the environment variables of magpie's instance that magpie
// sets: the gateway's address and key, and auto mode's review. Tool search
// (claudeSearchEnv) is magpie's only until the user gives it a value there.
var t3Env = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", claudeReviewEnv}

// t3EnvOf is the value of the environment variable named k on magpie's
// instance in the T3 settings at path.
func t3EnvOf(path, k string) (string, bool) {
	cur, ok := edit.GetJSON(path, t3Instance+".environment")
	if !ok {
		return "", false
	}
	var env []map[string]any
	if json.Unmarshal([]byte(cur), &env) != nil {
		return "", false
	}
	for _, v := range env {
		if n, _ := v["name"].(string); n == k {
			s, _ := v["value"].(string)
			return s, true
		}
	}
	return "", false
}

// t3Merged is magpie's instance as t3InstanceJSON writes it, merged into
// the one at path: what the user set on it in T3 Code's own settings
// stays (#2030, Kanscape: its name and Auto-compact after were reset on
// every start). Its name, whether it is on, and every key magpie doesn't
// write (config.autoCompactWindow, config.launchArgs, accentColor) are
// theirs, as are the environment variables besides magpie's own. The
// custom models and the gateway's variables are magpie's, and so is a
// binary path or Claude home while T3's own Claude has one to carry over.
// Only magpie's instance is read: another, such as T3's own Claude, lends
// it nothing of the user's.
func t3Merged(path, gw string) any {
	want := t3InstanceJSON(path, gw)
	cur, ok := edit.GetJSON(path, t3Instance)
	if !ok {
		return want
	}
	var have map[string]json.RawMessage
	if json.Unmarshal([]byte(cur), &have) != nil || have == nil {
		return want
	}
	var name string
	if json.Unmarshal(have["displayName"], &name) == nil && strings.TrimSpace(name) != "" {
		want["displayName"] = name
	}
	var on bool
	if json.Unmarshal(have["enabled"], &on) == nil {
		want["enabled"] = on
	}
	var theirs []map[string]any
	if json.Unmarshal(have["environment"], &theirs) == nil {
		env := want["environment"].([]map[string]any)
		for _, v := range theirs {
			if n, _ := v["name"].(string); n == claudeSearchEnv {
				env = slices.DeleteFunc(env, func(m map[string]any) bool { return m["name"] == claudeSearchEnv })
			}
			// a provider switch would take magpie's instance past the
			// gateway (claudeClouds), so it doesn't stay on magpie's
			if n, _ := v["name"].(string); n != "" && !slices.Contains(t3Env, n) && !slices.Contains(agentenv.ClaudeProviders, n) {
				env = append(env, v)
			}
		}
		want["environment"] = env
	}
	b, err := json.Marshal(want)
	var w map[string]json.RawMessage
	if err != nil || json.Unmarshal(b, &w) != nil {
		return want
	}
	return mergeTheirs(have, w, []string{"environment"})
}

// t3EffortLabels are T3 Code's names for Claude Code's levels, as its own
// Claude models show them.
var t3EffortLabels = map[string]string{"low": "Low", "medium": "Medium", "high": "High", "xhigh": "Extra High", "max": "Max"}

// t3Capabilities is the picks T3 Code shows beside a custom model: without
// them it shows none and sends no effort, so every magpie model ran at
// whatever Claude Code took (KevinXC on Discord). Reasoning is the levels
// of Claude Code's the model takes, medium chosen first as on T3's own
// Claude models (else the lowest); T3 hands the level to Claude Code as it
// is, which sends it to the gateway. Thinking, on at first, is the switch
// T3's own Claude Haiku has, for a model that thinks at some level: T3 sets
// Claude Code's alwaysThinkingEnabled by it, and Claude Code's requests with
// thinking off ask the gateway with none. nil for a model with neither.
func t3Capabilities(id string, efforts []string) map[string]any {
	var descriptors []map[string]any
	var options []map[string]any
	for _, l := range claudeEffortsFor(id) {
		if contains(efforts, l) {
			options = append(options, map[string]any{"id": l, "label": t3EffortLabels[l]})
		}
	}
	if len(options) > 0 {
		def := options[0]
		for _, o := range options {
			if o["id"] == "medium" {
				def = o
			}
		}
		def["isDefault"] = true
		descriptors = append(descriptors, map[string]any{"id": "effort", "label": "Reasoning", "type": "select", "options": options})
	}
	if slices.ContainsFunc(efforts, func(l string) bool { return l != "none" }) {
		descriptors = append(descriptors, map[string]any{"id": "thinking", "label": "Thinking", "type": "boolean", "currentValue": true})
	}
	if len(descriptors) == 0 {
		return nil
	}
	return map[string]any{"optionDescriptors": descriptors}
}

// t3CodexCatalog is the model catalog of magpie's Codex instance in T3
// Code, beside its settings.json at path.
func t3CodexCatalog(path string) string {
	return filepath.Join(filepath.Dir(path), "magpie-codex-models.json")
}

// t3TOML is s as a TOML basic string: JSON's quoting, which TOML reads
// alike, with nothing escaped for HTML.
func t3TOML(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// t3Arg is Codex's -c override of key to the TOML value v, quoted as T3
// Code's tokenizeCliArgs reads a double-quoted argument: \ before ", \,
// $ and `, so it comes to Codex as written.
func t3Arg(key, v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	return `-c "` + r.Replace(key+"="+v) + `"`
}

// t3CodexBase is the override of the base URL magpie's Codex instance
// reaches the gateway at gw by.
func t3CodexBase(gw string) string {
	return t3Arg("model_providers."+magpieID+".base_url", t3TOML(gw+"/v1"))
}

// t3CodexArgs is the launch arguments of magpie's Codex instance: magpie
// as Codex's provider, as Codex's own [model_providers.magpie] is when
// magpie is its provider (codex.go's putProvider), and its catalog.
func t3CodexArgs(path, gw string) string {
	p := "model_providers." + magpieID + "."
	return strings.Join([]string{
		t3Arg("model_provider", t3TOML(magpieID)),
		t3Arg(p+"name", t3TOML("magpie")),
		t3CodexBase(gw),
		t3Arg(p+"wire_api", t3TOML("responses")),
		t3Arg(p+"experimental_bearer_token", t3TOML(keyAt(gw))),
		// the image tool, as on Codex's own table (putProvider)
		t3Arg(p+"http_headers", `{"x-openai-actor-authorization"="magpie"}`),
		t3Arg("model_catalog_json", t3TOML(t3CodexCatalog(path))),
	}, " ")
}

// t3CodexJSON is magpie's Codex instance in T3 Code's settings.json at
// path, with the binary path and Codex home of T3's own Codex when the
// user gave them one.
func t3CodexJSON(path, gw string) map[string]any {
	config := map[string]any{"launchArgs": t3CodexArgs(path, gw)}
	for _, k := range []string{"binaryPath", "homePath"} {
		v, _ := edit.GetJSON(path, "providerInstances.codex.config."+k)
		if v == "" {
			v, _ = edit.GetJSON(path, "providers.codex."+k)
		}
		if v = strings.TrimSpace(v); v != "" {
			config[k] = v
		}
	}
	return map[string]any{"driver": "codex", "displayName": "magpie · Codex", "enabled": true, "config": config}
}

// t3CodexMerged is t3CodexJSON merged into the instance at path: the
// user's name, whether it is on, and keys magpie doesn't write stay.
func t3CodexMerged(path, gw string) any {
	want := t3CodexJSON(path, gw)
	cur, ok := edit.GetJSON(path, t3CodexInstance)
	if !ok {
		return want
	}
	var have map[string]json.RawMessage
	if json.Unmarshal([]byte(cur), &have) != nil || have == nil {
		return want
	}
	var name string
	if json.Unmarshal(have["displayName"], &name) == nil && strings.TrimSpace(name) != "" {
		want["displayName"] = name
	}
	var on bool
	if json.Unmarshal(have["enabled"], &on) == nil {
		want["enabled"] = on
	}
	b, err := json.Marshal(want)
	var w map[string]json.RawMessage
	if err != nil || json.Unmarshal(b, &w) != nil {
		return want
	}
	return mergeTheirs(have, w, nil)
}

// t3CodexPut writes magpie's Codex instance and its catalog into T3
// Code's settings at path.
func t3CodexPut(path, gw string) error {
	if err := t3CodexCatalogPut(path); err != nil {
		return err
	}
	if err := edit.SetJSON(path, edit.KV{Path: t3CodexInstance, Value: t3CodexMerged(path, gw)}); err != nil {
		return err
	}
	stash(map[string]string{t3CodexOffered: "1"})
	return nil
}

// t3CodexCatalogPut writes the models picked for T3 Code as Codex's
// catalog for magpie's Codex instance, when it changed.
func t3CodexCatalogPut(path string) error {
	cat := t3CodexCatalog(path)
	b := codexcat.CatalogAt(magpieModels("t3code"), "")
	if cur, err := os.ReadFile(cat); err == nil && bytes.Equal(cur, b) {
		return nil
	}
	return edit.WriteAtomic(cat, b)
}

// t3CodexSync brings magpie's Codex instance up to the catalog and the
// gateway, and writes it beside magpie's Claude Code instance an older
// magpie wrote alone. One the user removed stays removed.
func t3CodexSync(path, gw string) error {
	if _, ok := edit.GetJSON(path, t3CodexInstance); !ok {
		if _, mine := edit.GetJSON(path, t3Instance); !mine || t3Stashed(t3CodexOffered) {
			return nil
		}
		return t3CodexPut(path, gw)
	}
	if err := t3CodexCatalogPut(path); err != nil {
		return err
	}
	return syncJSON(path, t3CodexInstance, func() any { return t3CodexMerged(path, gw) })
}

// t3CodexOff says when magpie's Codex instance no longer reaches the
// gateway at gw: its launch arguments lost magpie's base URL, or name
// another address that isn't a way to magpie (reachesMagpie: a proxy the
// user put in front of it is kept, as for every agent).
func t3CodexOff(path, gw string) string {
	if _, there := edit.GetJSON(path, t3CodexInstance); !there {
		return ""
	}
	args, _ := edit.GetJSON(path, t3CodexInstance+".config.launchArgs")
	want := gw + "/v1"
	if v := t3CodexBaseOf(args); v == want || v != "" && reachesMagpie(v, want) {
		return ""
	}
	return "T3 Code's magpie · Codex launch arguments (" + filepath.Base(path) + ") no longer reach magpie's gateway at " + gw
}

// t3CodexBaseOf is the base URL the -c overrides in launchArgs give
// magpie's Codex provider, as T3 tokenizes them; "" when none does.
func t3CodexBaseOf(args string) string {
	toks := t3Tokens(args)
	base := ""
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] != "-c" && toks[i] != "--config" {
			continue
		}
		k, v, ok := strings.Cut(toks[i+1], "=")
		i++
		if !ok || strings.TrimSpace(k) != "model_providers."+magpieID+".base_url" {
			continue
		}
		// a TOML basic string, as t3TOML writes it; the last one wins
		var s string
		if json.Unmarshal([]byte(strings.TrimSpace(v)), &s) == nil {
			base = s
		} else {
			base = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return base
}

// t3Stashed reports whether the stash holds key.
func t3Stashed(key string) bool {
	stashMu.Lock()
	defer stashMu.Unlock()
	return stashShared()[key] != ""
}

// t3Tokens is T3 Code's tokenizeCliArgs (packages/shared/src/cliArgs.ts),
// which splits an instance's launchArgs into the arguments it passes to
// `codex app-server`.
func t3Tokens(s string) []string {
	in := []rune(strings.TrimSpace(s))
	var out []string
	cur, quote, quoted := "", rune(0), false
	space := func(r rune) bool { return strings.ContainsRune(" \t\n\r\f\v", r) }
	for i := 0; i < len(in); i++ {
		c := in[i]
		if quote != 0 {
			switch {
			case c == quote:
				quote, quoted = 0, true
			case c == '\\' && quote == '"' && i+1 < len(in) && strings.ContainsRune("\"\\$`", in[i+1]):
				cur += string(in[i+1])
				i++
			default:
				cur += string(c)
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			quote, quoted = c, true
		case space(c):
			if cur != "" || quoted {
				out = append(out, cur)
				cur, quoted = "", false
			}
		case c == '\\' && i+1 < len(in) && (space(in[i+1]) || in[i+1] == '\'' || in[i+1] == '"'):
			cur += string(in[i+1])
			i++
		default:
			cur += string(c)
		}
	}
	if cur != "" || quoted {
		out = append(out, cur)
	}
	return out
}
