package agent

// Ante (antigma.ai) takes a provider of one's own in ~/.ante/catalog.json
// (ANTE_HOME moves the folder; the catalog is always $ANTE_HOME/catalog.json),
// merged on top of the providers it ships (docs.antigma.ai,
// reference/catalog-reference):
//
//	{"providers":{"<id>":{"display_name":…,"base_url":…,
//	  "wire_style":"OpenAiCompatible","auth":{"bearer":{"env_key":…}},
//	  "http_headers":{…},"extra_body":{…},
//	  "preferred_models":[{"id":…,"display_name":…,"context_limit":…,
//	    "max_tokens":…,"effort":…,"supported_efforts":[…],
//	    "support_vision":…}]}},
//	 "models":{…}}
//
// The providers map's key is what --provider takes; an id inside an entry is
// ignored. magpie adds the entry "magpie" at the gateway's /v1, on
// OpenAiCompatible — the wire style Ante speaks Chat Completions with, which
// is what the gateway takes every model on — and lists magpie's models in its
// preferred_models, Ante's model picker. The user's own providers and the
// models section are left as they are, and so is everything the user added
// inside magpie's own entry (an auth block, http_headers, extra_body,
// stream_idle_timeout_secs), which is what a gateway shared over the local
// network or reached from WSL needs to send a key (#1103, theirsKept).
//
// No auth of magpie's own: the gateway listens on loopback and lets any token
// in from this machine (gateway.Token's comment, lan.go's identifyCaller), and
// Ante counts a provider with no auth block as authenticated ("Providers with
// no `auth` block are always authenticated", catalog-reference). Writing a
// bearer env_key instead would mean magpie putting a key into Ante's
// environment, which the user would have to do themselves — the same decision
// Empryo's entry makes, which sends no key of the user's either.
//
// Being in the catalog is not enough for Ante to use it. Which provider a
// session starts on is the `provider` and `model` of ~/.ante/settings.json:
// "Explicit --provider and --model flags override the provider and model
// values in ~/.ante/settings.json. When neither source specifies a choice,
// Ante uses the auto-detection rules below… it uses the first authenticated
// provider in catalog order" (catalog-reference). Providers it ships come
// first, so magpie's is one auto-detection never picks while the user has any
// other signed in, and a user who set nothing is on Ante's own provider. So
// magpie sets those two as well and saves the pair it found there, which is
// what steps out puts back — the same pair Empryo's defaultModel and MiniMax
// Code's defaultModel are. A pick the user makes in Ante while magpie is
// wired is theirs and stands; magpie hands them what they moved off, not the
// one it read when it wired in.
//
// Ante reads the catalog once per process (restart to pick an edit up) and
// parses it leniently: an entry that doesn't deserialize is skipped with a
// notice and the rest still load, so a bad file magpie wrote costs the user
// magpie's provider, not their own.

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

const (
	anteProviders = "providers"
	anteProvider  = anteProviders + "." + magpieID
	// anteProviderKey and anteModelKey are the two settings.json keys that
	// decide which provider a session starts on (catalog-reference, "How a
	// model and provider are chosen").
	anteProviderKey = "provider"
	anteModelKey    = "model"
)

// anteDir is the folder Ante keeps catalog.json and settings.json in:
// ANTE_HOME, else ~/.ante (catalog-reference: "~/.ante can be relocated with
// the ANTE_HOME environment variable; the catalog is always
// $ANTE_HOME/catalog.json"). A distro's ANTE_HOME isn't one magpie can read,
// so there it is ~/.ante, as MiniMax Code's mcode (place.getenv).
func anteDir(at place) string {
	if d := strings.TrimSpace(at.getenv("ANTE_HOME")); d != "" {
		return d
	}
	return filepath.Join(at.home, ".ante")
}

func ante(home string) *Agent { return anteAt(here(home)) }

// anteIn is Ante in a WSL distro (see wsl.go).
func anteIn(at place) *Agent { return anteAt(at) }

// anteAt is Ante with its home at at.home, reaching the gateway as at does.
func anteAt(at place) *Agent {
	gatewayV1 := at.v1
	dir := anteDir(at)
	path := filepath.Join(dir, "catalog.json")
	settingsPath := filepath.Join(dir, "settings.json")
	// keyNew marks a file magpie made, which goes again once magpie's own
	// keys are taken out of it and nothing is left
	keyNew := "ante:" + path + ":new"
	keyNewSettings := "ante:" + settingsPath + ":new"
	// the provider and model Ante starts on, stashed for stepping out
	keyProvider := at.key("ante.provider")
	keyModel := at.key("ante.model")
	// wired: magpie's provider is in the catalog
	wired := func() bool { _, ok := edit.GetJSON(path, anteProvider); return ok }
	get := func(k string) string { v, _ := edit.GetJSON(settingsPath, k); return v }
	set := func(k, v string) error {
		if v == "" {
			return edit.DelJSON(settingsPath, k)
		}
		return edit.SetJSON(settingsPath, edit.KV{Path: k, Value: v})
	}
	// entry is magpie's catalog entry, the user's own keys in it kept and
	// preferred_models — magpie's list alone — replaced
	entry := theirsKept(path, anteProvider, func() any { return anteProviderJSON(gatewayV1()) }, "preferred_models")
	// ours is whether Ante starts on magpie. Its settings name the provider
	// and the model apart, so magpie's provider is what makes the pair
	// magpie's; with no model named Ante takes the default of
	// preferred_models, one of magpie's.
	ours := func() bool { return get(anteProviderKey) == magpieID }
	// unwire takes Ante back off magpie: the provider and model it found
	// when it wired in — or nothing, which leaves Ante to its
	// auto-detection — and magpie's catalog entry out. What the user chose
	// in Ante since is theirs and stands, and the pair saved for it goes
	// rather than being written over their pick. Every step is a no-op when
	// there is nothing to undo, which is how this survives Disconnect
	// returning every field to its default: the first call takes the whole
	// wiring out.
	unwire := func() error {
		if get(anteProviderKey) == magpieID {
			was, mine := unstash(keyProvider), unstash(keyModel)
			if err := set(anteProviderKey, was); err != nil {
				return err
			}
			if err := set(anteModelKey, mine); err != nil {
				return err
			}
		} else {
			forget(keyProvider, keyModel)
		}
		// settings.json holding neither of magpie's keys, and magpie having
		// made it, is magpie's file to take away
		if get(anteProviderKey) == "" && get(anteModelKey) == "" && unstash(keyNewSettings) != "" {
			if raw, err := edit.Read(settingsPath); err == nil && len(gjson.ParseBytes(raw).Map()) == 0 {
				if err := edit.Remove(settingsPath); err != nil {
					return err
				}
			}
		}
		if !isFile(path) {
			return nil
		}
		if err := edit.DelJSON(path, anteProvider); err != nil {
			return err
		}
		// a providers map left with nothing goes with it, as Ante reads a
		// missing catalog as the providers it ships
		if raw, err := edit.Read(path); err == nil {
			if m := gjson.GetBytes(raw, anteProviders); m.Exists() && len(m.Map()) == 0 {
				if err := edit.DelJSON(path, anteProviders); err != nil {
					return err
				}
			}
		}
		// a file magpie made goes with its last entry
		if unstash(keyNew) != "" {
			if raw, err := edit.Read(path); err == nil && len(gjson.ParseBytes(raw).Map()) == 0 {
				return edit.Remove(path)
			}
		}
		return nil
	}
	// put is magpie's whole wiring in one step: magpie's catalog entry, with
	// the user's own keys in it kept, and Ante started on magpie. What Ante
	// named as its own provider is saved before magpie writes over it, and
	// only then — a value the user chose in Ante while magpie was wired is
	// theirs and is what comes back. An empty one is no value to restore,
	// which leaves Ante to its auto-detection.
	put := func(model string) error {
		if get(anteProviderKey) != magpieID {
			stash(map[string]string{keyProvider: get(anteProviderKey), keyModel: get(anteModelKey)})
		} else if m := get(anteModelKey); model == "" && isMagpie(m) {
			// already on magpie: the model it was started on stays
			model = m
		}
		if !wired() && !isFile(path) {
			stash(map[string]string{keyNew: "1"})
		}
		if !isFile(settingsPath) {
			stash(map[string]string{keyNewSettings: "1"})
		}
		if err := edit.SetJSON(path, edit.KV{Path: anteProvider, Value: entry()}); err != nil {
			return err
		}
		if err := set(anteProviderKey, magpieID); err != nil {
			return err
		}
		return set(anteModelKey, model)
	}
	return atomic(&Agent{
		ID: "ante", Name: "Ante", Icon: "generic",
		Bin: "ante", Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)ante( |$)`) {
				return "Ante reads its catalog at start-up — restart open ante sessions to use this."
			}
			return ""
		},
		Check: func() string {
			// magpie's entry alone says nothing is off when Ante has been
			// moved to another provider in its own /providers: its settings
			// decide what a session asks (commandcode's modelProvider is the
			// same pair)
			if !wired() {
				return ""
			}
			if !ours() {
				return "Ante's provider (settings.json) is " + orDefault(get(anteProviderKey)) + ", so it no longer asks magpie"
			}
			raw, err := edit.Read(path)
			if err != nil {
				return ""
			}
			e := gjson.GetBytes(raw, anteProvider)
			return wiringOff("Ante", path, func(k string) (string, bool) {
				r := e.Get(k)
				return r.String(), r.Exists()
			}, "base_url", gatewayV1())
		},
		Sync: func() error {
			if !wired() {
				return nil
			}
			// the user's own keys in magpie's entry stay, preferred_models
			// is magpie's list, and a file that says the same as it does is
			// not written again (syncJSON, theirsKept)
			return syncJSON(path, anteProvider, entry)
		},
		Fields: []Field{{
			// This field is magpie's models in Ante's picker: its entry in
			// the catalog, and Ante started on it (settings.json's
			// provider). Get reads the entry alone, as Pencil's does, so
			// Disconnect takes the entry out even when the user has since
			// picked another provider in Ante's own /providers.
			Key: "provider", Label: "provider",
			Get: func() string {
				if wired() {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				if v == "" {
					return unwire()
				}
				return put("")
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in Ante's model picker, and Ante starts on magpie"}}
			},
		}, {
			// Ante names the model on its own, without a provider in front
			// of it (its settings keep the two apart, and its picker lists
			// the ids of preferred_models), so magpie's catalog refs are
			// written as they are — the ids magpie put in preferred_models.
			// A bare id says nothing on its own: read beside magpie's
			// provider only, so an id the user's own provider happens to
			// serve the same way is not taken for one of magpie's.
			Key: "model", Label: "model",
			Get: func() string {
				if ours() {
					return get(anteModelKey)
				}
				return ""
			},
			Set: func(v string) error {
				if v == "" {
					// no model of its own: Ante starts on magpie and takes
					// the default of preferred_models, one of magpie's
					if ours() {
						return set(anteModelKey, "")
					}
					return unwire()
				}
				if !isMagpie(v) {
					// a model of Ante's own: magpie steps out and Ante is
					// left on it — with no provider named, which is how Ante
					// reaches the provider whose list holds that id
					if err := unwire(); err != nil {
						return err
					}
					return set(anteModelKey, v)
				}
				return put(v)
			},
			Options: func(map[string]string) []Option {
				return viaMagpie("ante", "")
			},
		}},
	}, path, settingsPath)
}

// anteProviderJSON is magpie's entry in Ante's catalog: the gateway's /v1 on
// OpenAiCompatible, every magpie model in preferred_models.
//
// A routing group is not told what it reasons at: which member answers, and
// what that member takes, is the group's own to decide per turn, so magpie's
// other agents leave an effort off for one too (magpieModels sets no APIs for
// a group for the same reason). Ante would otherwise ask for an effort the
// member the group picked doesn't have.
//
// support_vision is left alone for every model: Ante takes a model as seeing
// images unless it is told otherwise, and what magpie knows is only as good
// as a provider's own list — a relay's models are often not described at all,
// so writing false would have Ante drop the reader's images before the
// gateway, which describes them to a model that can't see them itself
// (provider.Described). No other agent magpie wires declares this either.
func anteProviderJSON(gw string) map[string]any {
	ms := []map[string]any{}
	for _, m := range magpieModels("ante") {
		ms = append(ms, anteModelJSON(m))
	}
	p := map[string]any{
		"display_name": "magpie",
		// gw is the gateway's /v1 already (place.v1), which is what Ante
		// joins its own path onto
		"base_url":   gw,
		"wire_style": "OpenAiCompatible",
	}
	if len(ms) > 0 {
		p["preferred_models"] = ms
	}
	return p
}

// anteModelJSON is one magpie model as Ante's catalog entry lists it: what
// magpie knows of it, and nothing it doesn't. A routing group is not told
// what it reasons at (anteProviderJSON says why), so its levels are left out
// with the rest of its capabilities.
func anteModelJSON(m catalog.Model) map[string]any {
	group := strings.HasPrefix(m.ID, provider.GroupPrefix)
	e := map[string]any{"id": m.ID}
	if m.Name != "" && m.Name != m.ID {
		e["display_name"] = m.Name
	}
	if m.Context > 0 {
		e["context_limit"] = m.Context
	}
	if m.Output > 0 {
		e["max_tokens"] = maxTokens(m)
	}
	// Ante's ladder is per provider and model family; a model magpie knows
	// the levels of says which of them Ante may send. No default "effort" is
	// written: a model then starts where Ante puts it, and written as the
	// top rung it would make every one of them Ante's slowest and dearest
	// (anteEfforts).
	if !group && len(m.Efforts) > 0 {
		e["supported_efforts"] = anteEfforts(m.Efforts)
	}
	return e
}

// anteLadder is Ante's effort scale, ascending, and the magpie level each
// of its rungs is. Ante's lowest rung, "min", is not a name magpie lists:
// Ante's reference says what it sends it as (docs.antigma.ai,
// reference/catalog-reference, the supported_efforts row):
//
//	"OpenAI-compatible providers send the selected level as the same-named
//	 reasoning_effort (min is sent as minimal)"
//
// so Ante's "min" is magpie's "minimal", and magpie's "none" — no thinking
// asked for at all — has no rung of Ante's, which sends one whatever the
// model was told it accepts. The PR's first version folded both into "min",
// so a model that takes only "none" was asked for `reasoning_effort:
// minimal` and refused.
//
// The other way Ante says a model takes no effort is an empty list:
//
//	"An empty list (supported_efforts: []) declares that the model takes no
//	 effort setting at all."
//
// which is what a model magpie knows only "none" comes out as: Ante then
// shows no slider and sends none, rather than a level the model refuses.
// The default is left off entirely — Ante's own words are
//
//	"effort … Omit for models with no reasoning knob (Ante then sends no
//	 effort parameter and hides the effort slider)"
//
// — so a written default is a sent effort, and setting it to the top rung
// made every model Ante's slowest and dearest one.
var anteLadder = []struct{ ante, magpie string }{
	{"min", "minimal"},
	{"low", "low"},
	{"medium", "medium"},
	{"high", "high"},
	{"xhigh", "xhigh"},
	{"max", "max"},
}

// anteEfforts are the levels Ante is told a model accepts, on Ante's ladder,
// in its order. Empty is the declaration that it takes no effort setting.
func anteEfforts(levels []string) []string {
	out := []string{}
	for _, rung := range anteLadder {
		if slices.Contains(levels, rung.magpie) {
			out = append(out, rung.ante)
		}
	}
	return out
}
