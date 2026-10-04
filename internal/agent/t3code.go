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
// same Claude Code. T3's Codex lists what Codex's model/list says, which is
// magpie's catalog once Codex is routed through magpie, so it needs nothing.
//
// The settings environment (~/.claude/settings.json's env) is applied over
// the process', so a Claude Code routed elsewhere in its own settings
// takes its requests there; one routed through magpie or not routed at all
// asks the gateway.

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// t3Instance is the key of magpie's provider instance in T3 Code's
// settings.json.
const t3Instance = "providerInstances." + magpieID

func t3code(home string) *Agent {
	base := appdir.Getenv("T3CODE_HOME")
	if base == "" {
		base = filepath.Join(home, ".t3")
	} else if rest, ok := strings.CutPrefix(base, "~"); ok {
		base = filepath.Join(home, rest)
	}
	path := filepath.Join(base, "userdata", "settings.json")
	wired := func() bool { _, ok := edit.GetJSON(path, t3Instance); return ok }
	return &Agent{
		ID: "t3code", Name: "T3 Code", Icon: "t3code", Aliases: []string{"t3", "t3-code"},
		Dir: base, Path: path,
		// ~/.t3 is made at its first start; a Mac app never opened yet is
		// found by its bundle. No command: `t3` is a common name.
		detect: func() bool { return isDir(base) || t3App(home) },
		Sync: func() error {
			return syncJSON(path, t3Instance, func() any { return t3InstanceJSON(path, gateway.URL()) })
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
					return edit.DelJSON(path, t3Instance)
				}
				return edit.SetJSON(path, edit.KV{Path: t3Instance, Value: t3InstanceJSON(path, gateway.URL())})
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model as a provider in T3 Code, on Claude Code"}}
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
	for _, d := range []string{"/Applications", filepath.Join(home, "Applications")} {
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
	for _, m := range magpieModels("t3code") {
		e := map[string]any{"slug": mark(m.ID), "name": m.Name}
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
	return map[string]any{
		"driver": "claudeAgent", "displayName": "magpie", "enabled": true,
		"environment": []map[string]any{
			{"name": "ANTHROPIC_BASE_URL", "value": gw, "sensitive": false},
			{"name": "ANTHROPIC_AUTH_TOKEN", "value": gateway.Token, "sensitive": false},
		},
		"config": config,
	}
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
