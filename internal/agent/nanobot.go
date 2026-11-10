package agent

// nanobot (HKUDS/nanobot, `pip install nanobot-ai`; Niapyaya on X) is a
// personal AI assistant that answers in chat apps, its CLI and its WebUI.
// It keeps its settings in ~/.nanobot/config.json (config/home.py,
// config/loader.py; read at 5c72ced, 0.4.1), plain JSON its pydantic models
// read with camelCase or snake_case keys alike (config_base.Base).
//
// The model it answers with is agents.defaults.model, asked of
// agents.defaults.provider ("auto" picks one by the model's name), unless
// agents.defaults.modelPreset names one of modelPresets, which then goes
// over both (Config.resolve_preset). Any key under providers that isn't one
// of its own is an OpenAI-compatible endpoint of the user's
// (ProvidersConfig.convert_extra_providers) at its apiBase, sent apiKey and
// extraHeaders, and a model "<that key>/<id>" is sent to it as <id>
// (create_dynamic_spec's strip_model_prefixes).
//
// So magpie adds providers.magpie at the gateway's /v1, with its key and a
// User-Agent of nanobot's name (the OpenAI SDK's otherwise, which tells it
// from no one), and sets agents.defaults to provider magpie, model
// magpie/<catalog id> and the window magpie knows the model to take. What
// the user had there, and a modelPreset that would go over it, is stashed
// and put back when magpie steps out; providers.magpie goes again then.
// The user's other providers, presets and keys stay as they are.
//
// nanobot's gateway (`nanobot gateway`, the WebUI's) watches the file and
// takes a change at its next turn (config/watcher.py,
// AgentLoop.invalidate_runtime_config); `nanobot agent` reads it once.

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

// nanobotUA is the User-Agent magpie has nanobot send, in its provider's
// extraHeaders.
const nanobotUA = "nanobot"

// nanobotDefaults are the keys under agents.defaults magpie writes or takes
// out, by the name nanobot writes them with (camelCase), and their
// snake_case spellings, which nanobot reads as well.
var nanobotDefaults = []struct{ key, snake string }{
	{"provider", "provider"},
	{"model", "model"},
	{"contextWindowTokens", "context_window_tokens"},
	{"modelPreset", "model_preset"},
}

func nanobot(home string) *Agent { return nanobotAt(here(home)) }

// nanobotIn is nanobot in a WSL distro (see wsl.go).
func nanobotIn(at place) *Agent { return nanobotAt(at) }

// nanobotAt is nanobot with its home at at.home, reaching the gateway as at
// does.
func nanobotAt(at place) *Agent {
	dir := at.join(".nanobot")
	path := at.join(".nanobot", "config.json")
	key := "nanobot:" + path + ":"
	// keyNew marks a file magpie made, which goes again once magpie's
	// provider and model are taken out of it and nothing is left
	keyNew := key + "new"
	// get is a key of agents.defaults as written, either spelling
	get := func(k, snake string) (string, bool) {
		raw, err := edit.Read(path)
		if err != nil {
			return "", false
		}
		d := gjson.GetBytes(raw, "agents.defaults")
		for _, n := range []string{k, snake} {
			if r := d.Get(gjson.Escape(n)); r.Exists() {
				return r.String(), true
			}
		}
		return "", false
	}
	model := func() string { v, _ := get("model", "model"); return v }
	provider := func() string { v, _ := get("provider", "provider"); return v }
	// ours is magpie's provider entry, "" where there is none
	ours := func() string {
		raw, err := edit.Read(path)
		if err != nil {
			return ""
		}
		return gjson.GetBytes(raw, "providers."+magpieID).Raw
	}
	// on: magpie's — the model one of magpie's, asked of its provider
	on := func() bool {
		ref, ok := strings.CutPrefix(model(), magpieID+"/")
		return ok && isMagpie(ref) && provider() == magpieID
	}
	// window is the context magpie knows ref to take, 0 when unknown
	window := func(ref string) int {
		for _, m := range sharedModels("nanobot") {
			if m.ID == ref {
				return m.Context
			}
		}
		return 0
	}
	// dels are the paths of agents.defaults' keys, both spellings
	dels := func() []string {
		var out []string
		for _, k := range nanobotDefaults {
			out = append(out, "agents.defaults."+k.key)
			if k.snake != k.key {
				out = append(out, "agents.defaults."+k.snake)
			}
		}
		return out
	}
	// prune takes out agents.defaults, agents and providers where magpie
	// left them empty, and the file where magpie made it and left nothing
	prune := func(made bool) error {
		raw, err := edit.Read(path)
		if err != nil {
			return nil
		}
		var del []string
		if d := gjson.GetBytes(raw, "agents.defaults"); d.IsObject() && len(d.Map()) == 0 {
			del = append(del, "agents.defaults")
			if a := gjson.GetBytes(raw, "agents"); len(a.Map()) == 1 {
				del = append(del, "agents")
			}
		}
		if p := gjson.GetBytes(raw, "providers"); p.IsObject() && len(p.Map()) == 0 {
			del = append(del, "providers")
		}
		if len(del) > 0 {
			if err := edit.DelJSON(path, del...); err != nil {
				return err
			}
		}
		if made {
			if raw, err := edit.Read(path); err == nil && len(gjson.ParseBytes(raw).Map()) == 0 {
				return edit.Remove(path)
			}
		}
		return nil
	}
	// restore takes magpie out: its provider, and agents.defaults back to
	// what the user had; then v, if any, as the model
	restore := func(v string) error {
		made := unstash(keyNew) != ""
		var kvs []edit.KV
		for _, k := range nanobotDefaults {
			was := unstash(key + k.key)
			switch {
			case was == "":
			case k.key == "contextWindowTokens":
				if n, err := strconv.Atoi(was); err == nil {
					kvs = append(kvs, edit.KV{Path: "agents.defaults." + k.key, Value: n})
				}
			default:
				kvs = append(kvs, edit.KV{Path: "agents.defaults." + k.key, Value: was})
			}
		}
		if v != "" {
			kvs = append(kvs, edit.KV{Path: "agents.defaults.model", Value: v})
		}
		if !isFile(path) {
			return nil
		}
		if err := edit.DelJSON(path, append(dels(), "providers."+magpieID)...); err != nil {
			return err
		}
		if len(kvs) > 0 {
			if err := edit.SetJSON(path, kvs...); err != nil {
				return err
			}
		}
		return prune(made)
	}
	return atomic(&Agent{
		ID: "nanobot", Name: "nanobot", Icon: "nanobot-color", Aliases: []string{"nanobot-ai"}, Spelled: prefixed, NoNames: true,
		UA: []string{nanobotUA},
		// no Bin: nanobot is other tools' name too; ~/.nanobot is made by
		// `nanobot onboard` and its first run
		Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)nanobot( |$)`) {
				return noticeNanobot.String()
			}
			return ""
		},
		Check: func() string {
			if !on() {
				return ""
			}
			raw := ours()
			if raw == "" {
				return "nanobot's magpie provider (config.json) is gone, so it no longer reaches magpie"
			}
			return wiringOff("nanobot", path, func(k string) (string, bool) {
				r := gjson.Get(raw, k)
				return r.String(), r.Exists()
			}, "apiBase", at.v1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: model,
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !on() && ours() == "" {
						// what the user had, unless magpie stashed it already
						kv := map[string]string{}
						for _, k := range nanobotDefaults {
							forget(key + k.key)
							kv[key+k.key], _ = get(k.key, k.snake)
						}
						forget(keyNew)
						if !isFile(path) {
							kv[keyNew] = "1"
						}
						stash(kv)
					}
					if err := edit.DelJSON(path, dels()...); err != nil && isFile(path) {
						return err
					}
					kvs := []edit.KV{
						{Path: "providers." + magpieID, Value: map[string]any{
							"apiKey":       at.gwKey(),
							"apiBase":      at.v1(),
							"extraHeaders": map[string]string{"User-Agent": nanobotUA},
						}},
						{Path: "agents.defaults.provider", Value: magpieID},
						{Path: "agents.defaults.model", Value: v},
					}
					if n := window(ref); n > 0 {
						kvs = append(kvs, edit.KV{Path: "agents.defaults.contextWindowTokens", Value: n})
					}
					return edit.SetJSON(path, kvs...)
				}
				if on() || ours() != "" {
					return restore(v)
				}
				if v == "" {
					if !isFile(path) {
						return nil
					}
					return edit.DelJSON(path, "agents.defaults.model")
				}
				return edit.SetJSON(path, edit.KV{Path: "agents.defaults.model", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					id := c
					if _, rest, ok := strings.Cut(c, "/"); ok {
						id = rest
					}
					own = append(own, Option{Value: c, Icon: modelIcon("", id)})
				}
				return withVia(group("nanobot", own), "nanobot", magpieID+"/")
			},
		}},
	}, path)
}

// what nanobot says after a change (notice.go)
var (
	noticeNanobot = newNotice("nanobot's gateway and WebUI take this at their next reply; restart an open `nanobot agent` chat to use it there.")
)
