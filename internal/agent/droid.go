package agent

// Factory's Droid CLI keeps its settings in ~/.factory/settings.json
// ($FACTORY_HOME_OVERRIDE/.factory when set). Models of the user's own
// (BYOK) are the customModels array, each {model, displayName, baseUrl,
// apiKey, provider, …}, provider being "anthropic" (Messages), "openai"
// (Responses) or "generic-chat-completion-api" (Chat Completions); the
// older ~/.factory/config.json custom_models is still read, merged under
// settings.json, and magpie leaves it alone. A custom model is picked as
// "custom:<id>": its "id" when it has one, else its displayName with
// whitespace as dashes and a count of the same names before it
// ("custom:Kimi-K2-[Groq]-0"). The model sessions start on is
// sessionDefaultSettings.model; a top-level "model", as the docs show it,
// is moved there by droid when that is unset.
//
// magpie appends one entry per catalog model, id "custom:magpie/<ref>", so
// the catalog joins droid's /model picker under Custom models, each on the
// API its provider speaks natively so the gateway relays it as it is, and
// sets sessionDefaultSettings.model; the user's entries stay as they were
// and in front, so their own ids don't move, and the default they had is
// stashed and put back when magpie steps out.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

const (
	droidModels = "customModels"
	droidModel  = "sessionDefaultSettings.model"
	// droidID is how every id magpie gives a custom model begins
	droidID = "custom:" + magpieID + "/"
)

// droidEntry is one custom model magpie writes, in droid's key order.
type droidEntry struct {
	Model           string `json:"model"`
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	BaseURL         string `json:"baseUrl"`
	APIKey          string `json:"apiKey"`
	Provider        string `json:"provider"`
	MaxContextLimit int    `json:"maxContextLimit,omitempty"`
	MaxOutputTokens int    `json:"maxOutputTokens,omitempty"`
	// droid takes a model on Chat Completions as seeing no images unless
	// told otherwise, so this is always written
	NoImageSupport bool `json:"noImageSupport"`
}

// droidEntries are magpie's custom models as the catalog is now.
func droidEntries() []droidEntry {
	var out []droidEntry
	for _, m := range magpieModels("droid") {
		e := droidEntry{Model: m.ID, ID: droidID + m.ID, BaseURL: gatewayV1(), APIKey: gateway.Token,
			Provider: "generic-chat-completion-api", MaxContextLimit: m.Context, MaxOutputTokens: maxTokens(m), NoImageSupport: !m.Images}
		switch {
		case slices.Contains(m.APIs, string(provider.Responses)):
			e.Provider = "openai"
		case slices.Contains(m.APIs, string(provider.Anthropic)):
			// Anthropic's SDK adds the /v1 itself
			e.Provider, e.BaseURL = "anthropic", gateway.URL()
		}
		// the catalog's own label ("pro · DeepSeek") is what the picker
		// shows; droid finds the model by its id, whatever its name
		if e.DisplayName = m.Name; e.DisplayName == "" {
			e.DisplayName = m.ID
		}
		out = append(out, e)
	}
	return out
}

// droidSlug is droid's own spelling of a display name in a model id.
func droidSlug(s string) string {
	return strings.Join(strings.FieldsFunc(strings.TrimSpace(s), unicode.IsSpace), "-")
}

// droidCustom is one custom model as it is in a file: its raw JSON, and the
// id droid knows it by.
type droidCustom struct {
	raw          json.RawMessage
	id, name, of string // of: the model asked for
}

// droidCustoms reads the custom models of a settings file (key
// customModels, camelCase) or a legacy config.json (custom_models).
func droidCustoms(path, key string) []droidCustom {
	raw, err := edit.Read(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var out []droidCustom
	seen := map[string]int{}
	nameKey := "displayName"
	if key == "custom_models" {
		nameKey = "model_display_name"
	}
	gjson.GetBytes(jsonc.ToJSONInPlace(raw), key).ForEach(func(_, v gjson.Result) bool {
		c := droidCustom{raw: json.RawMessage(v.Raw), of: v.Get("model").String(), name: v.Get(nameKey).String()}
		if c.name == "" {
			c.name = c.of
		}
		slug := droidSlug(c.name)
		c.id = v.Get("id").String()
		if c.id == "" {
			c.id = "custom:" + slug + "-" + strconv.Itoa(seen[slug])
		}
		seen[slug]++
		out = append(out, c)
		return true
	})
	return out
}

func droid(home string) *Agent {
	if h := os.Getenv("FACTORY_HOME_OVERRIDE"); h != "" {
		home = h
	}
	dir := filepath.Join(home, ".factory")
	path := filepath.Join(dir, "settings.json")
	legacy := filepath.Join(dir, "config.json")
	key := "droid:" + path + ":" + droidModel
	// raw is the model droid starts on as written: sessionDefaultSettings'
	// or, while that is unset, the top-level one it moves there
	raw := func() string {
		if v, ok := edit.GetJSON(path, droidModel); ok {
			return v
		}
		v, _ := edit.GetJSON(path, "model")
		return v
	}
	// get spells magpie's custom models as the catalog does
	get := func() string {
		v := raw()
		if ref, ok := strings.CutPrefix(v, droidID); ok {
			return magpieID + "/" + ref
		}
		return v
	}
	// setModels writes customModels with the user's own and, when with,
	// magpie's after them; none left takes the key out
	setModels := func(with bool) error {
		var ms []any
		var had bool
		for _, c := range droidCustoms(path, droidModels) {
			if strings.HasPrefix(c.id, droidID) {
				had = true
				continue
			}
			ms = append(ms, c.raw)
		}
		if with {
			for _, e := range droidEntries() {
				ms = append(ms, e)
			}
		} else if !had {
			return nil
		}
		if len(ms) > 0 {
			return edit.SetJSON(path, edit.KV{Path: droidModels, Value: ms})
		}
		return edit.DelJSON(path, droidModels)
	}
	// put sets the default model to v, or takes it out for ""
	put := func(v string) error {
		if v != "" {
			return edit.SetJSON(path, edit.KV{Path: droidModel, Value: v})
		}
		if err := edit.DelJSON(path, droidModel); err != nil {
			return err
		}
		if v, ok := edit.GetJSON(path, "sessionDefaultSettings"); ok && strings.TrimSpace(v) == "{}" {
			return edit.DelJSON(path, "sessionDefaultSettings")
		}
		return nil
	}
	// mine finds a custom model of magpie's by id
	mine := func(id string) (droidCustom, bool) {
		for _, c := range droidCustoms(path, droidModels) {
			if c.id == id {
				return c, true
			}
		}
		return droidCustom{}, false
	}
	return &Agent{
		ID: "droid", Name: "Droid", Icon: "factory", Aliases: []string{"factory", "factory-droid"},
		// droid's requests to a custom model carry factory-cli/<version>
		UA:  []string{"factory-cli"},
		Bin: "droid", Dir: dir, Path: path,
		Sync: func() error {
			for _, c := range droidCustoms(path, droidModels) {
				if strings.HasPrefix(c.id, droidID) {
					return setModels(true)
				}
			}
			return nil
		},
		Notice: func() string {
			if Running(`(^|/)droid( |$)`) {
				return "Droid reads its settings at start-up — restart open droid sessions to use this."
			}
			return ""
		},
		Check: func() string {
			v := raw()
			if !strings.HasPrefix(v, droidID) {
				return ""
			}
			c, ok := mine(v)
			if !ok {
				return "Droid's custom model " + v + " (settings.json) is gone, so it no longer reaches magpie"
			}
			r := gjson.ParseBytes(c.raw)
			base := gatewayV1()
			if r.Get("provider").String() == "anthropic" {
				base = gateway.URL()
			}
			return wiringOff("Droid", path, func(k string) (string, bool) { g := r.Get(k); return g.String(), g.Exists() },
				"baseUrl", base, "apiKey", gateway.Token)
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				cur := raw()
				ours := strings.HasPrefix(cur, droidID)
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !ours {
						if sd, ok := edit.GetJSON(path, droidModel); ok {
							stash(map[string]string{key: sd})
						} else {
							forget(key)
						}
					}
					if err := setModels(true); err != nil {
						return err
					}
					return put(droidID + ref)
				}
				if ours {
					// back to what the user had
					if v == "" {
						v = unstash(key)
					} else {
						forget(key)
					}
				}
				if err := put(v); err != nil {
					return err
				}
				return setModels(false)
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				seen := map[string]bool{}
				for _, c := range append(droidCustoms(path, droidModels), droidCustoms(legacy, "custom_models")...) {
					if seen[c.id] || strings.HasPrefix(c.id, droidID) {
						continue
					}
					seen[c.id] = true
					own = append(own, Option{Value: c.id, Label: c.name, Icon: modelIcon("", c.of)})
				}
				if c := cur["model"]; c != "" && !usesMagpie(c) && !seen[c] {
					own = append([]Option{{Value: c, Icon: modelIcon("", c)}}, own...)
				}
				return append(group("Droid", own), viaMagpie("droid", magpieID+"/")...)
			},
		}},
	}
}
