package agent

// Bub (bubbuild/bub, Apache-2.0, the `bub` CLI) keeps its settings in
// config.yml in $BUB_HOME (~ expanded), else ~/.bub (bub.home,
// bub/configure.py). Its model is the top-level model, "<provider>:<model>"
// split at the first colon, and a provider of the user's naming is an entry
// of providers: {type, api_key, api_base, headers}, where type is one of
// Republic's providers (AgentSettings.providers, CustomProvider in
// bub/builtin/settings.py). A named provider's api_key and api_base win over
// the top-level ones and over BUB_<PROVIDER>_API_KEY
// (model_client_kwargs).
//
// Republic, the model library Bub 0.6 runs on, has a provider for magpie's
// gateway itself (republic/providers/magpie.py): chat completions at
// <base>/chat/completions, with the session's id as x-session-id and the
// prompt_cache_key. So magpie adds providers.magpie {type: magpie,
// api_base: the gateway's /v1, api_key: a key naming Bub} and points model
// at "magpie:<provider>/<model>". Bub lists that provider's models itself
// from the gateway's /v1/models, so no list of magpie's is written. What
// model was, and an entry the user had named magpie, are stashed and put
// back when magpie steps out; every other key of the file stays as written.
// Bub 0.5 ran on any-llm, which has no magpie provider: one found on that
// version gets type openai, the same gateway as chat completions.
//
// Bub's requests say bub/<version> (settings.USER_AGENT), its id. Its
// Library places (internal/library/targets.go) are ~/.agents/skills, the
// global root of bub/skills.py, and $BUB_HOME/mcp.json, which the bub-mcp
// plugin reads (bub-contrib packages/bub-mcp).

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

const bubID = "bub"

// bubEntry is magpie's provider in Bub's config.yml.
const bubEntry = "providers." + magpieID

// what Bub says after a change (notice.go)
var noticeBub = newNotice("Bub reads its settings at start-up — restart open bub sessions to use this.")

// bubDir is Bub's folder: $BUB_HOME (~ expanded), else ~/.bub.
func bubDir(at place) string {
	if d := strings.TrimSpace(at.getenv("BUB_HOME")); d != "" {
		if rest, ok := strings.CutPrefix(d, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == '\\') {
			return at.join(strings.TrimLeft(rest, "/\\"))
		}
		return d
	}
	return at.join(".bub")
}

// bubRef is the catalog ref a model value of Bub's names through magpie's
// provider, "magpie:<ref>", and whether it is one.
func bubRef(v string) (string, bool) { return strings.CutPrefix(v, magpieID+":") }

// bubType is the provider type magpie's entry takes: Republic's magpie,
// unless the bub found runs on Bub 0.5's any-llm, which has none (an
// unknown type stops Bub at start). Where Bub's packages can't be read
// it is magpie, Bub 0.6's.
func bubType(bin string) string {
	if bin == "" {
		return magpieID
	}
	venv := filepath.Dir(filepath.Dir(evalLink(bin)))
	for _, site := range []string{"lib/python*/site-packages", "Lib/site-packages"} {
		dirs, _ := filepath.Glob(filepath.Join(venv, filepath.FromSlash(site)))
		for _, d := range dirs {
			if _, err := os.Stat(filepath.Join(d, "republic", "providers", "magpie.py")); err == nil {
				return magpieID
			}
			if _, err := os.Stat(filepath.Join(d, "any_llm")); err == nil {
				if _, err := os.Stat(filepath.Join(d, "republic")); os.IsNotExist(err) {
					return "openai"
				}
			}
		}
	}
	return magpieID
}

func bub(home string) *Agent { return bubAt(here(home)) }

func bubAt(at place) *Agent {
	dir := bubDir(at)
	path := filepath.Join(dir, "config.yml")
	keyPick := at.key("bub:" + path + ":model")
	keyEntry := at.key("bub:" + path + ":entry")
	key := func() string { return agentKeyAt(bubID, at.gw()) }
	get := func(k string) string { v, _ := edit.GetYAML(path, k); return v }
	entry := func() bool { _, ok := edit.GetYAMLText(path, bubEntry); return ok }
	// ours: Bub runs a model through magpie's entry
	ours := func() bool {
		_, ok := bubRef(get("model"))
		return ok && entry()
	}
	setModel := func(v string) error {
		if v == "" {
			return edit.DelYAML(path, "model")
		}
		return edit.SetYAML(path, edit.KV{Path: "model", Value: v})
	}
	// wire writes magpie's entry; a key the user put on it (headers) stays
	wire := func() error {
		typ := bubType("")
		if at.spell == nil {
			typ = bubType(binAt("bub"))
		}
		return edit.SetYAML(path,
			edit.KV{Path: bubEntry + ".type", Value: typ},
			edit.KV{Path: bubEntry + ".api_base", Value: at.v1()},
			edit.KV{Path: bubEntry + ".api_key", Value: key()})
	}
	// dropEntry takes magpie's entry out, an emptied providers with it, and
	// puts back one the user had named magpie before
	dropEntry := func() error {
		if err := edit.DelYAML(path, bubEntry); err != nil {
			return err
		}
		if was := unstash(keyEntry); was != "" {
			return edit.SetYAML(path, edit.KV{Path: bubEntry, Value: edit.YAMLText(was)})
		}
		if t, ok := edit.GetYAMLText(path, "providers"); ok && strings.TrimSpace(t) == "{}" {
			return edit.DelYAML(path, "providers")
		}
		return nil
	}
	unwire := func(own string) error {
		if ours() {
			was := unstash(keyPick)
			if own != "" {
				was = own
			}
			if err := setModel(was); err != nil {
				return err
			}
		} else {
			forget(keyPick)
			if own != "" {
				if err := setModel(own); err != nil {
					return err
				}
			}
		}
		return dropEntry()
	}
	return atomic(&Agent{
		ID: bubID, Name: "Bub", Icon: "bub",
		Spelled: prefixed, NoNames: true,
		Bin: "bub", Dir: dir, Path: path,
		// the MCP servers magpie's Library gives the bub-mcp plugin: a
		// folder holding only that is no sign of Bub
		synced: []string{"mcp.json"},
		Notice: func() string {
			if Running(`(^|/)bub( |$)`) {
				return noticeBub.String()
			}
			return ""
		},
		// the gateway's address or key moved: magpie's entry follows
		Sync: func() error {
			if !ours() {
				return nil
			}
			return wire()
		},
		Check: func() string {
			if !ours() {
				return ""
			}
			return wiringOff("Bub", path, func(k string) (string, bool) {
				v, ok := edit.GetYAML(path, bubEntry+"."+k)
				return v, ok && v != ""
			}, "api_base", at.v1(), "api_key", key())
		},
		Fields: []Field{{
			// the model Bub starts on: one of magpie's is
			// magpie/<provider>/<model> here, magpie:<provider>/<model> in
			// config.yml
			Key: "model", Label: "model",
			Get: func() string {
				m := get("model")
				if ref, ok := bubRef(m); ok && entry() {
					return magpieID + "/" + ref
				}
				return m
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !ours() {
						stash(map[string]string{keyPick: get("model")})
						if t, ok := edit.GetYAMLText(path, bubEntry); ok {
							stash(map[string]string{keyEntry: t})
						}
					}
					if err := wire(); err != nil {
						return err
					}
					return setModel(magpieID + ":" + ref)
				}
				if ours() {
					return unwire(v)
				}
				return setModel(v)
			},
			Options: func(cur map[string]string) []Option {
				return withVia(bubOwn(keyPick, cur["model"]), bubID, magpieID+"/")
			},
		}},
	}, path)
}

// bubOwn is the model Bub started on before magpie, or starts on now, for
// the picker: Bub's own providers are its config's, whose models magpie
// doesn't list.
func bubOwn(keyPick, cur string) []Option {
	var out []Option
	for _, m := range []string{stashShared()[keyPick], cur} {
		if m == "" || prefixed(m) || slices.ContainsFunc(out, func(o Option) bool { return o.Value == m }) {
			continue
		}
		_, id, _ := strings.Cut(m, ":")
		out = append(out, Option{Value: m, Icon: modelIcon("", id), Note: "Bub's own"})
	}
	return group("Bub", out)
}
