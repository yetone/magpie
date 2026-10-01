package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Codex talks the OpenAI Responses API. Signed in to ChatGPT, it is
// routed through magpie with `openai_base_url` alone: Codex keeps its
// built-in OpenAI provider and sign-in, so its own models, its threads
// (listed per provider) and the Codex app's model picker stay as they are,
// and magpie's gateway passes its own models through to OpenAI while it
// answers the rest — the model list included, so magpie's models join
// OpenAI's in /model. Not signed in, the built-in provider can't run, and
// magpie is a provider of its own: a [model_providers.magpie] table,
// `model_provider = "magpie"`, and a model catalog file for /model. So it
// is too for Codex signed in with an API key, often one a relay issued:
// Codex then never asks for the model list (its models manager skips the
// fetch for an API key), so its picker was the models it was built with
// and the one set, magpie's others missing, and the built-in ones went to
// OpenAI with that key (#322). So it is too for a ChatGPT account that
// has used its allowance up, which the Codex app won't send anything for,
// whoever serves the model, and when the user asks for it (the sign-in
// field's api): the Codex app is then in
// its API state rather than signed in to ChatGPT. The base URL is set then
// too: a thread started on the built-in provider is opened on it again, and
// a magpie model picked in it would otherwise go to the ChatGPT backend.
//
// The [model_providers.magpie] table stays once written. A thread keeps the
// provider it was started on, and one started on magpie can't be opened
// again without the table ("Model provider `magpie` not found"), whichever
// way Codex is routed now.

// codexStandIn is the model Codex is set to use, when magpie is its
// provider, for a model it names that magpie doesn't serve. Codex asks for
// some by a name of OpenAI's whatever its provider: auto-review of an
// approval goes out as "codex-auto-review" (the guardian's reviewer model,
// "independent of current login"), which magpie answered 404 "magpie knows
// no model", and Codex reported the review as failed. Codex itself reviews
// with the turn's model when its catalog lacks the reviewer; so does magpie.
// Signed in, Codex's own names go on to OpenAI (codexUpstream) instead.
func codexStandIn(path string) string {
	if p, _ := edit.GetTOMLTop(path, "model_provider"); p != magpieID {
		return ""
	}
	m, _ := edit.GetTOMLTop(path, "model")
	return m
}

func codex(home string) *Agent { return codexIn(here(home)) }

// codexIn is Codex as it lives at a place: this machine's home, or a WSL
// distro's (see wsl.go).
func codexIn(at place) *Agent {
	dir := filepath.Join(at.home, ".codex")
	path := filepath.Join(dir, "config.toml")
	catalogPath := filepath.Join(dir, "magpie-models.json")
	get := func(k string) string { v, _ := edit.GetTOMLTop(path, k); return v }
	asProvider := func() bool { return get("model_provider") == magpieID }
	viaBase := func() bool { return isCodexGatewayOn(get("openai_base_url"), at.host()) }
	routed := func() bool { return asProvider() || viaBase() }
	models := func() []catalog.Model {
		switch {
		case asProvider():
			return magpieModels("codex")
		case viaBase():
			return append(catalog.Codex(), magpieModels("codex")...)
		}
		return catalog.Codex()
	}
	var dropSubEffort func() error
	// keep the effort valid for the model; a fresh model gets its default.
	// Routed, the Codex app offers magpie's models' efforts too (#310).
	settle := func() error {
		if routed() {
			if err := codexEnableEfforts(path, magpieModels("codex")); err != nil {
				return err
			}
		}
		ms := models()
		model, effort := get("model"), get("model_reasoning_effort")
		if e := catalog.Efforts(ms, model); len(e) > 0 && !contains(e, effort) {
			if err := edit.SetTOMLTop(path, edit.KV{Path: "model_reasoning_effort", Value: codexcat.DefaultEffort(e)}); err != nil {
				return err
			}
		}
		return dropSubEffort()
	}
	// magpie as a provider Codex can name; its threads started on magpie do
	putProvider := func() error {
		return edit.SetTOMLTable(path, "model_providers."+magpieID,
			edit.KV{Path: "name", Value: "magpie"},
			edit.KV{Path: "base_url", Value: at.v1()},
			edit.KV{Path: "wire_api", Value: "responses"},
			edit.KV{Path: "experimental_bearer_token", Value: gateway.Token},
		)
	}
	hasProvider := func() bool {
		t, err := edit.GetTOMLTable(path, "model_providers."+magpieID)
		return err == nil && t != nil
	}
	// dropProvider takes magpie out as the provider Codex is on; the table
	// stays for the threads started on it
	dropProvider := func() error {
		if !asProvider() {
			return nil
		}
		if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
			return err
		}
		os.Remove(catalogPath)
		return nil
	}
	// CC Switch's provider tables in Codex's config, each one's base URL
	// by its id. A thread keeps the provider it was
	// started on, and CC Switch moves every third-party thread onto its
	// "custom" one, so one reopened there still went to the relay, a
	// magpie model picked in it too, which the relay didn't know. While a
	// magpie model is on, those tables go through magpie as well; a table
	// a profile names is the user's to pick, and stays as it is.
	ccSwitchTables := func() map[string]string {
		names, _ := edit.TOMLTables(path)
		named := map[string]bool{}
		for _, n := range names {
			if strings.HasPrefix(n, "profiles.") {
				if t, _ := edit.GetTOMLTable(path, n); t["model_provider"] != "" {
					named[t["model_provider"]] = true
				}
			}
		}
		out := map[string]string{}
		for _, n := range names {
			id, ok := strings.CutPrefix(n, "model_providers.")
			if !ok || !ccSwitchProvider.MatchString(id) || named[id] {
				continue
			}
			if t, _ := edit.GetTOMLTable(path, n); t["base_url"] != "" {
				out[id] = t["base_url"]
			}
		}
		return out
	}
	// takeTables points CC Switch's tables at magpie, each one's own base
	// URL kept in the stash for giveTables (a table already on magpie's
	// keeps the one kept for it)
	takeTables := func() error {
		was := map[string]string{}
		json.Unmarshal([]byte(stashLoad()[at.key("codex.tables")]), &was)
		for id, u := range ccSwitchTables() {
			if u == at.v1() {
				continue
			}
			if err := edit.SetTOMLKey(path, "model_providers."+id, "base_url", at.v1()); err != nil {
				return err
			}
			was[id] = u
		}
		if len(was) == 0 {
			return nil
		}
		b, _ := json.Marshal(was)
		stash(map[string]string{at.key("codex.tables"): string(b)})
		return nil
	}
	// giveTables puts the base URLs back, on the tables still pointed at
	// magpie; one changed since is left as it is now
	giveTables := func() error {
		var was map[string]string
		json.Unmarshal([]byte(stashLoad()[at.key("codex.tables")]), &was)
		now := ccSwitchTables()
		for id, u := range was {
			if now[id] != at.v1() {
				continue
			}
			if err := edit.SetTOMLKey(path, "model_providers."+id, "base_url", u); err != nil {
				return err
			}
		}
		forget(at.key("codex.tables"))
		return nil
	}
	// api: the user wants magpie as Codex's provider even while Codex is
	// signed in to ChatGPT
	api := func() bool { return stashLoad()[at.key("codex.login")] == "api" }
	dropBase := func() error {
		if !viaBase() {
			return nil
		}
		return edit.DelTOMLTop(path, "openai_base_url")
	}
	// the model spawned subagents start on, when not the parent's; one of
	// magpie's goes when magpie steps out, as Codex could no longer find it
	subagent := func() (string, error) {
		agents, err := edit.GetTOMLTable(path, "agents")
		return agents["default_subagent_model"], err
	}
	dropSubagent := func() error {
		model, err := subagent()
		if err != nil {
			return err
		}
		if !isMagpie(model) {
			return nil
		}
		return edit.DelTOMLKey(path, "agents", "default_subagent_model")
	}
	// the effort spawned subagents start at, when not the parent's
	// (core/src/agent/child_config.rs): checked against the model they run
	// on, theirs or the parent's, and a spawn at one it lacks fails, so one
	// that model doesn't take goes
	subEffort := func() (string, error) {
		agents, err := edit.GetTOMLTable(path, "agents")
		return agents["default_subagent_reasoning_effort"], err
	}
	subModel := func() string {
		if m, _ := subagent(); m != "" {
			return m
		}
		return get("model")
	}
	subEfforts := func() []string { return catalog.Efforts(models(), subModel()) }
	dropSubEffort = func() error {
		e, err := subEffort()
		if err != nil || e == "" {
			return err
		}
		if l := subEfforts(); len(l) > 0 && !contains(l, e) {
			return edit.DelTOMLKey(path, "agents", "default_subagent_reasoning_effort")
		}
		return nil
	}
	// Codex on one of its own models goes through magpie too while more of
	// its ChatGPT accounts are on there, so one out of its allowance hands
	// the turn to the next; with none, it goes straight to OpenAI again
	failover := func() error {
		if isMagpie(get("model")) || asProvider() {
			return nil
		}
		if p := get("model_provider"); p != "" && p != "openai" {
			return nil
		}
		on := codexFailover()
		switch {
		case on && !viaBase():
			return edit.SetTOMLTop(path, edit.KV{Path: "openai_base_url", Value: at.codexURL()})
		case !on && viaBase():
			return dropBase()
		}
		return nil
	}
	modelOptions := func(withMagpie bool) []Option {
		var own []Option
		if p := get("model_provider"); p != "" && p != magpieID {
			own = group(p, options(catalog.Codex(), ""))
		} else {
			own = group("OpenAI", options(ownCodex(), ""))
		}
		if !withMagpie {
			return own
		}
		return append(own, viaMagpieFor("codex", "")...)
	}
	set := func(v string) error {
		if v == "" {
			if err := dropSubagent(); err != nil {
				return err
			}
			if err := giveTables(); err != nil {
				return err
			}
			// Codex as installed: OpenAI, its own catalog, its default model
			if err := dropBase(); err != nil {
				return err
			}
			if err := edit.DelTOMLTop(path, "model", "model_provider", "model_catalog_json"); err != nil {
				return err
			}
			os.Remove(catalogPath)
			forget(at.key("codex.model"), at.key("codex.effort"), at.key("codex.provider"), at.key("codex.catalog"))
			return nil
		}
		if isMagpie(v) {
			if !routed() {
				stash(map[string]string{at.key("codex.model"): get("model"), at.key("codex.effort"): get("model_reasoning_effort"),
					at.key("codex.provider"): get("model_provider"), at.key("codex.catalog"): get("model_catalog_json")})
			}
			// a ChatGPT account out of allowance keeps the Codex app from
			// sending at all, a magpie model's request too; as a provider
			// of Codex's own, magpie is past that
			if !api() && codexChatGPT(dir) && !codexUsedUp() {
				if err := dropProvider(); err != nil {
					return err
				}
				// for the threads started while magpie was the provider
				if err := putProvider(); err != nil {
					return err
				}
				// the base URL is the built-in provider's, and a catalog
				// file would stand in for the list magpie hands out
				if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
					return err
				}
				if err := edit.SetTOMLTop(path,
					edit.KV{Path: "openai_base_url", Value: at.codexURL()},
					edit.KV{Path: "model", Value: v},
				); err != nil {
					return err
				}
				if err := takeTables(); err != nil {
					return err
				}
				return settle()
			}
			if err := putProvider(); err != nil {
				return err
			}
			if err := edit.WriteAtomic(catalogPath, codexcat.Catalog(magpieModels("codex"))); err != nil {
				return err
			}
			// a thread started on Codex's built-in provider stays on it when
			// opened again, and its model can be switched to one of magpie's
			// there (the Codex app's, ChatGPT Desktop's picker): the base
			// URL sends that one to magpie too, not to the ChatGPT backend,
			// which refuses it (#259). A base URL of the user's own stays.
			kv := []edit.KV{
				{Path: "model_provider", Value: magpieID},
				{Path: "model_catalog_json", Value: at.native(catalogPath)},
				{Path: "model", Value: v},
			}
			if u := get("openai_base_url"); u == "" || viaBase() {
				kv = append(kv, edit.KV{Path: "openai_base_url", Value: at.codexURL()})
			}
			if err := edit.SetTOMLTop(path, kv...); err != nil {
				return err
			}
			if err := takeTables(); err != nil {
				return err
			}
			return settle()
		}
		if err := giveTables(); err != nil {
			return err
		}
		if routed() {
			if err := dropSubagent(); err != nil {
				return err
			}
			if err := dropBase(); err != nil {
				return err
			}
			if err := dropProvider(); err != nil {
				return err
			}
			unstash(at.key("codex.model"))
			var back []edit.KV
			if p := unstash(at.key("codex.provider")); p != "" && p != magpieID {
				back = append(back, edit.KV{Path: "model_provider", Value: p})
			}
			if c := unstash(at.key("codex.catalog")); c != "" && c != at.native(catalogPath) {
				back = append(back, edit.KV{Path: "model_catalog_json", Value: c})
			}
			if e := unstash(at.key("codex.effort")); e != "" {
				back = append(back, edit.KV{Path: "model_reasoning_effort", Value: e})
			}
			if len(back) > 0 {
				if err := edit.SetTOMLTop(path, back...); err != nil {
					return err
				}
			}
		}
		if err := edit.SetTOMLTop(path, edit.KV{Path: "model", Value: v}); err != nil {
			return err
		}
		if err := failover(); err != nil {
			return err
		}
		return settle()
	}

	return atomic(&Agent{
		ID: "codex", Name: "Codex", Icon: "codex-color", Bin: "codex", Dir: dir, Path: path,
		UA: []string{"codex"},
		Sync: func() error {
			if err := failover(); err != nil {
				return err
			}
			// on a magpie model by the base URL alone with no ChatGPT
			// sign-in, as an older magpie left a Codex signed in with an
			// API key: its picker never had magpie's models (#322)
			if m := get("model"); isMagpie(m) && viaBase() && get("model_provider") == "" && !codexChatGPT(dir) {
				return set(m)
			}
			// a table taken away before (by an older magpie) comes back
			// while magpie is wired, for the threads that name it
			if isMagpie(get("model")) && viaBase() && !hasProvider() {
				if err := putProvider(); err != nil {
					return err
				}
			}
			// CC Switch's tables, left from before magpie took them over
			if isMagpie(get("model")) && routed() {
				if err := takeTables(); err != nil {
					return err
				}
			}
			switch {
			case asProvider() && get("model_catalog_json") == at.native(catalogPath):
				// set up by a magpie from before #259: the threads started
				// on Codex's built-in provider reach magpie too
				if get("openai_base_url") == "" {
					if err := edit.SetTOMLTop(path, edit.KV{Path: "openai_base_url", Value: at.codexURL()}); err != nil {
						return err
					}
				}
				b := codexcat.Catalog(magpieModels("codex"))
				if cur, _ := edit.Read(catalogPath); string(cur) != string(b) {
					if err := edit.WriteAtomic(catalogPath, b); err != nil {
						return err
					}
				}
			case viaBase():
				if err := codexStaleCache(filepath.Join(dir, "models_cache.json"), provider.CodexListTag()); err != nil {
					return err
				}
			default:
				return nil
			}
			// a model that now has levels it had none of before (models.dev
			// synced, a vendor's list fetched) keeps an effort it takes
			if isMagpie(get("model")) {
				return settle()
			}
			return nil
		},
		Check: func() string {
			if !isMagpie(get("model")) {
				return ""
			}
			// a profile's settings win over the top level's, magpie's included
			if p := get("profile"); p != "" {
				t, err := edit.GetTOMLTable(path, "profiles."+p)
				if err != nil {
					return err.Error()
				}
				for _, k := range []string{"model", "model_provider", "openai_base_url", "model_catalog_json"} {
					if v, ok := t[k]; ok && v != get(k) {
						return "Codex's profile " + p + " sets its own " + k + " (" + v + "), which Codex takes over magpie's"
					}
				}
			}
			switch {
			case asProvider():
				t, err := edit.GetTOMLTable(path, "model_providers."+magpieID)
				if err != nil {
					return err.Error()
				}
				if t["base_url"] != at.v1() || t["experimental_bearer_token"] != gateway.Token || t["wire_api"] != "responses" {
					return "Codex's [model_providers.magpie] no longer points at magpie's gateway (" + at.v1() + ")"
				}
				if c := get("model_catalog_json"); c != at.native(catalogPath) {
					return "Codex's model_catalog_json is no longer magpie's list"
				}
				if _, err := os.Stat(catalogPath); err != nil {
					return "magpie's model list for Codex (" + catalogPath + ") is gone"
				}
			case viaBase():
				if u := get("openai_base_url"); strings.TrimSuffix(u, "/") != at.codexURL() {
					return "Codex's openai_base_url is " + u + ", not magpie's gateway at " + at.codexURL()
				}
			default:
				return "Codex's config no longer sends its model through magpie (no openai_base_url or model_provider of magpie's), so Codex asks OpenAI for a model OpenAI doesn't have"
			}
			for id, u := range ccSwitchTables() {
				if u != at.v1() {
					return "Codex's [model_providers." + id + "] (CC Switch's) sends to " + u + ", not magpie's gateway: a Codex thread started on it goes there when reopened, a magpie model picked in it too"
				}
			}
			return ""
		},
		// every prompt typed into Codex goes into history.jsonl
		LastUsed: func() time.Time { return lastJSONLTime(filepath.Join(dir, "history.jsonl"), "ts", "text") },
		// the Codex app writes no history.jsonl, but it and the TUI log each
		// model request they open
		Reached: func(since time.Time) (time.Time, string, bool) { return codexReached(dir, since) },
		// the app-server behind the Codex app (and every codex TUI) builds
		// its model list once, at start-up.
		Notice: func() string {
			if Running(`(^|/)codex( |$)`) {
				return "Codex builds its model list at start-up — restart the Codex app (and open codex sessions) to see this."
			}
			return ""
		},
		Fields: []Field{
			{
				Key: "model", Label: "model",
				Get:     func() string { return get("model") },
				Set:     set,
				Options: func(map[string]string) []Option { return modelOptions(true) },
			},
			{
				Key: "effort", Label: "effort",
				// unset, Codex takes the model's default, as the catalog
				// magpie wrote says it — shown as such rather than as none
				Get: func() string {
					if e := get("model_reasoning_effort"); e != "" {
						return e
					}
					if m := get("model"); isMagpie(m) {
						if e := catalog.Efforts(models(), m); len(e) > 0 {
							return codexcat.DefaultEffort(e)
						}
					}
					return ""
				},
				Set: func(v string) error {
					if v == "" {
						return edit.DelTOMLTop(path, "model_reasoning_effort")
					}
					return edit.SetTOMLTop(path, edit.KV{Path: "model_reasoning_effort", Value: v})
				},
				Options: func(cur map[string]string) []Option {
					if e := catalog.Efforts(models(), cur["model"]); len(e) > 0 {
						return static(e...)
					}
					return static("low", "medium", "high", "xhigh")
				},
			},
			{
				// Codex lists only the first few models in the spawn_agent
				// tool it gives the model, its own ahead of magpie's, so a
				// subagent is put on one of magpie's here, where it can't be
				// by the model unless asked by name
				Key: "subagent", Label: "subagents", Quiet: true,
				Get: func() string { v, _ := subagent(); return v },
				Set: func(v string) error {
					if v == "" {
						return edit.DelTOMLKey(path, "agents", "default_subagent_model")
					}
					if isMagpie(v) && !routed() {
						return fmt.Errorf("pick a model through magpie for Codex first; its subagents can then have one of their own")
					}
					if err := edit.SetTOMLKey(path, "agents", "default_subagent_model", v); err != nil {
						return err
					}
					return dropSubEffort()
				},
				Options: func(map[string]string) []Option { return modelOptions(routed()) },
			},
			{
				// [agents] default_subagent_reasoning_effort: unset, a
				// subagent runs at the session's effort, or at its model's
				// default when it has a model of its own
				Key: "subagent_effort", Label: "subagent effort", Quiet: true,
				Get: func() string { v, _ := subEffort(); return v },
				Set: func(v string) error {
					if v == "" {
						return edit.DelTOMLKey(path, "agents", "default_subagent_reasoning_effort")
					}
					if l := subEfforts(); len(l) > 0 && !contains(l, v) {
						return fmt.Errorf("Codex runs %s at %s, not %q: it would refuse to start a subagent", subModel(), strings.Join(l, ", "), v)
					}
					return edit.SetTOMLKey(path, "agents", "default_subagent_reasoning_effort", v)
				},
				Options: func(map[string]string) []Option {
					if e := subEfforts(); len(e) > 0 {
						return static(e...)
					}
					return static("low", "medium", "high", "xhigh")
				},
			},
			{
				// how Codex takes magpie's models: beside its ChatGPT
				// sign-in (openai_base_url), or with magpie as its provider,
				// the Codex app in its API state. Kept in the stash, where
				// set("") leaves it.
				Key: "login", Label: "sign-in", Quiet: true,
				Get: func() string { return stashLoad()[at.key("codex.login")] },
				Set: func(v string) error {
					if v != "" && v != "api" {
						return fmt.Errorf("sign-in is api or empty (ChatGPT), not %q", v)
					}
					stash(map[string]string{at.key("codex.login"): v})
					if m := get("model"); isMagpie(m) {
						return set(m)
					}
					return nil
				},
				Options: func(map[string]string) []Option {
					return []Option{
						{Value: "", Label: "ChatGPT", Note: "magpie's models join Codex's own; Codex stays signed in to ChatGPT"},
						{Value: "api", Label: "magpie API", Note: "magpie is Codex's provider; the Codex app is in its API state, with magpie's models only"},
					}
				},
			},
		},
	}, path, catalogPath)
}

// ccSwitchProvider matches the ids of the provider tables CC Switch writes
// into Codex's config: "custom", and "cc-switch", "cc-switch-2"… from its
// older versions. Its "cc-switch-official" is its own proxy to OpenAI.
var ccSwitchProvider = regexp.MustCompile(`^(custom|cc-switch(-[0-9]+)?)$`)

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// The reasoning efforts the Codex app knows, in its order (the enum of its
// enabled-reasoning-efforts setting), and those it offers while the
// setting is unset.
var (
	codexAppEfforts   = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "persistent"}
	codexAppEffortsOn = []string{"low", "medium", "high", "xhigh", "ultra", "persistent"}
)

// codexEnableEfforts adds the efforts magpie's models take to the Codex
// app's [desktop] enabled-reasoning-efforts in config.toml: its model
// picker offers a model's efforts only when they are there, so a model's
// "max" or "minimal" was never offered (#310). The user's entries stay,
// only what is missing is added, and nothing is written when nothing is;
// an effort the app doesn't know is left out, as it would drop the whole
// setting for it. A [desktop] spelled some other way (inline, dotted
// keys) is left alone.
func codexEnableEfforts(path string, ms []catalog.Model) error {
	raw, err := edit.Read(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		Desktop map[string]any `toml:"desktop"`
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	on := codexAppEffortsOn
	if cfg.Desktop != nil {
		if tables, _ := edit.TOMLTables(path); !slices.Contains(tables, "desktop") {
			return nil
		}
		if v, ok := cfg.Desktop["enabled-reasoning-efforts"]; ok {
			xs, ok := v.([]any)
			if !ok {
				return nil
			}
			on = nil
			for _, x := range xs {
				s, ok := x.(string)
				if !ok {
					return nil
				}
				on = append(on, s)
			}
		}
	}
	add := false
	out := slices.Clone(on)
	for _, e := range codexAppEfforts {
		if slices.Contains(on, e) {
			continue
		}
		for _, m := range ms {
			if slices.Contains(m.Efforts, e) {
				out, add = append(out, e), true
				break
			}
		}
	}
	if !add {
		return nil
	}
	q := make([]string, len(out))
	for i, e := range out {
		q[i] = strconv.Quote(e)
	}
	return edit.SetTOMLKey(path, "desktop", "enabled-reasoning-efforts", edit.Raw("["+strings.Join(q, ", ")+"]"))
}

// ownCodex is Codex's own models, narrowed to the ones ticked on its ChatGPT
// subscription in magpie when any are — the subscription switched off, its
// picks narrow nothing, as it serves no agent anything.
func ownCodex() []catalog.Model {
	ms := catalog.Codex()
	p, err := provider.Find("codex")
	if err != nil || p.Off || len(p.Models) == 0 {
		return ms
	}
	var out []catalog.Model
	for _, m := range ms {
		if slices.Contains(p.Models, m.ID) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return ms
	}
	return out
}

// codexGatewayURL is where Codex's built-in OpenAI provider is pointed to
// reach magpie.
func codexGatewayURL() string { return gateway.URL() + gateway.CodexPath }

// isCodexGateway reports whether an openai_base_url is magpie's, on
// whichever port it listened on then.
func isCodexGateway(u string) bool { return isCodexGatewayOn(u, "127.0.0.1") }

// isCodexGatewayOn is isCodexGateway for a gateway reached at host.
func isCodexGatewayOn(u, host string) bool {
	return strings.HasPrefix(u, "http://"+host+":") && strings.HasSuffix(strings.TrimSuffix(u, "/"), gateway.CodexPath)
}

// codexStaleCache ages Codex's cached model list if it isn't the one magpie
// would hand out now (its ETag carries the list's tag), so the next Codex to
// start asks the gateway again rather than showing the old one until the
// cache ages on its own; the rest of the cache stays as Codex wrote it.
func codexStaleCache(path, tag string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c map[string]json.RawMessage
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	var etag string
	json.Unmarshal(c["etag"], &etag)
	if codexcat.Tagged(etag, tag) {
		return nil
	}
	old := time.Unix(0, 0).UTC().Format(time.RFC3339)
	var at string
	if json.Unmarshal(c["fetched_at"], &at) == nil && at == old {
		return nil
	}
	c["fetched_at"], _ = json.Marshal(old)
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(path, out)
}

// codexUsedUp reports whether the ChatGPT account Codex is signed in to
// has used up its allowance. A var so tests can say.
var codexUsedUp = func() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return provider.CodexUsedUp(ctx)
}

// codexFailover reports whether Codex is signed in to a ChatGPT account
// with more of its accounts on in magpie, behind it.
func codexFailover() bool {
	for _, p := range provider.Accounts() {
		if p.Account != nil && p.Account.Agent == "codex" {
			return len(p.AlsoOn()) > 0
		}
	}
	return false
}

// codexChatGPT reports whether Codex is signed in to a ChatGPT account:
// its built-in provider then runs and asks for the model list at
// openai_base_url. Signed in with an API key alone, it asks for none.
func codexChatGPT(dir string) bool {
	var a struct {
		Tokens struct {
			Access string `json:"access_token"`
		} `json:"tokens"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil || json.Unmarshal(b, &a) != nil {
		return false
	}
	return a.Tokens.Access != ""
}

// codexReached reads Codex's newest model request since a time — the
// newest day of it at most — from the log database the Codex app and the
// TUI share: the address it opened, and whether that was refused (nothing
// listening there). Zero when none is logged.
func codexReached(dir string, since time.Time) (at time.Time, to string, refused bool) {
	logs, _ := filepath.Glob(filepath.Join(dir, "logs_*.sqlite"))
	if len(logs) == 0 {
		return
	}
	slices.Sort(logs)
	db, err := provider.OpenReadOnly(logs[len(logs)-1])
	if err != nil {
		return
	}
	defer db.Close()
	from := max(since.Unix(), time.Now().Add(-24*time.Hour).Unix())
	rows, err := db.Query(`SELECT ts, feedback_log_body FROM logs
		WHERE ts > ? AND target = 'codex_api::endpoint::responses_websocket'
		ORDER BY ts DESC, ts_nanos DESC, id DESC LIMIT 20`, from)
	if err != nil {
		return
	}
	defer rows.Close()
	// newest first: a refusal comes before the attempt it answers
	failedAt := map[string]bool{}
	for rows.Next() {
		var ts int64
		var body string
		if rows.Scan(&ts, &body) != nil {
			continue
		}
		if _, u, ok := strings.Cut(body, "failed to connect to websocket: "); ok {
			if _, u, ok := strings.Cut(u, "url: "); ok && (strings.Contains(body, "Connection refused") || strings.Contains(body, "os error 10061")) {
				failedAt[strings.TrimSpace(u)] = true
			}
			continue
		}
		if _, u, ok := strings.Cut(body, "connecting to websocket: "); ok {
			u = strings.TrimSpace(u)
			return time.Unix(ts, 0), u, failedAt[u]
		}
	}
	return
}
