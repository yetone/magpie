package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

const zedProvider = "language_models.openai_compatible.magpie"
const zedModel = "agent.default_model"

// Zed stores API keys in the OS credential store, keyed by the API URL.
var zedCredential = saveZedCredential

// Zed uses ~/.config on macOS, XDG on Linux and Roaming AppData on Windows.
func zed(home, cfg string) *Agent {
	bin := os.Getenv("MAGPIE_ZED_BIN")
	if bin == "" {
		bin = "zed"
	}
	processes := zedProcessNames()
	if custom := appdir.Getenv("MAGPIE_ZED_CONFIG_DIR"); custom != "" {
		return zedAtWith(custom, bin, processes)
	}
	switch runtime.GOOS {
	case "darwin":
		cfg = filepath.Join(home, ".config")
	case "windows":
		cfg = appdir.Getenv("APPDATA")
		if cfg == "" {
			cfg = filepath.Join(home, "AppData", "Roaming")
		}
		return zedAtWith(filepath.Join(cfg, "Zed"), bin, processes)
	}
	return zedAtWith(filepath.Join(cfg, "zed"), bin, processes)
}

func zedAt(dir string) *Agent {
	return zedAtWith(dir, "zed", zedProcessNames())
}

func zedAtWith(dir, bin string, processes []string) *Agent {
	path := filepath.Join(dir, "settings.json")
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	model := pairGet(func(k string) (string, bool) { return edit.GetJSON(path, k) }, zedModel+".provider", zedModel+".model")
	key := "zed:" + path + ":"
	return atomic(&Agent{
		ID: "zed", Name: "Zed", Icon: "zed", Bin: bin, Dir: dir, Path: path, Spelled: prefixed,
		UA: []string{"zed"},
		Notice: func() string {
			if usesMagpie(model()) && Running(processes...) {
				return noticeZed.String()
			}
			return ""
		},
		Sync: func() error {
			if get(zedProvider+".api_url") != gatewayV1() {
				return nil
			}
			return syncJSON(path, zedProvider+".available_models", func() any {
				return zedProviderJSON(path)["available_models"]
			})
		},
		Check: func() string {
			if !usesMagpie(model()) {
				return ""
			}
			return wiringOff("Zed", path, func(k string) (string, bool) { return edit.GetJSON(path, zedProvider+"."+k) }, "api_url", gatewayV1())
		},
		Fields: []Field{{
			Key: "model", Label: "model", Get: model,
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if err := zedCredential(gatewayV1()); err != nil {
						return fmt.Errorf("configure Zed gateway credential: %w", err)
					}
					if !usesMagpie(model()) {
						previous := map[string]string{key + "model": get(zedModel)}
						if get(zedProvider+".api_url") != gatewayV1() {
							previous[key+"provider"] = get(zedProvider)
						}
						stash(previous)
					}
					return edit.SetJSON(path,
						edit.KV{Path: zedProvider, Value: zedProviderJSON(path)},
						edit.KV{Path: zedModel, Value: map[string]string{"provider": magpieID, "model": ref}})
				}
				// Validate a native selection before removing magpie's wiring.
				p, m, ok := strings.Cut(v, "/")
				if v != "" && (!ok || p == "" || m == "") {
					return fmt.Errorf("expected provider/model, got %q", v)
				}
				if get(zedProvider+".api_url") == gatewayV1() || usesMagpie(model()) {
					for _, entry := range []struct{ name, field string }{{"provider", zedProvider}, {"model", zedModel}} {
						if entry.name == "model" && !usesMagpie(model()) {
							forget(key + entry.name)
							continue
						}
						if was := unstash(key + entry.name); was != "" {
							if err := edit.SetJSON(path, edit.KV{Path: entry.field, Value: json.RawMessage(was)}); err != nil {
								return err
							}
						} else if entry.name == "provider" || usesMagpie(model()) {
							if err := edit.DelJSON(path, entry.field); err != nil {
								return err
							}
						}
					}
				} else if v == "" {
					return edit.DelJSON(path, zedModel)
				}
				if v == "" {
					return nil
				}
				return edit.SetJSON(path, edit.KV{Path: zedModel, Value: map[string]string{"provider": p, "model": m}})
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if v := cur["model"]; v != "" && !usesMagpie(v) {
					own = append(own, Option{Value: v, Icon: modelIcon("", v)})
				}
				return append(group("Zed", own), viaMagpie("zed", magpieID+"/")...)
			},
		}},
	}, path, stashPath())
}

func zedProcessNames() []string {
	value := os.Getenv("MAGPIE_ZED_PROCESS_NAMES")
	if value == "" {
		return []string{`(^|/)(zed|zeditor|zed-editor)( |$)`}
	}
	var out []string
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, `(^|/)`+regexp.QuoteMeta(name)+`( |$)`)
		}
	}
	if len(out) == 0 {
		return []string{`(^|/)(zed|zeditor|zed-editor)( |$)`}
	}
	return out
}

// zedLevels are the reasoning_effort values magpie writes for Zed, weakest
// first: those every Zed that reads the field takes. Its settings take
// language_model_core's ReasoningEffort, lowercase; max and none came after
// it (Zed 0.233 has minimal to xhigh), and a value a Zed doesn't know is
// reported as a settings error.
var zedLevels = []string{"minimal", "low", "medium", "high"}

// zedEffort is the reasoning_effort a model is written with, "" for one
// that doesn't think. Zed offers its thinking switch, and its levels, only
// for a model whose reasoning_effort is set and not "none"
// (open_ai_compatible.rs's default_thinking_reasoning_effort), and starts
// on that level (#964): high, or the strongest of the model's own when they
// all fall short of it. A model that thinks with a switch alone, or whose
// levels reach high or above it, is written high, and the gateway fits what
// is asked to the levels the model takes.
func zedEffort(m catalog.Model) string {
	if !m.Reasoning && len(m.Efforts) == 0 {
		return ""
	}
	best := -1
	for _, e := range m.Efforts {
		i := slices.Index(zedLevels, e)
		if i < 0 && slices.Contains([]string{"xhigh", "max", "ultra"}, e) {
			i = len(zedLevels) - 1
		}
		best = max(best, i)
	}
	if best < 0 {
		return "high"
	}
	return zedLevels[best]
}

// zedProviderJSON is magpie's provider in Zed's settings at path. A
// reasoning_effort already on a model there is kept: the user may have
// picked another level, or "none" to keep it from thinking.
func zedProviderJSON(path string) map[string]any {
	kept := map[string]json.RawMessage{}
	if raw, ok := edit.GetJSON(path, zedProvider+".available_models"); ok {
		var cur []struct {
			Name   string          `json:"name"`
			Effort json.RawMessage `json:"reasoning_effort"`
		}
		if json.Unmarshal([]byte(raw), &cur) == nil {
			for _, c := range cur {
				if len(c.Effort) > 0 && string(c.Effort) != "null" {
					kept[c.Name] = c.Effort
				}
			}
		}
	}
	models := []any{}
	for _, m := range magpieModels("zed") {
		context := m.Context
		if context == 0 {
			context = 128000 // Zed requires a context window for every custom model.
		}
		// Zed's max_tokens is the window a prompt and its reply share: it
		// keeps max_output_tokens of it for the reply and lets the prompt
		// fill the rest before it compacts. Context is what a prompt may
		// hold, so the window is it and the reply together (#850); written
		// as Context alone, a model whose reply may be as long as its
		// prompt (glm-4.6) left Zed no room for a prompt at all.
		entry := map[string]any{
			"name": m.ID, "display_name": m.Name, "max_tokens": context,
			"capabilities": map[string]any{
				"tools": true, "images": m.Images,
				"parallel_tool_calls": false, "prompt_cache_key": false,
				"chat_completions": true,
			},
		}
		if output := m.Output; output > 0 {
			if m.Context == 0 {
				// Keep the fallback reply cap when the prompt limit is unknown.
				output = min(output, context)
			}
			entry["max_output_tokens"] = output
			entry["max_tokens"] = context + output
		}
		if e, ok := kept[m.ID]; ok {
			entry["reasoning_effort"] = e
		} else if e := zedEffort(m); e != "" {
			entry["reasoning_effort"] = e
		}
		models = append(models, entry)
	}
	return map[string]any{"api_url": gatewayV1(), "available_models": models}
}

// what zed says after a change (notice.go)
var (
	noticeZed = newNotice("Restart Zed if it still asks for an API key: magpie has configured its gateway credential in the system credential store.")
)
