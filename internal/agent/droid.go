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
//
// droid judges a custom model by its "model" too: one whose name reads as
// OpenAI's on another provider, or as a Claude off "anthropic", gets a
// warning at every session start (droidWants). Such a model is written on
// the provider droid asks for, and the gateway translates.
//
// A field of one of magpie's entries the user changed in settings.json
// stays theirs (droidKept): what magpie last wrote is stashed, and a value
// other than it is the user's. baseUrl and apiKey are the wiring, put back
// at every sync.
//
// A model that reasons is given reasoningEffort, the effort droid starts
// it at: droid offers a custom model it doesn't know as one of its own
// models no levels at all (droid 1.3.0's B3: ["none"], and no reasoning
// field is sent whatever the user picks), and one with reasoningEffort
// off, low, medium and high, with that one as the default (tao5010 on
// Discord: none of 123 magpie models had thinking levels in /model).
// One whose own id (the last part of magpie's) is a model droid has built
// in is given that id as baseModelId too, so droid takes its levels and
// default from its own table, off included, rather than from a guess at
// the name (droidBuiltins). A routing group gets reasoningEffort only
// when its members share levels (provider's group entries keep the levels
// every member takes), and never a baseModelId: its members are different
// models. Both are droidKept, so a value the user set stays.

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
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
	// ReasoningEffort is the effort droid starts the model at; a model
	// that doesn't reason has none (droidEffort)
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// BaseModelID is the model of droid's own this one is, whose levels
	// droid then offers (droidBuiltin)
	BaseModelID string `json:"baseModelId,omitempty"`
	// droid takes a model on Chat Completions as seeing no images unless
	// told otherwise, so this is always written
	NoImageSupport bool `json:"noImageSupport"`
}

// droidEntries are magpie's custom models as the catalog is now.
func droidEntries() []droidEntry { return droidEntriesAt(gateway.URL()) }

// droidEntriesAt are droidEntries for a droid reaching the gateway at gw.
func droidEntriesAt(gw string) []droidEntry {
	var out []droidEntry
	for _, m := range sharedModels("droid") {
		e := droidEntry{Model: m.ID, ID: droidID + m.ID, APIKey: keyAt(gw),
			Provider: "generic-chat-completion-api", MaxContextLimit: m.Context, MaxOutputTokens: maxTokens(m), NoImageSupport: !m.Images}
		switch want := droidWants(m.ID); {
		case want != "":
			// droid warns at every session start about a model whose name
			// it reads as OpenAI's or Claude's on any other provider
			// (ji233 on Discord: ultrafast/gpt-6.1-sol on Chat)
			e.Provider = want
		case slices.Contains(m.APIs, string(provider.Responses)):
			e.Provider = "openai"
		case slices.Contains(m.APIs, string(provider.Anthropic)):
			e.Provider = "anthropic"
		}
		e.BaseURL = droidBase(gw, e.Provider)
		e.ReasoningEffort = droidEffort(m)
		if e.ReasoningEffort != "" && !provider.IsGroupRef(m.ID) {
			e.BaseModelID = droidBuiltin(m.ID)
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

// droidBase is the baseUrl droid sends a provider's requests to: the
// OpenAI SDKs join their path to /v1, Anthropic's adds the /v1 itself.
func droidBase(gw, prov string) string {
	if prov == "anthropic" {
		return gw
	}
	return gw + "/v1"
}

// droidLevels are the efforts droid's settings take for a custom model
// (droid 1.3.0's reasoningEffort enum) that start it reasoning, in the
// order magpie picks a default from when the model's list names none.
var droidLevels = []string{"medium", "high", "low", "xhigh", "max", "minimal"}

// droidEffort is the reasoningEffort magpie gives m: the effort its
// vendor's list says it starts at, else medium, else the nearest level it
// has. "" for a model that doesn't reason, or a group whose members share
// no levels.
func droidEffort(m catalog.Model) string {
	if provider.IsGroupRef(m.ID) {
		if len(m.Efforts) == 0 {
			return ""
		}
	} else if !m.Reasoning && len(m.Efforts) == 0 {
		return ""
	}
	has := func(l string) bool { return len(m.Efforts) == 0 || slices.Contains(m.Efforts, l) }
	if d := m.DefaultEffort; slices.Contains(droidLevels, d) && has(d) {
		return d
	}
	for _, l := range droidLevels {
		if has(l) {
			return l
		}
	}
	return ""
}

// droidBuiltins are the models droid 1.3.0 has built in (its model id
// enum, the Factory router's "auto" and "auto-fast" aside): the values its
// settings take for baseModelId. droid drops a baseModelId it doesn't know
// and keeps the entry, so an older droid reads one named here as none.
var droidBuiltins = []string{
	"claude-3-5-sonnet-20241022", "claude-3-7-sonnet-20250219",
	"claude-sonnet-4-20250514", "claude-opus-4-1-20250805",
	"claude-3-5-haiku-20241022", "claude-sonnet-4-5-20250929",
	"claude-opus-4-5-20251101", "claude-haiku-4-5-20251001",
	"claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-6-fast",
	"claude-opus-4-7", "claude-opus-4-7-fast", "claude-opus-4-8",
	"claude-opus-4-8-fast", "claude-opus-5", "claude-opus-5-fast",
	"claude-opus-5-5", "claude-opus-5-5-fast", "claude-sonnet-5",
	"claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5",
	"claude-fable-5.1", "atlas-07-21", "aster-07-15", "gpt-5-2025-08-07",
	"gpt-5-mini-2025-08-07", "gpt-5-nano-2025-08-07", "gpt-5-codex",
	"gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-max", "gpt-5.2",
	"gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.3-codex-fast", "gpt-5.4",
	"gpt-5.4-fast", "gpt-5.4-mini", "gpt-5.4-mini-fast", "gpt-5.5",
	"gpt-5.5-fast", "gpt-5.5-pro", "gpt-5.6-sol", "gpt-5.6-sol-fast",
	"gpt-5.6-terra", "gpt-5.6-terra-flex", "gpt-5.6-luna",
	"gpt-5.6-luna-flex", "gpt-6-astra", "gpt-6-astra-ultrafast",
	"gpt-6-astra-flex", "gpt-6.1-sol", "gpt-6.1-sol-ultrafast",
	"gpt-6.1-terra", "gpt-6-sol", "gpt-6-sol-fast", "gpt-6-sol-flex",
	"gpt-6-luna", "gpt-6-luna-fast", "gpt-6-luna-flex", "gemini-2.5-flash",
	"gemini-2.5-pro", "gemini-3-pro-preview", "gemini-3-flash-preview",
	"gemini-3.1-pro-preview", "gemini-3.5-flash", "gemini-3.6-flash",
	"gemini-3.7-flash", "gemini-3.8-flash", "gemini-4-argon",
	"garnet-07-15", "grok-4.5", "grok-4.6", "grok-4.7", "glm-4.6",
	"glm-4.7", "kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code", "kimi-k3",
	"qwen3.8-max", "deepseek-v4.1-flash-1017", "deepseek-v4.1-flash",
	"deepseek-v4-flash-0731", "deepseek-v4-pro", "minimax-m2.5",
	"minimax-m2.7", "minimax-m3", "minimax-m3.1-flash", "glm-5", "glm-5.1",
	"glm-5.2", "glm-5.2-fast", "glm-5.3", "glm-5.3-flash",
	"mistral-medium-3.5", "mistral-large-4", "inkling", "nemotron-3-ultra",
	"nemotron-3.5-super", "muse-spark-1.2", "muse-spark-1.3",
	"muse-spark-open", "beam-1.0",
}

// droidBuiltin is the model of droid's own whose id is the last part of
// magpie's id ref ("codex/gpt-5.5" is gpt-5.5), or "".
func droidBuiltin(ref string) string {
	name := strings.ToLower(ref[strings.LastIndex(ref, "/")+1:])
	if slices.Contains(droidBuiltins, name) {
		return name
	}
	return ""
}

// droid's own reading of a custom model's name (droid 0.231.0, xs and g2
// in its bundle): one that isn't on "openai" while its name matches
// droidGPT is warned about as OpenAI's, and then one on neither
// "anthropic" nor "bedrock-converse" whose name matches droidClaude as
// Claude's. Before the patterns, droid looks the name up among its own
// models (gpt-* are OpenAI's, the rest not); every OpenAI one of those
// matches droidGPT, bar gpt-image and gpt-transcribe, which no chat
// catalog lists.
var (
	droidGPT = []*lazyre.RE{
		lazyre.New(`(?i)\bgpt[-_.]?\d`),
		lazyre.New(`(?i)codex`),
		lazyre.New(`(?i)\bo[1-9](?:[-_.]|$)`),
	}
	droidClaude = lazyre.New(`(?i)claude|haiku|sonnet|opus`)
)

// droidWants is the provider droid asks for a custom model by its "model",
// or "" when it asks for none.
func droidWants(model string) string {
	for _, re := range droidGPT {
		if re.MatchString(model) {
			return "openai"
		}
	}
	if droidClaude.MatchString(model) {
		return "anthropic"
	}
	return ""
}

// droidKept are the fields of magpie's entries a value the user set stays
// in. baseUrl follows the provider; apiKey is the wiring.
var droidKept = []string{"provider", "displayName", "maxContextLimit", "maxOutputTokens", "noImageSupport", "reasoningEffort", "baseModelId"}

// droidWrote is what magpie gave the droidKept fields of an entry, as JSON,
// a field it left out missing.
func droidWrote(e droidEntry) map[string]json.RawMessage {
	b, _ := json.Marshal(e)
	r := gjson.ParseBytes(b)
	out := map[string]json.RawMessage{}
	for _, k := range droidKept {
		if v := r.Get(k); v.Exists() {
			out[k] = json.RawMessage(v.Raw)
		}
	}
	return out
}

// droidMerge is magpie's entry e over the one the file has (had): a
// droidKept field whose value there isn't what magpie last wrote (last)
// keeps the file's, and keys magpie doesn't write stay, droid's "index"
// aside. With no record of the entry, magpie's values are taken: before
// the record, every sync wrote them over anyway.
func droidMerge(gw string, e droidEntry, had gjson.Result, last map[string]json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(e)
	if last == nil {
		return b
	}
	type kv struct {
		k string
		v json.RawMessage
	}
	var out []kv
	ours := map[string]bool{}
	gjson.ParseBytes(b).ForEach(func(k, v gjson.Result) bool {
		ours[k.String()] = true
		out = append(out, kv{k.String(), json.RawMessage(v.Raw)})
		return true
	})
	same := func(a json.RawMessage, b gjson.Result) bool {
		if a == nil || !b.Exists() {
			return a == nil && !b.Exists()
		}
		return reflect.DeepEqual(gjson.ParseBytes(a).Value(), b.Value())
	}
	user := map[string]gjson.Result{}
	for _, k := range droidKept {
		if v := had.Get(k); !same(last[k], v) {
			user[k] = v
		}
	}
	prov := e.Provider
	if v, ok := user["provider"]; ok && v.Exists() {
		prov = v.String()
	}
	var kept []kv
	for _, p := range out {
		switch v, ok := user[p.k]; {
		case p.k == "baseUrl":
			p.v, _ = json.Marshal(droidBase(gw, prov))
		case ok && !v.Exists():
			// the user took it out
			continue
		case ok:
			p.v = json.RawMessage(v.Raw)
		}
		kept = append(kept, p)
	}
	had.ForEach(func(k, v gjson.Result) bool {
		if n := k.String(); !ours[n] && n != "index" {
			kept = append(kept, kv{n, json.RawMessage(v.Raw)})
		}
		return true
	})
	var sb strings.Builder
	sb.WriteByte('{')
	for i, p := range kept {
		if i > 0 {
			sb.WriteByte(',')
		}
		k, _ := json.Marshal(p.k)
		sb.Write(k)
		sb.WriteByte(':')
		sb.Write(p.v)
	}
	sb.WriteByte('}')
	return json.RawMessage(sb.String())
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

func droid(home string) *Agent { return droidIn(here(home)) }

// droidIn is Droid at a place: this machine's home, or a WSL distro's (see
// wsl.go), where FACTORY_HOME_OVERRIDE isn't read and its custom models
// name the gateway as the distro reaches it.
func droidIn(at place) *Agent {
	home := at.home
	if h := at.getenv("FACTORY_HOME_OVERRIDE"); h != "" {
		home = h
	}
	droidEntries := func() []droidEntry { return droidEntriesAt(at.gw()) }
	dir := at.open(filepath.Join(home, ".factory"))
	path := filepath.Join(dir, "settings.json")
	legacy := filepath.Join(dir, "config.json")
	key := "droid:" + path + ":" + droidModel
	// keyWrote is what magpie last gave the droidKept fields of each of
	// its entries, so a value other than it is the user's
	keyWrote := "droid:" + path + ":wrote"
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
		had := map[string]gjson.Result{}
		for _, c := range droidCustoms(path, droidModels) {
			if strings.HasPrefix(c.id, droidID) {
				had[c.id] = gjson.ParseBytes(c.raw)
				continue
			}
			ms = append(ms, c.raw)
		}
		if with {
			saved := stashShared()[keyWrote]
			var last map[string]map[string]json.RawMessage
			json.Unmarshal([]byte(saved), &last)
			wrote := map[string]map[string]json.RawMessage{}
			gw := at.gw()
			for _, e := range droidEntries() {
				wrote[e.ID] = droidWrote(e)
				if h, ok := had[e.ID]; ok {
					ms = append(ms, droidMerge(gw, e, h, last[e.ID]))
				} else {
					ms = append(ms, e)
				}
			}
			if b, _ := json.Marshal(wrote); string(b) != saved {
				stash(map[string]string{keyWrote: string(b)})
			}
		} else if len(had) == 0 {
			return nil
		} else {
			forget(keyWrote)
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
		ID: "droid", Name: "Droid", Icon: "factory", Aliases: []string{"factory", "factory-droid"}, Spelled: prefixed,
		// droid's requests to a custom model carry factory-cli/<version>
		UA:  []string{"factory-cli"},
		Bin: "droid", Dir: dir, Path: path,
		ListOnly: true,
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
				return noticeDroid.String()
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
			base := at.v1()
			if r.Get("provider").String() == "anthropic" {
				base = at.gw()
			}
			return wiringOff("Droid", path, func(k string) (string, bool) { g := r.Get(k); return g.String(), g.Exists() },
				"baseUrl", base, "apiKey", at.gwKey())
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
				return withVia(group("Droid", own), "droid", magpieID+"/")
			},
		}},
	}
}

// what droid says after a change (notice.go)
var (
	noticeDroid = newNotice("Droid reads its settings at start-up — restart open droid sessions to use this.")
)
