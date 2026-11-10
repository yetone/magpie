package agent

// Paimon (aisk/paimon, `uv tool install paimon`; ditangongyuan on X) is a
// coding agent in the terminal, built on pydantic-ai. Read at 7f7fa51
// (paimon/config.py, paimon/llm.py):
//
// Its settings are one JSON file, $PAIMON_CONFIG_HOME/config.json, else
// ~/.config/paimon/config.json (Windows too: Path.home()). "model" is
// "provider:name" (or the older "provider/name"), and "providers" keeps
// each provider's api_base and api_key, so a model is sent with its own
// provider's credentials only. The provider half picks pydantic-ai's
// provider class; a custom api_base on one that has no base_url argument
// goes through an OpenAI client of its own (llm.build_model).
//
// magpie uses pydantic-ai's litellm provider, an OpenAI-compatible proxy
// that profiles a "vendor/model" name by its vendor, as magpie's ids are
// spelled. Paimon's login doesn't offer it (it lists the providers of
// pydantic-ai's known model names), so it is almost never one of the
// user's own. magpie sets "model" to litellm:<catalog id>, providers.litellm
// to the gateway's /v1 and magpie's key for Paimon, and
// compaction.context_window to the window magpie knows the model to take:
// Paimon compacts against that only for a name genai-prices doesn't know,
// which magpie's ids aren't, and doesn't compact at all without it. What
// the user had in those three is stashed and put back when magpie steps
// out; the rest of the file is left alone.
//
// Paimon reads the file when it starts, and its own saves change only the
// keys they name (Config.save), so a running Paimon keeps its model until
// it starts again.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

// paimonProvider is the pydantic-ai provider magpie's models go through.
const paimonProvider = "litellm"

// paimonDir is Paimon's config folder at the place: $PAIMON_CONFIG_HOME
// when it is a whole path on this machine, else ~/.config/paimon.
func paimonDir(at place) string {
	if d := at.getenv("PAIMON_CONFIG_HOME"); d != "" && filepath.IsAbs(d) {
		return d
	}
	return at.join(".config", "paimon")
}

func paimon(home string) *Agent { return paimonAt(here(home)) }

// paimonIn is Paimon in a WSL distro (see wsl.go).
func paimonIn(at place) *Agent { return paimonAt(at) }

func paimonAt(at place) *Agent {
	dir := paimonDir(at)
	path := filepath.Join(dir, "config.json")
	key := "paimon:" + path + ":"
	keyModel, keyProv, keyWindow := key+"model", key+"provider", key+"window"
	// keyNew marks a file magpie made, which goes again once magpie steps
	// out and nothing is left in it
	keyNew := key + "new"
	gwKey := func() string { return agentKeyAt("paimon", at.gw()) }
	read := func(p string) gjson.Result {
		raw, err := edit.Read(path)
		if err != nil {
			return gjson.Result{}
		}
		return gjson.GetBytes(raw, p)
	}
	model := func() string { return read("model").String() }
	prov := func() gjson.Result { return read("providers." + paimonProvider) }
	// ours: providers.litellm is magpie's, told by its key
	ours := func() bool { return keyHolds(prov().Get("api_key").String(), gwKey()) }
	// on: the model one of magpie's, sent to magpie's provider
	on := func() bool {
		ref, ok := strings.CutPrefix(model(), paimonProvider+":")
		return ok && ref != "" && ours()
	}
	window := func(ref string) int {
		for _, m := range sharedModels("paimon") {
			if m.ID == ref {
				return m.Context
			}
		}
		return 0
	}
	// prune takes out compaction and providers where magpie left them
	// empty, and the file where magpie made it and left nothing
	prune := func(made bool) error {
		raw, err := edit.Read(path)
		if err != nil {
			return nil
		}
		var del []string
		for _, k := range []string{"compaction", "providers"} {
			if r := gjson.GetBytes(raw, k); r.IsObject() && len(r.Map()) == 0 {
				del = append(del, k)
			}
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
	// restore takes magpie out: the model, provider and window back to
	// what the user had; then v, if any, as the model
	restore := func(v string) error {
		was, wasProv, wasWin := unstash(keyModel), unstash(keyProv), unstash(keyWindow)
		made := unstash(keyNew) != ""
		if !isFile(path) {
			return nil
		}
		if err := edit.DelJSON(path, "model", "providers."+paimonProvider, "compaction.context_window"); err != nil {
			return err
		}
		var kvs []edit.KV
		if v != "" {
			was = v
		}
		if was != "" {
			kvs = append(kvs, edit.KV{Path: "model", Value: was})
		}
		if gjson.Valid(wasProv) && gjson.Parse(wasProv).IsObject() {
			var p map[string]any
			json.Unmarshal([]byte(wasProv), &p)
			kvs = append(kvs, edit.KV{Path: "providers." + paimonProvider, Value: p})
		}
		if wasWin != "" {
			if n, err := strconv.Atoi(wasWin); err == nil {
				kvs = append(kvs, edit.KV{Path: "compaction.context_window", Value: n})
			}
		}
		if len(kvs) > 0 {
			if err := edit.SetJSON(path, kvs...); err != nil {
				return err
			}
		}
		return prune(made)
	}
	return atomic(&Agent{
		ID: "paimon", Name: "Paimon", Icon: "paimon", Spelled: prefixed, NoNames: true,
		// pydantic-ai's requests carry paimon/<version> (llm.user_agent)
		UA:  []string{"paimon/"},
		Bin: "paimon", Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)paimon( |$)`) {
				return noticePaimon.String()
			}
			return ""
		},
		Check: func() string {
			if !on() {
				return ""
			}
			p := prov()
			return wiringOff("Paimon", path, func(k string) (string, bool) {
				r := p.Get(k)
				return r.String(), r.Exists()
			}, "api_base", at.v1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				m := model()
				if on() {
					return magpieID + "/" + strings.TrimPrefix(m, paimonProvider+":")
				}
				return m
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !ours() {
						// what the user had, unless magpie stashed it already
						forget(keyModel, keyProv, keyWindow, keyNew)
						kv := map[string]string{
							keyModel:  model(),
							keyProv:   prov().Raw,
							keyWindow: read("compaction.context_window").Raw,
						}
						if !isFile(path) {
							kv[keyNew] = "1"
						}
						stash(kv)
					} else if m := model(); !on() && m != "" {
						// a model the user picked in Paimon since, on a
						// provider of their own: theirs now, put back later
						stash(map[string]string{keyModel: m})
					}
					if !isFile(path) {
						// Paimon keeps keys in it: private, as Paimon makes it
						if err := os.MkdirAll(dir, 0o755); err != nil {
							return err
						}
						if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
							f.WriteString("{}\n")
							f.Close()
						}
					}
					kvs := []edit.KV{
						{Path: "model", Value: paimonProvider + ":" + ref},
						{Path: "providers." + paimonProvider, Value: map[string]any{
							"api_base": at.v1(),
							"api_key":  gwKey(),
						}},
					}
					// the model's window; for one magpie doesn't know, the
					// user's own, never the last model's
					n := window(ref)
					if n == 0 {
						n, _ = strconv.Atoi(unstashPeek(keyWindow))
					}
					if n > 0 {
						kvs = append(kvs, edit.KV{Path: "compaction.context_window", Value: n})
					} else if err := edit.DelJSON(path, "compaction.context_window"); err != nil {
						return err
					}
					if err := edit.SetJSON(path, kvs...); err != nil {
						return err
					}
					return prune(false)
				}
				if ours() {
					return restore(v)
				}
				if v == "" {
					if !isFile(path) {
						return nil
					}
					return edit.DelJSON(path, "model")
				}
				return edit.SetJSON(path, edit.KV{Path: "model", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					id := c
					if _, rest, ok := strings.Cut(c, ":"); ok {
						id = rest
					} else if _, rest, ok := strings.Cut(c, "/"); ok {
						id = rest
					}
					own = append(own, Option{Value: c, Icon: modelIcon("", id)})
				}
				return withVia(group("Paimon", own), "paimon", magpieID+"/")
			},
		}},
	}, path)
}

// what Paimon says after a change (notice.go)
var noticePaimon = newNotice("Paimon reads its model at start-up — restart open paimon sessions to use this.")
