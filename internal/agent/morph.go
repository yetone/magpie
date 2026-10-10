package agent

// Mister Morph (morph) keeps its settings in ~/.morph/config.yaml, or the
// file $MISTER_MORPH_CONFIG names: the main model under llm.model, asked of
// the vendor llm.inference_provider names at llm.endpoint with llm.api_key.
// It has no list of providers to add one to, so magpie takes llm over:
// inference_provider openai_response_compatible at the gateway's /v1 (morph
// adds no second /v1 to a base ending in one), spoken to on OpenAI's Responses
// API, morph's own default, which the gateway serves for every vendor; its
// token as the key, and the catalog id as the model; a model through magpie
// is "magpie/<provider>/<model>" here. llm.provider is left as it is: morph
// derives it from inference_provider. Morph knows a model's window only from a list of its own, by the id
// after its last slash, so magpie hands it the window it knows in
// llm.context_window_tokens. What the user had under those keys is stashed
// and put back when magpie steps out. llm.profiles and llm.routes are the
// user's: a route that sends the main loop to another profile is left so.

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// morphUA is the User-Agent magpie has morph send, in llm.headers: its
// requests otherwise carry the OpenAI SDK's, which tells them from no one's.
const morphUA = "mistermorph"

// morphKeys are the keys under llm magpie writes, in the order they are put
// back.
var morphKeys = []string{"inference_provider", "endpoint", "api_key", "model", "context_window_tokens", "headers.User-Agent"}

func morph(home string) *Agent { return morphIn(here(home)) }

// morphIn is Mister Morph at a place: this machine's home, or a WSL distro's
// (see wsl.go), where MISTER_MORPH_CONFIG isn't read.
func morphIn(at place) *Agent {
	dir := filepath.Join(at.home, ".morph")
	path := at.getenv("MISTER_MORPH_CONFIG")
	if path == "" {
		path = filepath.Join(dir, "config.yaml")
	}
	key := "morph:" + path + ":"
	getKey := func(k string) string { v, _ := edit.GetYAML(path, "llm."+k); return v }
	// the token is magpie's alone, where the endpoint moves with the
	// gateway's port
	onMagpie := func() bool { return ourKey(getKey("api_key")) }
	// restore puts back what the user had under llm before magpie
	restore := func() error {
		var del []string
		var kvs []edit.KV
		for _, k := range morphKeys {
			v := unstash(key + k)
			switch {
			case v == "":
				del = append(del, "llm."+k)
			case k == "context_window_tokens":
				if n, err := strconv.Atoi(v); err == nil {
					kvs = append(kvs, edit.KV{Path: "llm." + k, Value: n})
					break
				}
				fallthrough
			default:
				kvs = append(kvs, edit.KV{Path: "llm." + k, Value: v})
			}
		}
		if err := edit.DelYAML(path, del...); err != nil {
			return err
		}
		// the headers magpie's User-Agent was alone in
		if m := edit.GetYAMLMap(path, "llm.headers"); m != nil && len(m) == 0 {
			if err := edit.DelYAML(path, "llm.headers"); err != nil {
				return err
			}
		}
		if len(kvs) == 0 {
			return nil
		}
		return edit.SetYAML(path, kvs...)
	}
	// window is the context magpie knows ref to take, 0 when unknown
	window := func(ref string) int {
		for _, m := range magpieModels("morph") {
			if m.ID == ref {
				return m.Context
			}
		}
		return 0
	}
	return &Agent{
		ID: "morph", Name: "Mister Morph", Icon: "mistermorph-color", Aliases: []string{"mistermorph", "mister-morph"}, Spelled: prefixed,
		UA: []string{morphUA},
		// no Bin: morph is other tools' name too; ~/.morph is made by its
		// first run, `morph install` or the desktop app's setup
		Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)morph( |$)`, `(^|/)mistermorph( |$)`) {
				return noticeMorph.String()
			}
			return ""
		},
		Check: func() string {
			if !onMagpie() {
				return ""
			}
			return wiringOff("Mister Morph", path, func(k string) (string, bool) { return edit.GetYAML(path, "llm."+k) },
				"endpoint", at.v1(), "inference_provider", "openai_response_compatible")
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				v := getKey("model")
				if v != "" && onMagpie() {
					return magpieID + "/" + v
				}
				return v
			},
			Set: func(v string) error {
				if v == "" {
					if onMagpie() {
						return restore()
					}
					return edit.DelYAML(path, "llm.model")
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !onMagpie() {
						kv := map[string]string{}
						for _, k := range morphKeys {
							kv[key+k] = getKey(k)
						}
						stash(kv)
					}
					kvs := []edit.KV{
						{Path: "llm.inference_provider", Value: "openai_response_compatible"},
						{Path: "llm.endpoint", Value: at.v1()},
						{Path: "llm.api_key", Value: at.gwKey()},
						{Path: "llm.model", Value: ref},
						{Path: "llm.headers.User-Agent", Value: morphUA},
					}
					if n := window(ref); n > 0 {
						kvs = append(kvs, edit.KV{Path: "llm.context_window_tokens", Value: n})
					} else if err := edit.DelYAML(path, "llm.context_window_tokens"); err != nil {
						return err
					}
					return edit.SetYAML(path, kvs...)
				}
				if onMagpie() {
					if err := restore(); err != nil {
						return err
					}
				}
				return edit.SetYAML(path, edit.KV{Path: "llm.model", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				return append(ownOptions("", cur["model"]), viaMagpie("morph", magpieID+"/")...)
			},
		}, {
			// llm.reasoning_effort; unset, morph sends none
			Key: "effort", Label: "effort",
			Get: func() string { return getKey("reasoning_effort") },
			Set: func(v string) error {
				if v == "" {
					return edit.DelYAML(path, "llm.reasoning_effort")
				}
				return edit.SetYAML(path, edit.KV{Path: "llm.reasoning_effort", Value: v})
			},
			Options: func(map[string]string) []Option {
				return static("none", "minimal", "low", "medium", "high", "xhigh", "max")
			},
		}},
	}
}

// what morph says after a change (notice.go)
var (
	noticeMorph = newNotice("Mister Morph's Console uses this for new tasks; restart open morph chats to use it there.")
)
