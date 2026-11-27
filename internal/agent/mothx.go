package agent

// MothX (oschina/mothx, the mothx command; formerly vibecoding) is a Go
// terminal coding agent that reads its providers and the model it starts
// on from one settings.json in its config folder: $MOTHX_DIR, else
// %APPDATA%\mothx on Windows and ~/.mothx elsewhere (platform.ConfigDir,
// configDirForOS). Its fields (config.Settings, ProviderConfig,
// ModelConfig, mothx 1.3.104):
//
//	{"defaultProvider":"magpie","defaultModel":"<model>",
//	 "defaultThinkingLevel":"high",
//	 "providers":{"magpie":{"baseUrl":"http://127.0.0.1:3425/v1",
//	   "api":"openai-chat","apiKey":"magpie-mothx","thinkingFormat":"openai",
//	   "models":[{"id":…,"name":…,"reasoning":true,"contextWindow":…,
//	     "maxTokens":…,"cost":{…},"input":["text","image"]}]}}}
//
// A session asks provider defaultProvider for model defaultModel on the
// provider's api (openai-chat: baseUrl + /chat/completions) under Bearer
// apiKey. Its requests carry "Vibecoding Client/<version>" as their
// User-Agent, its old name an alias here; the key magpie gives it names it
// too (gateway.TokenFor). It
// reads settings.json at start-up, and its own /model and /auth dialogs
// write the whole file back (SaveGlobalSettings), every default spelled out.
//
// Thanks to zhenruyan, whose fork's internal/agent/mothx.go (#2353) this
// follows.

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// mothxLevels are the thinking levels MothX takes (provider.ThinkingLevel,
// its --thinking flag): Pi's set.
var mothxLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

var noticeMothx = newNotice("MothX reads its settings at start-up — restart open mothx sessions to use this.")

func mothx(home string) *Agent { return mothxIn(here(home)) }

// mothxIn is MothX at a place: this machine's home, or a WSL distro's (see
// wsl.go), its settings.json naming the gateway as it reaches it from there.
func mothxIn(at place) *Agent {
	dir := mothxDir(at)
	path := filepath.Join(dir, "settings.json")
	get := func(k string) (string, bool) { return edit.GetJSON(path, k) }
	set := func(kvs ...edit.KV) error { return edit.SetJSON(path, kvs...) }
	pair := pairSet(set, "defaultProvider", "defaultModel")
	onMagpie := func() bool { p, _ := get("defaultProvider"); return p == magpieID }
	joined := func() bool { _, ok := get("providers." + magpieID); return ok }
	drop := func() error {
		if !joined() {
			return nil
		}
		if err := edit.DelJSON(path, "providers."+magpieID); err != nil {
			return err
		}
		// MothX omits an empty providers object.
		if raw, ok := get("providers"); ok && strings.TrimSpace(raw) == "{}" {
			return edit.DelJSON(path, "providers")
		}
		return nil
	}
	key := func() string { return agentKeyAt("mothx", at.gw()) }
	block := mothxBlockKept(path, func() any { return mothxProvider(at, key()) })
	writeMagpie := func() error {
		return set(edit.KV{Path: "providers." + magpieID, Value: block()})
	}
	// the file as it was before magpie's first write, put back byte for
	// byte by Disconnect where it leaves it saying the same
	before := beforeMagpie{at.key("mothx.before.absent"), []string{path}}
	return atomic(&Agent{
		ID: "mothx", Name: "MothX", Icon: "mothx", Bin: "mothx", Dir: dir, Path: path, Spelled: prefixed,
		// the command and the package were vibecoding before (its npm
		// vibecoding-installer still installs it under that name), and
		// its requests still say "Vibecoding Client/<version>": usage
		// names them by the word before the space (usage.AgentOf)
		Aliases: []string{"vibecoding"},
		Restore: before.restore,
		Check: func() string {
			if p, _ := get("defaultProvider"); p != magpieID {
				return ""
			}
			return wiringOff("MothX", path, func(k string) (string, bool) { return edit.GetJSON(path, "providers."+magpieID+"."+k) },
				"baseUrl", at.v1(), "apiKey", key())
		},
		ListOnly: true,
		Joined:   joined,
		Unwire: func() error {
			if onMagpie() {
				return nil
			}
			return drop()
		},
		Sync: func() error { return syncJSON(path, "providers."+magpieID, block) },
		Notice: func() string {
			if p, _ := get("defaultProvider"); p == magpieID && Running(`(^|/)mothx( |$)`, `(^|/)vibecoding( |$)`) {
				return noticeMothx.String()
			}
			return ""
		},
		Fields: []Field{
			{
				// defaultProvider and defaultModel, as one provider/model
				Key: "model", Label: "model",
				Get: pairGet(get, "defaultProvider", "defaultModel"),
				Set: func(v string) error {
					if v == "" {
						if onMagpie() {
							if err := edit.DelJSON(path, "defaultProvider", "defaultModel"); err != nil {
								return err
							}
						}
						return drop()
					}
					// a model of magpie's: its provider block goes in beside the pick
					if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
						if _, has := get("providers." + magpieID); !has {
							if !onMagpie() {
								before.take()
							}
						}
						if err := writeMagpie(); err != nil {
							return err
						}
					}
					return pair(v)
				},
				Options: func(cur map[string]string) []Option {
					return withVia(mothxOwn(path, cur["model"]), "mothx", magpieID+"/")
				},
			},
			{
				// defaultThinkingLevel, what a session starts at; MothX sends
				// none for a model whose entry doesn't say it reasons
				Key: "effort", Label: "thinking",
				Get: func() string { v, _ := get("defaultThinkingLevel"); return v },
				Set: func(v string) error {
					if v == "" {
						return edit.DelJSON(path, "defaultThinkingLevel")
					}
					return set(edit.KV{Path: "defaultThinkingLevel", Value: v})
				},
				Options: func(cur map[string]string) []Option {
					if offered := mothxOffered(cur["model"]); offered != nil {
						return static(offered...)
					}
					return static(mothxLevels...)
				},
			},
		},
	}, path)
}

// mothxDir is MothX's config folder at a place: $MOTHX_DIR (a relative one
// is its working directory's, which magpie can't know), else
// %APPDATA%\mothx on Windows, else ~/.mothx. A distro's variables aren't
// read, so it is ~/.mothx there.
func mothxDir(at place) string {
	if d := at.getenv("MOTHX_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" && at.spell == nil {
		if app := appdir.Getenv("APPDATA"); app != "" {
			return filepath.Join(app, "mothx")
		}
		return at.join("AppData", "Roaming", "mothx")
	}
	return at.join(".mothx")
}

// mothxBlockKept is theirsKept for magpie's provider in MothX's
// settings.json: what else the user put in it (headers, httpProxy) stays,
// and so does a thinkingFormat already there, which may be theirs.
func mothxBlockKept(path string, value func() any) func() any {
	return theirsKept(path, "providers."+magpieID, func() any {
		v := value()
		if _, set := edit.GetJSON(path, "providers."+magpieID+".thinkingFormat"); set {
			if m, ok := v.(map[string]any); ok {
				delete(m, "thinkingFormat")
			}
		}
		return v
	}, "models")
}

// mothxProvider is magpie as MothX's provider: the gateway's Chat
// Completions, the key that names MothX, and one entry per model of
// magpie's.
//
// thinkingFormat "openai" has MothX ask every model for a level as
// reasoning_effort, which the gateway carries to each vendor's own form.
// Without it MothX guesses the vendor from the model id and the base URL
// (thinkingFormatForModel): a magpie model named qwen3.7… got Qwen's
// enable_thinking and thinking_budget, a deepseek-v4.1… only
// thinking:{type:enabled}, whatever served it.
func mothxProvider(at place, key string) any {
	ms := []map[string]any{}
	for _, m := range sharedModels("mothx") {
		ms = append(ms, mothxModelJSON(m))
	}
	return map[string]any{"baseUrl": at.v1(), "api": "openai-chat", "apiKey": key,
		"thinkingFormat": "openai", "models": ms}
}

// mothxModelJSON is one of magpie's models as an entry of a MothX
// provider's models, as its own presets write one.
func mothxModelJSON(m catalog.Model) map[string]any {
	// reasoning is what has MothX send a thinking level at all
	e := map[string]any{"id": m.ID, "name": m.Name, "reasoning": len(m.Efforts) > 0}
	in := []string{"text"}
	if m.Images {
		in = append(in, "image")
	}
	e["input"] = in
	// without them MothX takes the model for its own default window and
	// caps a reply at its own default
	if m.Context > 0 {
		e["contextWindow"] = m.Context
	}
	if m.Output > 0 {
		e["maxTokens"] = maxTokens(m)
	}
	// MothX counts a turn's cost by it, per million tokens as magpie's
	if m.Price != nil {
		e["cost"] = map[string]any{"input": m.Price.Input, "output": m.Price.Output,
			"cacheRead": m.Price.CacheRead, "cacheWrite": m.Price.CacheWrite}
	}
	return e
}

// mothxOwn are the models of the user's own providers in settings.json,
// as provider/model, and the current pick when it is none of them (one of
// MothX's built-in presets, such as deepseek-openai).
func mothxOwn(path, cur string) []Option {
	var out []Option
	seen := map[string]bool{}
	if raw, ok := edit.GetJSON(path, "providers"); ok {
		var ps map[string]struct {
			Models []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.Unmarshal([]byte(raw), &ps) == nil {
			names := make([]string, 0, len(ps))
			for p := range ps {
				if p != magpieID {
					names = append(names, p)
				}
			}
			sort.Strings(names)
			for _, p := range names {
				var ms []catalog.Model
				for _, m := range ps[p].Models {
					if m.ID != "" {
						ms = append(ms, catalog.Model{ID: m.ID, Name: m.Name})
						seen[p+"/"+m.ID] = true
					}
				}
				out = append(out, group(p, options(ms, p+"/"))...)
			}
		}
	}
	if cur != "" && !usesMagpie(cur) && !seen[cur] {
		out = append([]Option{{Value: cur, Icon: modelIcon("", cur)}}, out...)
	}
	return out
}

// mothxOffered are the levels MothX offers for model when it is one of
// magpie's whose efforts magpie knows: those of them MothX has, none read
// as off. nil for any other model, which is offered MothX's whole list.
func mothxOffered(model string) []string {
	ref, ok := strings.CutPrefix(model, magpieID+"/")
	if !ok {
		return nil
	}
	for _, m := range sharedModels("mothx") {
		if m.ID != ref || len(m.Efforts) == 0 {
			continue
		}
		var out []string
		for _, e := range m.Efforts {
			if e == "none" {
				e = "off"
			}
			if slices.Contains(mothxLevels, e) && !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
