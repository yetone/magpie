package agent

// Antigravity's CLI, agy, keeps its settings in
// ~/.gemini/antigravity-cli/settings.json. With "modelProvider": "gemini" it
// asks a Gemini API endpoint rather than Google's own backend, and lists the
// models of customModelsConfig.customModels, each {apiProvider, modelName},
// the one picked being "model". magpie's gateway speaks the Gemini API, so
// magpie adds one custom model per catalog model, keyed
// "magpie/<provider>/<model>", and sets modelProvider and model; the user's
// own are stashed and put back when magpie steps out. The endpoint and key
// agy takes only from its environment (GOOGLE_GEMINI_BASE_URL and
// GEMINI_API_KEY; no .env file), so magpie can't set them: the agent's row
// gives the command that starts agy with them (see Agent.Launch). That
// names the model with --model too: agy 1.2 asks for a custom model picked
// in settings.json or its /model picker with no model name ("model is
// empty"); only one given on the command line resolves.

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

const agyModels = "customModelsConfig.customModels"

// AgyLaunch is the command that starts agy on magpie's gateway and one of
// its custom models, in the shell of this system. Its key names agy: its
// requests go out as Google's Gemini SDK's (User-Agent google-genai-sdk/…),
// with nothing of agy's own, so the gateway knows them by the key
// (x-goog-api-key) alone. A command copied before, with the plain token,
// still reaches magpie.
func AgyLaunch(model string) string {
	key := gateway.TokenFor("agy")
	if runtime.GOOS == "windows" {
		return `$env:GEMINI_API_KEY="` + key + `"; $env:GOOGLE_GEMINI_BASE_URL="` + gateway.URL() + `"; agy --model '` + strings.ReplaceAll(model, "'", "''") + `'`
	}
	return "GEMINI_API_KEY=" + key + " GOOGLE_GEMINI_BASE_URL=" + gateway.URL() + " agy --model '" + strings.ReplaceAll(model, "'", `'\''`) + "'"
}

// agyCustom reads customModels as it is, raw JSON by key.
func agyCustom(path string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	raw, err := edit.Read(path)
	if err != nil || len(raw) == 0 {
		return out
	}
	gjson.GetBytes(jsonc.ToJSONInPlace(raw), agyModels).ForEach(func(k, v gjson.Result) bool {
		out[k.String()] = json.RawMessage(v.Raw)
		return true
	})
	return out
}

func agy(home string) *Agent {
	dir := filepath.Join(home, ".gemini", "antigravity-cli")
	path := filepath.Join(dir, "settings.json")
	get := jsonGet(path, "model")
	provider := jsonGet(path, "modelProvider")
	stashModel, stashProvider := "agy:"+path+":model", "agy:"+path+":modelProvider"
	// setModels writes customModels with the user's own and, when with,
	// magpie's; none left takes the key out
	setModels := func(with bool) error {
		ms := map[string]any{}
		for k, v := range agyCustom(path) {
			if !strings.HasPrefix(k, magpieID+"/") {
				ms[k] = v
			}
		}
		if with {
			for _, m := range magpieModels("agy") {
				ms[magpieID+"/"+m.ID] = map[string]string{"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": m.ID}
			}
		}
		if len(ms) > 0 {
			return edit.SetJSON(path, edit.KV{Path: agyModels, Value: ms})
		}
		if err := edit.DelJSON(path, agyModels); err != nil {
			return err
		}
		if v, ok := edit.GetJSON(path, "customModelsConfig"); ok && strings.TrimSpace(v) == "{}" {
			return edit.DelJSON(path, "customModelsConfig")
		}
		return nil
	}
	// put sets key to v, or takes it out for ""
	put := func(key, v string) error {
		if v == "" {
			return edit.DelJSON(path, key)
		}
		return edit.SetJSON(path, edit.KV{Path: key, Value: v})
	}
	return &Agent{
		ID: "agy", Name: "Antigravity CLI", Icon: "antigravity-color", Aliases: []string{"antigravity-cli"},
		Bin: "agy", Dir: dir, Path: path,
		Launch: func() string {
			if v := get(); usesMagpie(v) {
				return AgyLaunch(v)
			}
			return ""
		},
		Sync: func() error {
			if !usesMagpie(get()) {
				return nil
			}
			return setModels(true)
		},
		Notice: func() string {
			v := get()
			if !usesMagpie(v) {
				return ""
			}
			return "agy takes magpie's gateway and model only from how it is started — start it with " + AgyLaunch(v)
		},
		Check: func() string {
			v := get()
			if !usesMagpie(v) {
				return ""
			}
			if _, ok := agyCustom(path)[v]; !ok {
				return "Antigravity CLI's custom model " + v + " (settings.json) is gone, so it no longer reaches magpie"
			}
			if p := provider(); p != "gemini" {
				return "Antigravity CLI's modelProvider (settings.json) is " + orDefault(p) + ", not gemini, so it asks Google directly"
			}
			return ""
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				cur := get()
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !usesMagpie(cur) {
						stash(map[string]string{stashModel: cur, stashProvider: provider()})
					}
					if err := setModels(true); err != nil {
						return err
					}
					return edit.SetJSON(path, edit.KV{Path: "modelProvider", Value: "gemini"}, edit.KV{Path: "model", Value: v})
				}
				if usesMagpie(cur) {
					// back to what the user had
					if v == "" {
						v = unstash(stashModel)
					} else {
						forget(stashModel)
					}
					if err := put("modelProvider", unstash(stashProvider)); err != nil {
						return err
					}
				}
				if err := put("model", v); err != nil {
					return err
				}
				return setModels(false)
			},
			Options: func(cur map[string]string) []Option {
				custom := agyCustom(path)
				var own []Option
				for k := range custom {
					if !strings.HasPrefix(k, magpieID+"/") {
						own = append(own, Option{Value: k, Icon: modelIcon("", gjson.GetBytes(custom[k], "modelName").String())})
					}
				}
				sort.Slice(own, func(i, j int) bool { return own[i].Value < own[j].Value })
				if c := cur["model"]; c != "" && !usesMagpie(c) && custom[c] == nil {
					own = append([]Option{{Value: c, Icon: modelIcon("", c)}}, own...)
				}
				return append(group("Antigravity CLI", own), viaMagpie("agy", magpieID+"/")...)
			},
		}},
	}
}
