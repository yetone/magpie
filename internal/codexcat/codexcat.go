// Package codexcat renders models the way Codex describes them: the entries
// of its models.json, for `model_catalog_json` and for the model list magpie
// hands Codex in place of the ChatGPT backend's.
package codexcat

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/settings"
)

// Prompt is what Codex itself sends as the instructions of a model its
// catalog doesn't list (Apache-2.0, openai/codex rust-v0.166.0,
// models-manager/prompt.md: BASE_INSTRUCTIONS, model_info_from_slug). A
// model_catalog_json entry has to carry its own (Codex fails the whole
// catalog on one with neither base_instructions nor
// model_messages.instructions_template), so a model no official entry
// matches carries this one, as official Codex would send for it. Until
// 11-01 every third-party model got core/gpt-5.2-codex_prompt.md, a GPT-5.2
// prompt Codex no longer sends for any model (#3759, #4008, #4783).
//
//go:embed codex_prompt.md
var Prompt string

// bundled is the instructions of each model in Codex's own bundled catalog
// (rust-v0.166.0, models-manager/models.json: model_messages'
// instructions_template), by slug, word for word; TestBundledIsCodexOwn
// checks it against that file in testdata.
//
//go:embed codex_instructions.json
var bundledJSON []byte

// It is read the first time a model asks, not at launch: ~0.5 MB a
// launch that writes no Codex catalog (`magpie statusline`) never uses.
var bundled = sync.OnceValue(func() map[string]string {
	var b struct {
		Instructions map[string]string `json:"instructions"`
	}
	if err := json.Unmarshal(bundledJSON, &b); err != nil {
		panic(err)
	}
	return b.Instructions
})

// longestPrefix is the slug of slugs Codex takes a model's entry from:
// the longest one the model's id starts with (codex-rs models-manager
// find_model_by_longest_prefix), so a dated snapshot or a -mini of a
// model takes that model's. "" when none.
func longestPrefix(id string, slugs func(func(string) bool)) string {
	best := ""
	for s := range slugs {
		if s != "" && strings.HasPrefix(id, s) && len(s) > len(best) {
			best = s
		}
	}
	return best
}

// officialFor is the instructions official Codex sends for the model a
// magpie model serves, and the model_messages they come with, never a
// word of magpie's. The model is the id's own model (mycpa/gpt-6-astra,
// openai/gpt-6-astra on a relay, a found auto/gpt-6.1-sol), matched as
// Codex matches a slug (longestPrefix) against Codex's own entries: those
// of the ChatGPT account it last listed (models_cache.json, own), which
// are this Codex version's and carry model_messages too, else its bundled
// catalog's (bundled, instructions alone, whose model_messages may not be
// the shape an older Codex reads). A routing group may hand a turn to any
// model, and a model no entry matches is unknown to Codex: both get
// Prompt, Codex's for an unknown model.
func officialFor(own map[string]map[string]any, id string) (string, map[string]any) {
	if strings.HasPrefix(id, "group/") {
		return Prompt, nil
	}
	slug := strings.ToLower(id[strings.LastIndex(id, "/")+1:])
	if s := longestPrefix(slug, maps.Keys(own)); s != "" {
		e := own[s]
		msgs, _ := e["model_messages"].(map[string]any)
		text, _ := e["base_instructions"].(string)
		if text == "" {
			text, _ = msgs["instructions_template"].(string)
		}
		if text != "" {
			return text, msgs
		}
	}
	if s := longestPrefix(slug, maps.Keys(bundled())); s != "" {
		return bundled()[s], nil
	}
	return Prompt, nil
}

// Official is the instructions official Codex sends for the OpenAI model
// slug when no list of the account's carries them: its bundled catalog's
// entry for it (matched as Codex matches a slug), else Codex's prompt for
// an unknown model.
func Official(slug string) string {
	text, _ := officialFor(nil, slug)
	return text
}

// DefaultEffort picks the middle of the road: "medium" or "high" when
// offered, else whatever the list starts with.
func DefaultEffort(e []string) string {
	for _, want := range []string{"medium", "high"} {
		if slices.Contains(e, want) {
			return want
		}
	}
	return e[0]
}

// NewSessionEffort is the effort a new Codex session starts at, as the
// top of this machine's Codex config.toml says it (model_reasoning_effort,
// magpie's "New sessions" effort, or the one Codex's own picker last
// saved); "" when it is unset or the config can't be read.
func NewSessionEffort() string {
	v, _ := edit.GetTOMLTop(filepath.Join(catalog.CodexHome(), "config.toml"), "model_reasoning_effort")
	return v
}

// ModelDefault is the default_reasoning_level of a model of these levels
// with chosen set for new sessions: chosen when the model takes it, else
// the level of its own nearest chosen (catalog.NearestEffort: by
// strength, a tie going up — xhigh on a model of high and max is max), the
// one the gateway would send it at anyway. Codex takes a model's default
// when it is switched to: the CLI's /model offers it first and saves it as
// the effort, and the Codex app keeps the current effort only for a model
// that lists it. With the middle of the road there, a session started at
// xhigh dropped to medium on a switch to a routing group (Creat.wu on
// Discord). Nothing chosen, or nothing near it, keeps DefaultEffort.
func ModelDefault(levels []string, chosen string) string {
	if chosen != "" {
		if n := catalog.NearestEffort(chosen, levels); slices.Contains(levels, n) {
			return n
		}
	}
	return DefaultEffort(levels)
}

// TakesEffort is the effort Codex takes for the model id when none is set:
// the default_reasoning_level of the entry it reads for it. Codex's own
// model and a ChatGPT account's (codex/) keep their entry from Codex's
// models_cache.json, so it is that entry's (gpt-6.1-sol: low); one of
// magpie's other models takes the one Entries writes. "" when the model
// has no levels, or Codex's entry names none.
func TakesEffort(ms []catalog.Model, id string) string {
	e := catalog.Efforts(ms, id)
	if len(e) == 0 {
		return ""
	}
	slug, chatgpt := strings.CutPrefix(id, "codex/")
	if d, ok := cacheLevels()[slug]; ok && (chatgpt || slug == id) {
		return d
	}
	return DefaultEffort(e)
}

// cacheLevels is the default_reasoning_level of each of CacheEntries'
// models, read again only when models_cache.json changes: the window's
// Codex effort field asks TakesEffort per model every 15s poll, and
// reading and decoding the whole cache each time was 238 MB of what a
// live magpie allocated in 37 minutes. Shared: read only.
func cacheLevels() map[string]string {
	levels, _ := filememo.Read("codex cache levels", catalog.CodexModelsCache(), func(b []byte) (map[string]string, error) {
		var cache struct {
			ETag   string `json:"etag"`
			Models []struct {
				Slug        string `json:"slug"`
				Description string `json:"description"`
				Level       any    `json:"default_reasoning_level"`
			} `json:"models"`
		}
		if err := json.Unmarshal(b, &cache); err != nil {
			return nil, err
		}
		out := map[string]string{}
		for _, m := range cache.Models {
			// as CacheEntries keeps them; a level that isn't a string is none
			if m.Slug != "" && !catalog.MagpieAdded(cache.ETag, m.Slug, m.Description) {
				out[m.Slug], _ = m.Level.(string)
			}
		}
		return out, nil
	})
	return levels
}

// Catalog renders models as a whole models.json, for the effort this
// machine's Codex starts new sessions at (NewSessionEffort).
func Catalog(ms []catalog.Model) []byte { return CatalogAt(ms, NewSessionEffort()) }

// CatalogAt renders models as a whole models.json for a Codex that starts
// new sessions at effort ("" for its models' own defaults): each model's
// default_reasoning_level is ModelDefault's.
func CatalogAt(ms []catalog.Model, effort string) []byte { return CatalogUsed(ms, effort, nil) }

// CatalogUsed is CatalogAt with the models Codex used, most recent first,
// and a ChatGPT account's own (ChatGPTOwn), kept where the Codex app reads
// them (FitOwn).
func CatalogUsed(ms []catalog.Model, effort string, used []string) []byte {
	entries := entriesAt(ms, 0, effort)
	FitOwn(entries, used, ChatGPTOwn)
	out := struct {
		Models []any `json:"models"`
	}{Models: entries}
	if out.Models == nil {
		out.Models = []any{}
	}
	b, _ := json.MarshalIndent(out, "", " ")
	return b
}

// ChatGPTOwn reports whether slug is a ChatGPT account's own model in
// magpie's list for Codex (codex/<slug>): Codex's own GPT models, for a
// Codex that reaches them through magpie, which FitOwn keeps in the first
// AppList.
func ChatGPTOwn(slug string) bool { return strings.HasPrefix(slug, "codex/") }

// level, tier and model are an entry of models.json as Entries writes it.
type level struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}
type tier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type model struct {
	Slug         string `json:"slug"`
	DisplayName  string `json:"display_name"`
	Description  string `json:"description"`
	Instructions string `json:"base_instructions"`
	// the model_messages of the official entry Instructions came from,
	// when that is one Codex itself was handed (officialFor): the rest of
	// what official Codex sends that model, as it sends it
	Messages      map[string]any `json:"model_messages,omitempty"`
	DefaultEffort *string        `json:"default_reasoning_level"`
	Efforts       []level        `json:"supported_reasoning_levels"`
	Shell         string         `json:"shell_type"`
	Visibility    string         `json:"visibility"`
	InAPI         bool           `json:"supported_in_api"`
	Priority      int            `json:"priority"`
	Verbosity     bool           `json:"support_verbosity"`
	DefVerbosity  *string        `json:"default_verbosity"`
	ApplyPatch    string         `json:"apply_patch_tool_type"`
	Truncation    struct {
		Mode  string `json:"mode"`
		Limit int    `json:"limit"`
	} `json:"truncation_policy"`
	Tools      []string `json:"experimental_supported_tools"`
	Modalities []string `json:"input_modalities"`
	// the model's whole window, which Codex shows (95% of it) as the
	// context the conversation has
	Context *int `json:"context_window,omitempty"`
	// the share of the window Codex counts as the conversation's room, as
	// every entry of its own catalog says (95): an entry without it left
	// a newer Codex with no limit to compact against, so a long
	// conversation ran into the vendor's "too long" (codealc on X)
	EffectivePct int `json:"effective_context_window_percent"`
	// where Codex compacts the conversation when that is before the
	// window: the threshold set on the model or its provider, else
	// Long conversations' (settings.Working). Codex compacts at the
	// lesser of this and 90% of Context, and a
	// model_auto_compact_token_limit the user put in config.toml comes
	// before it. Until 10-30 the threshold went in Context, so a 1M
	// model showed 258K in Codex (01huadalang on Discord).
	AutoCompact *int `json:"auto_compact_token_limit,omitempty"`
	// never written now; kept a field so it is one magpie owns, and
	// Keep doesn't carry an old catalog's over
	MaxContext *int   `json:"max_context_window,omitempty"`
	Tiers      []tier `json:"service_tiers"`
	// Without the search, Codex puts every MCP tool's schema (a
	// ChatGPT sign-in's apps' among them) in every request, 190K
	// tokens before the first word (#258); with it, they are named in
	// tool_search's description and handed over when searched for,
	// as Codex does for its own models. Magpie serves the search to
	// any model as a function (gateway/toolsearch.go). Code mode and
	// Responses Lite stay off: the one has the model write JavaScript
	// against Codex's tools, the other moves the tools and
	// instructions into the input, neither for a model not trained on
	// them.
	SearchTool bool `json:"supports_search_tool"`
	// Required from Codex 0.147 (#298: without it the whole catalog
	// fails to load); later Codex ask for parallel calls whatever it
	// says, so it says what they do.
	Parallel bool `json:"supports_parallel_tool_calls"`
	// Required by Codex before 0.145: without it the whole catalog fails
	// to load ("missing field `supports_reasoning_summaries`", tried on
	// 0.144.0), and it sends a request's reasoning parameters — the effort
	// picked, and the summary it shows as the model's thinking — only for
	// a model whose entry says true (codex-rs client.rs build_reasoning,
	// until openai/codex#32206), so the user had to add it by hand
	// (#1450). Later Codex send them always and ignore the field. True for
	// a model with effort levels; a true the user put in for another stays
	// (Keep).
	Summaries bool `json:"supports_reasoning_summaries" keep:"true"`
	// "v1" only with settings.CodexAgentsV1, on an OpenAI model's
	// entry (see V1); "v2" on a model offering Ultra that no ChatGPT
	// account answers for (catalog.Model.AgentsV2), as Codex's own
	// entry for it says: Ultra hands work to Codex's agents in V2
	// alone, and a magpie-served lead writes their tasks as text.
	MultiAgent string `json:"multi_agent_version,omitempty"`
	// the effort Codex's Ultra sends the model, as Codex's own entry
	// for the same model says it (see agentsEffort); without it Codex
	// sends max (#1108)
	AgentsEffort string `json:"multi_agent_reasoning_effort,omitempty"`
	// the model Codex's auto-review runs on, settings.CodexAutoReview
	// (see AutoReview)
	AutoReview string `json:"auto_review_model_override,omitempty"`
	// "none" on a DeepSeek model, as DeepSeek's own Codex setup says (see
	// deepSeek); Codex's default, auto, otherwise, where a value the user
	// put in by hand stays (Keep).
	Summary string `json:"default_reasoning_summary,omitempty" keep:"unset"`
	// Codex may send an image at its original detail: on a DeepSeek model
	// that takes images, as DeepSeek's setup says. A true the user put in
	// stays (Keep).
	DetailOriginal bool `json:"supports_image_detail_original,omitempty" keep:"true"`
}

// deepSeekModel is a DeepSeek model of the V4 line on any host
// (deepseek/deepseek-v4-pro, a relay's deepseek-ai/deepseek-v4-pro-0813,
// DeepSeek's own deepseek-flash, a found auto/deepseek-flash), named by
// the model's own id after its provider's. A routing group is not one: it
// may hand the turn to any model.
var deepSeekModel = lazyre.New(`(?i)^deepseek-(v[4-9]|v\d\d|flash|pro|chat|reasoner)`)

func deepSeek(id string) bool {
	if strings.HasPrefix(id, "group/") {
		return false
	}
	return deepSeekModel.MatchString(id[strings.LastIndex(id, "/")+1:])
}

// Entries renders models as models.json entries, ranked after the first
// `after`. Only fields Codex requires or that change behaviour are set; the
// rest take Codex's defaults. A model's default effort is ModelDefault's
// for the effort this machine's Codex starts new sessions at.
func Entries(ms []catalog.Model, after int) []any {
	return entriesAt(ms, after, NewSessionEffort())
}

func entriesAt(ms []catalog.Model, after int, effort string) []any {
	own := CacheEntries()
	v1 := V1()
	work := settings.Load().ForAgent("codex")
	autoReview := AutoReviewModel()
	var entries []any
	for i, m := range ms {
		if raw, ok := own[strings.TrimPrefix(m.ID, "codex/")]; ok && strings.HasPrefix(m.ID, "codex/") {
			e := ownEntry(raw, m.ID, shownName(m), after+i+1)
			// the window as magpie resolves it — the one the user set,
			// else the account's list's — not the cache's, which is what
			// magpie last handed Codex (#674)
			Window(e, m.Context, 0)
			if v1 {
				Stamp(e)
			}
			AutoReview(e, autoReview)
			entries = append(entries, e)
			continue
		}
		if e, ok := listedEntry(m, after+i+1, work, autoReview, v1, effort); ok {
			entries = append(entries, e)
			continue
		}
		text, msgs := officialFor(own, m.ID)
		e := model{
			Slug: m.ID, DisplayName: shownName(m), Description: m.Name + " via magpie",
			Instructions: text, Messages: msgs, Efforts: []level{},
			Shell: "unified_exec", Visibility: "list", InAPI: true, Priority: after + i + 1,
			ApplyPatch: "freeform", Tools: []string{}, Modalities: []string{"text"},
			Tiers: []tier{}, SearchTool: true, Parallel: true,
			AutoReview: autoReview, EffectivePct: effectivePct,
		}
		if e.AutoReview == settings.CodexAutoReviewSession {
			e.AutoReview = m.ID
		}
		// Fast mode: a ChatGPT account's GPT model Codex has no entry for,
		// or a group one is in, gets the tier Codex's own catalog gives its
		// GPT models
		if slug, ok := strings.CutPrefix(m.ID, "codex/"); m.Fast || ok && strings.HasPrefix(slug, "gpt-") {
			e.Tiers = append(e.Tiers, fastTier)
		} else if m.OwnTier && (len(m.Tiers) == 0 || len(ownTiers(own, m)) > 0) {
			e.Tiers = ownTiers(own, m)
		} else if len(m.Tiers) > 0 {
			// another magpie's model: the tiers it offers its own Codex
			// (#1234)
			e.Tiers = namedTiers(own, m.Tiers)
		}
		// an OpenAI model: a ChatGPT account's (codex/), or a group one is
		// in (Fast, see provider.codexListed), or one a provider that seals
		// a V2 lead's subagent tasks serves (Seals: a relay of the ChatGPT
		// backend)
		if v1 && (strings.HasPrefix(m.ID, "codex/") || m.Fast || m.Seals) {
			e.MultiAgent = "v1"
		} else if m.AgentsV2 {
			e.MultiAgent = "v2"
		}
		if m.Images {
			e.Modalities = append(e.Modalities, "image")
		} else if !strings.HasPrefix(m.ID, "group/") && !strings.HasPrefix(m.ID, "auto/") { // a routing group, a found one as auto/<model>
			// Codex turns a pasted image away itself, saying only that the
			// model doesn't take images; /model says where that is changed
			// (KimYx0207 on X). The description still ends " via magpie",
			// which is how its entry is told from Codex's own
			// (catalog.MagpieAdded)
			e.Description = m.Name + " (text only: Accepts images is in its provider's Model settings in magpie) via magpie"
		}
		if c := m.Context; c > 0 {
			// the model's or its provider's threshold, else the one for
			// every model (#876); one at or above c is its whole window.
			// A DeepSeek model too, though DeepSeek's own Codex setup
			// compacts at none of its 1M: magpie's 272K came of a
			// DeepSeek report (e950eb080: turns of ~550K waited 70-90s
			// for a first token), and Full window or the model's own
			// Compact at gives it the whole window
			w := work.Working(c)
			if m.Compact > 0 {
				w = min(c, m.Compact)
			}
			e.Context = &c
			if w < c {
				e.AutoCompact = &w
			}
		} else if w := cmp.Or(m.Compact, work.Compact()); w > 0 {
			// a window magpie doesn't know (a relay listing none, a
			// model models.dev lacks): Codex compacts at no point on an
			// entry with neither a window nor a limit, and after the
			// vendor's "too long" it only errors again until a manual
			// /compact (jiangguochao666 on X). The threshold alone is
			// one Codex compacts at (ModelInfo::auto_compact_token_limit)
			e.AutoCompact = &w
		}
		e.Truncation.Mode, e.Truncation.Limit = "tokens", 10000
		if deepSeek(m.ID) {
			// as DeepSeek's own Codex setup has its models (its
			// models.json; Rollkey8 on X): shell_command, the one-shot
			// shell tool of a Codex before 0.16x (0.165 reads it as
			// unified_exec, its only one); low verbosity;
			// no reasoning summary asked for (DeepSeek streams its
			// reasoning as text); Codex's agents in V2; an image at its
			// original detail. Its prompt (base_instructions) is not
			// taken: magpie never rewrites a model's Codex prompt.
			e.Shell = "shell_command"
			low := "low"
			e.Verbosity, e.DefVerbosity = true, &low
			e.Summary = "none"
			if e.MultiAgent == "" {
				e.MultiAgent = "v2"
			}
			// The original detail only on DeepSeek's own API: a relay's
			// may refuse it, as OpenCode Go does with 400 "invalid image
			// detail" (qiu_eth)
			e.DetailOriginal = m.Images && strings.HasPrefix(m.ID, "deepseek/")
		}
		levels := m.Efforts
		if ultraHere(m) {
			// Codex's Ultra on a model of another vendor's (pingqipai3
			// on X): Codex sends the model its own top level for it, max
			// or the highest it lists below (ModelInfo::
			// resolve_reasoning_effort), so the model is asked nothing it
			// doesn't take, and in V2 its lead hands parts of the task to
			// agents of its own unasked (multi_agents.rs: Proactive at
			// Ultra, ExplicitRequestOnly otherwise). A magpie-served lead
			// writes their tasks as text, as Copilot's Ultra does (#656)
			levels = append(slices.Clone(levels), "ultra")
			e.MultiAgent = "v2"
		}
		for _, ef := range levels {
			e.Efforts = append(e.Efforts, level{Effort: ef})
		}
		e.AgentsEffort = agentsEffort(own, m)
		if len(m.Efforts) > 0 {
			d := ModelDefault(m.Efforts, effort)
			e.DefaultEffort = &d
			e.Summaries = true
		}
		entries = append(entries, &e)
	}
	return entries
}

// ultraHere reports whether magpie's entry for m offers Codex's Ultra
// beside its levels: a model that reasons, of a vendor other than OpenAI.
// An OpenAI model has Ultra where OpenAI's own catalog gives it
// (provider.OffersUltra), never added here: GPT-6 Luna stops at max there.
// Nor a model whose lead seals its subagents' tasks, which V2 Ultra needs
// to read (a ChatGPT account's, a group one is in, a relay of the ChatGPT
// backend: Fast, Seals, #141).
func ultraHere(m catalog.Model) bool {
	if len(m.Efforts) == 0 || slices.Contains(m.Efforts, "ultra") || m.Fast || m.Seals || strings.HasPrefix(m.ID, "codex/") {
		return false
	}
	return !openAIModel(m.ID)
}

// openAIModel reports whether id (provider/model, a relay's dated or
// renamed one) names an OpenAI model: a GPT, an o-series one, a Codex one.
func openAIModel(id string) bool {
	slug := strings.ToLower(id[strings.LastIndex(id, "/")+1:])
	return gptModel.MatchString(slug) || strings.Contains(slug, "gpt-") || strings.Contains(slug, "codex")
}

// owned are the keys of an entry magpie writes, or leaves out on purpose:
// every field of model, and the two ownEntry takes out of Codex's own.
// keepTrue are those of them where a true the user set by hand stays over
// magpie's false (keep:"true"). A field tagged keep:"unset" is not owned:
// written on some models only, the user's own on the rest stays, as
// before magpie wrote it anywhere.
var owned, keepTrue = func() (own, yes map[string]bool) {
	own = map[string]bool{"availability_nux": true, "upgrade": true}
	yes = map[string]bool{}
	t := reflect.TypeFor[model]()
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			continue
		}
		own[name] = true
		switch f.Tag.Get("keep") {
		case "true":
			yes[name] = true
		case "unset":
			delete(own, name)
		}
	}
	return own, yes
}()

// Keep is the catalog b, as Catalog renders it, with what the user added by
// hand to the entries of cur, the catalog on disk magpie is about to write
// over: a key of an entry of the same slug that magpie doesn't own, and a
// true where magpie says false of a keepTrue key — supports_reasoning_
// summaries, put in for Codex to send a model's effort and show its
// thinking (#1450). Another key magpie writes takes magpie's value; the
// entry of a model magpie no longer serves goes. b as it is when there is
// nothing to keep, or cur isn't a catalog.
func Keep(cur, b []byte) []byte {
	type list struct {
		Models []map[string]any `json:"models"`
	}
	read := func(raw []byte, l *list) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		return d.Decode(l)
	}
	var was list
	if len(cur) == 0 || read(cur, &was) != nil {
		return b
	}
	add := map[string]map[string]any{}
	for _, e := range was.Models {
		slug, _ := e["slug"].(string)
		if slug == "" {
			continue
		}
		for k, v := range e {
			if !owned[k] || keepTrue[k] && v == true {
				if add[slug] == nil {
					add[slug] = map[string]any{}
				}
				add[slug][k] = v
			}
		}
	}
	if len(add) == 0 {
		return b
	}
	var now list
	if read(b, &now) != nil {
		return b
	}
	kept := false
	for _, e := range now.Models {
		slug, _ := e["slug"].(string)
		for k, v := range add[slug] {
			if was, ok := e[k]; !ok || keepTrue[k] && was != true {
				e[k], kept = v, true
			}
		}
	}
	if !kept {
		return b
	}
	out, err := json.MarshalIndent(now, "", " ")
	if err != nil {
		return b
	}
	return out
}

// datedSuffix is a snapshot's date after a model's id (-2026-09-14,
// -20260914).
var datedSuffix = lazyre.New(`-(\d{4}-\d{2}-\d{2}|\d{8})$`)

// agentsEffort is the multi_agent_reasoning_effort of Codex's own entry for
// the model a third-party one serves (s2a/gpt-6-astra, openai/gpt-6-astra on
// a relay, a dated snapshot): Codex's Ultra sends that effort, and max when
// an entry has none, so the same model under magpie's id would otherwise
// run at max where OpenAI's runs at xhigh (#1108). Only a model offering
// Ultra and the effort itself gets it; "" otherwise.
func agentsEffort(own map[string]map[string]any, m catalog.Model) string {
	if !slices.Contains(m.Efforts, "ultra") {
		return ""
	}
	slug := strings.ToLower(m.ID)
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		slug = slug[i+1:]
	}
	slug = datedSuffix.ReplaceAllString(slug, "")
	ef, _ := own[slug]["multi_agent_reasoning_effort"].(string)
	if ef == "" || !slices.Contains(m.Efforts, ef) {
		return ""
	}
	return ef
}

// gptModel is an OpenAI model's id, as a relay may serve it: gpt-6-sol, o4.
var gptModel = lazyre.New(`^(gpt-|o\d)`)

// ownTiers are the service tiers Codex's own entry gives the model a
// provider the user added by its address serves (openai/gpt-6-sol on a
// relay, a dated snapshot), Ultrafast among them where the entry has it;
// Fast on another GPT model; none on any other.
func ownTiers(own map[string]map[string]any, m catalog.Model) []tier {
	slug := strings.ToLower(m.ID)
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		slug = slug[i+1:]
	}
	slug = datedSuffix.ReplaceAllString(slug, "")
	if raw, ok := own[slug]["service_tiers"].([]any); ok {
		var ts []tier
		for _, r := range raw {
			t, _ := r.(map[string]any)
			id, _ := t["id"].(string)
			if id == "" {
				continue
			}
			name, _ := t["name"].(string)
			desc, _ := t["description"].(string)
			ts = append(ts, tier{ID: id, Name: name, Description: desc})
		}
		if len(ts) > 0 {
			return ts
		}
	}
	if gptModel.MatchString(slug) {
		return []tier{{ID: "priority", Name: "Fast", Description: "1.5x speed, increased usage"}}
	}
	return []tier{}
}

// fastTier is Fast as Codex's own catalog writes it on its GPT models.
var fastTier = tier{ID: "priority", Name: "Fast", Description: "1.5x speed, increased usage"}

// namedTiers are the tiers ids name, each as Codex's own catalog writes it
// on any of its models, else by its id: Fast's "priority" as Codex names it.
func namedTiers(own map[string]map[string]any, ids []string) []tier {
	ts := []tier{}
	for _, id := range ids {
		t := tier{ID: id, Name: id}
		if id == fastTier.ID {
			t = fastTier
		}
	found:
		for _, slug := range slices.Sorted(maps.Keys(own)) {
			raw, _ := own[slug]["service_tiers"].([]any)
			for _, r := range raw {
				o, _ := r.(map[string]any)
				if o["id"] == id {
					t.Name, _ = o["name"].(string)
					t.Description, _ = o["description"].(string)
					t.Name = cmp.Or(t.Name, id)
					break found
				}
			}
		}
		ts = append(ts, t)
	}
	return ts
}

// ServiceTiers are the service tiers Entries offers Codex on each of ms,
// by id, where it offers any: what this magpie's list tells another magpie
// that has it as its provider, so that one's Codex is offered them too
// (#1234).
func ServiceTiers(ms []catalog.Model) map[string][]tier {
	out := map[string][]tier{}
	for _, e := range Entries(ms, 0) {
		switch e := e.(type) {
		case *model:
			if len(e.Tiers) > 0 {
				out[e.Slug] = e.Tiers
			}
		case map[string]any:
			slug, _ := e["slug"].(string)
			raw, _ := e["service_tiers"].([]any)
			var ts []tier
			for _, r := range raw {
				o, _ := r.(map[string]any)
				id, _ := o["id"].(string)
				if id == "" {
					continue
				}
				name, _ := o["name"].(string)
				desc, _ := o["description"].(string)
				ts = append(ts, tier{ID: id, Name: name, Description: desc})
			}
			if slug != "" && len(ts) > 0 {
				out[slug] = ts
			}
		}
	}
	return out
}

// Order ranks entries — Codex's own, as the backend gives them, and
// magpie's from Entries — in the order the user put them in (#855): Codex
// lists its models by priority, lowest first. The ones at names go first,
// by their place there; the others after them, as they stood. The slice is
// put in that order too.
func Order(entries []any, at map[string]int) {
	type ranked struct {
		i, by, was int
	}
	rs := make([]ranked, len(entries))
	for i, e := range entries {
		slug, was := "", i
		switch o := e.(type) {
		case map[string]any:
			slug, _ = o["slug"].(string)
			switch p := o["priority"].(type) {
			case float64:
				was = int(p)
			case int:
				was = p
			}
		case *model:
			slug, was = o.Slug, o.Priority
		}
		by := len(at)
		if n, ok := at[slug]; ok {
			by = n
		}
		rs[i] = ranked{i, by, was}
	}
	slices.SortStableFunc(rs, func(a, b ranked) int {
		if a.by != b.by {
			return a.by - b.by
		}
		return a.was - b.was
	})
	in := slices.Clone(entries)
	for n, r := range rs {
		switch o := in[r.i].(type) {
		case map[string]any:
			o["priority"] = n + 1
		case *model:
			o.Priority = n + 1
		}
		entries[n] = in[r.i]
	}
}

// AppList is how much of Codex's model list the Codex app and its VS Code
// extension read: each asks its app-server for model/list with
// includeHidden, cursor null and limit 100, and never for the next page
// (ChatGPT.app 26.1013's and openai.chatgpt 26.51021's webview), so a
// model past the 100th by priority is in no picker of theirs and can't be
// chosen there. The CLI's /model lists every one.
const AppList = 100

// Fit ranks entries, Codex's own and magpie's, so the first AppList by
// priority are ones the Codex app can show and the user wants: a hidden
// entry, which no picker shows, goes after every listed one; and when more
// than AppList are listed, the ones used (slugs, most recent first, as
// usage.CodexUsed has them) are brought into the first AppList, in the
// order they stood, in place of the last of the others there. The order
// is otherwise kept, and the priorities numbered 1 to n in it. The slice
// is put in that order too.
func Fit(entries []any, used []string) { FitOwn(entries, used, nil) }

// FitOwn is Fit with Codex's own models (own reports them by slug) kept
// in the first AppList ahead of the ones used, wherever the user's order
// (Order) put them: Codex lists them itself, so a user who didn't take
// them out of its list expects them in the app. An order set while the
// list had none of them names only magpie's, and put every one past the
// 100th (maiya on Discord). A hidden one still goes last.
func FitOwn(entries []any, used []string, own func(slug string) bool) {
	type ranked struct {
		e        any
		slug     string
		was      int
		hidden   bool
		promoted bool
	}
	rs := make([]ranked, len(entries))
	for i, e := range entries {
		r := ranked{e: e, was: i}
		switch o := e.(type) {
		case map[string]any:
			r.slug, _ = o["slug"].(string)
			v, _ := o["visibility"].(string)
			r.hidden = v == "hide"
			switch p := o["priority"].(type) {
			case float64:
				r.was = int(p)
			case int:
				r.was = p
			}
		case *model:
			r.slug, r.was, r.hidden = o.Slug, o.Priority, o.Visibility == "hide"
		}
		rs[i] = r
	}
	slices.SortStableFunc(rs, func(a, b ranked) int {
		if a.hidden != b.hidden {
			if a.hidden {
				return 1
			}
			return -1
		}
		return a.was - b.was
	})
	listed := 0
	for _, r := range rs {
		if !r.hidden {
			listed++
		}
	}
	if listed > AppList && (len(used) > 0 || own != nil) {
		n := 0
		// Codex's own first, then the used ones, as room allows
		if own != nil {
			for i := range rs[:listed] {
				if n < AppList && own(rs[i].slug) {
					rs[i].promoted = true
					n++
				}
			}
		}
		at := map[string]int{}
		for i, r := range rs[:listed] {
			at[r.slug] = i
			// a ChatGPT account's own model, which magpie's records name
			// as codex/<slug>
			if _, ok := at["codex/"+r.slug]; !ok {
				at["codex/"+r.slug] = i
			}
		}
		for _, u := range used {
			if i, ok := at[u]; ok && !rs[i].promoted && n < AppList {
				rs[i].promoted = true
				n++
			}
		}
		// the first AppList: the used ones and as many others, from the top,
		// as there is room for, in the order they stood
		room := AppList - n
		first, rest := make([]ranked, 0, AppList), make([]ranked, 0, listed)
		for _, r := range rs[:listed] {
			if r.promoted || room > 0 {
				if !r.promoted {
					room--
				}
				first = append(first, r)
				continue
			}
			rest = append(rest, r)
		}
		copy(rs, append(first, rest...))
	}
	for n, r := range rs {
		switch o := r.e.(type) {
		case map[string]any:
			o["priority"] = n + 1
		case *model:
			o.Priority = n + 1
		}
		entries[n] = r.e
	}
}

// CacheEntries is Codex's own models, as models_cache.json describes them
// for the ChatGPT account it last asked with, by slug.
func CacheEntries() map[string]map[string]any {
	b, err := os.ReadFile(catalog.CodexModelsCache())
	if err != nil {
		return nil
	}
	var cache struct {
		ETag   string           `json:"etag"`
		Models []map[string]any `json:"models"`
	}
	if json.Unmarshal(b, &cache) != nil {
		return nil
	}
	out := map[string]map[string]any{}
	for _, m := range cache.Models {
		desc, _ := m["description"].(string)
		if slug, _ := m["slug"].(string); slug != "" && !catalog.MagpieAdded(cache.ETag, slug, desc) {
			out[slug] = m
		}
	}
	if MarkedV1(cache.ETag) {
		// the versions in it are magpie's, written over the backend's: the
		// backend's go back, so a list made from this cache after the
		// setting is turned off says what the backend did
		was := originals()
		for slug, m := range out {
			if v, ok := was[slug]; ok {
				unstamp(m, v)
			}
		}
	} else {
		// as the backend gave them
		Remember(slices.Collect(func(yield func(any) bool) {
			for _, m := range out {
				if !yield(m) {
					return
				}
			}
		}))
	}
	return out
}

// Codex picks a thread's multi-agent tools by its model's entry: its
// multi_agent_version ("v1", "v2") unless features.multi_agent_v2 is on,
// which makes it V2 whatever the entry says. In V2 OpenAI's server seals a
// subagent's task, so a GPT lead can't hand one to a magpie-served
// subagent; in V1 the task goes as text (#141). With settings.CodexAgentsV1
// the OpenAI entries magpie hands Codex say "v1"; nothing else in them
// changes. Codex's own features.multi_agent_v2 still wins over the entry, so
// where it is on in the config magpie routes the setting can't reach Codex:
// magpie says so (internal/agent/codex.go, and the Settings row) and leaves
// the config alone — turning the key off there is the user's. Codex keeps
// what it was handed in models_cache.json, versions and all, so what the
// backend itself said is kept aside (originals) and put back when the cache
// is read again (CacheEntries).

// V1 reports whether the OpenAI models magpie hands Codex say "v1".
func V1() bool { return settings.Load().CodexAgentsV1 }

// Stamp has an entry say multi-agent V1.
func Stamp(e map[string]any) { e["multi_agent_version"] = "v1" }

// unstamp puts back the version an entry had: was, or none when "".
func unstamp(e map[string]any, was string) {
	if was == "" {
		delete(e, "multi_agent_version")
	} else {
		e["multi_agent_version"] = was
	}
}

// Codex's auto-review (the guardian deciding an approval in the user's
// place) runs on the auto_review_model_override of the conversation's
// model's entry, else on codex-auto-review when the list has it, else on the
// conversation's model at low effort (#938). With settings.CodexAutoReview
// every entry magpie hands Codex names that model; set to
// settings.CodexAutoReviewSession, each of magpie's names itself and Codex's
// own are left as OpenAI gave them, so the review runs on the session's
// model.

// SignInPlan is the plan of the ChatGPT account this machine's Codex is
// signed in to, as its ID token names it ("free", "plus", "pro" …), ""
// when it is unknown or Codex isn't signed in (provider.CodexSignInPlan).
var SignInPlan func() string

// AutoReviewModel is the auto-review setting in effect:
// settings.CodexAutoReview, but with nothing picked and Codex signed in
// to a Free ChatGPT plan, the session's (settings.CodexAutoReviewSession).
// Left to Codex the review runs on codex-auto-review, kept as the default
// for being free on a ChatGPT sign-in (#1593, from a paid account); on a
// Free plan it is not (cybercaius on Discord measured it spending the
// plan's allowance), so there it follows the session as Codex's other
// side calls do. A plan magpie can't read keeps Codex's pick.
func AutoReviewModel() string {
	v := settings.Load().CodexAutoReview
	if v == "" && SignInPlan != nil && strings.EqualFold(strings.TrimSpace(SignInPlan()), "free") {
		return settings.CodexAutoReviewSession
	}
	return v
}

// AutoReview has one of Codex's own entries name the auto-review model in
// effect, v (AutoReviewModel, read once for a whole list: it reads Codex's
// sign-in). With none picked, one magpie put there before — a magpie id,
// which has a "/" where OpenAI's slugs have none, kept in Codex's cache — is
// taken out, and one OpenAI gave it is left.
func AutoReview(e map[string]any, v string) {
	if v != "" && v != settings.CodexAutoReviewSession {
		e["auto_review_model_override"] = v
	} else if was, _ := e["auto_review_model_override"].(string); strings.Contains(was, "/") {
		delete(e, "auto_review_model_override")
	}
}

// v1Mark starts the tag of a list whose OpenAI models say V1. It goes before
// the tag's hash, so neither tag is found inside the other (Tagged).
const v1Mark = "v1."

// PolicyTag is a list's tag with the V1 setting in it.
func PolicyTag(tag string) string {
	if V1() {
		return v1Mark + tag
	}
	return tag
}

// MarkedV1 reports whether an ETag is of a list magpie stamped V1.
func MarkedV1(etag string) bool { return strings.Contains(etag, tagMark+v1Mark) }

// versionsPath keeps the multi_agent_version the backend gave each of the
// account's models, "" for none, by slug.
func versionsPath() string { return filepath.Join(appdir.Config(), "codex-agent-versions.json") }

func originals() map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(versionsPath()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// Remember keeps the versions of entries as the backend gave them, before
// any is stamped: those of slugs it names are replaced, the rest kept.
func Remember(entries []any) {
	was := originals()
	changed := false
	for _, e := range entries {
		m, _ := e.(map[string]any)
		slug, _ := m["slug"].(string)
		if slug == "" {
			continue
		}
		v, _ := m["multi_agent_version"].(string)
		if cur, ok := was[slug]; !ok || cur != v {
			was[slug], changed = v, true
		}
	}
	if !changed {
		return
	}
	if b, err := json.MarshalIndent(was, "", "  "); err == nil {
		edit.WriteAtomic(versionsPath(), b)
	}
}

// ownEntry is one of Codex's own models, reached through magpie with the
// ChatGPT sign-in: its entry as Codex has it (images, context window, tools,
// instructions), under magpie's id. The start-up notice and the upgrade
// prompt are left out; they name slugs the catalog does not have.
// effectivePct is the effective_context_window_percent of Codex's own
// entries.
const effectivePct = 95

func ownEntry(raw map[string]any, id, name string, priority int) map[string]any {
	e := make(map[string]any, len(raw))
	for k, v := range raw {
		e[k] = v
	}
	delete(e, "availability_nux")
	delete(e, "upgrade")
	if _, ok := e["effective_context_window_percent"]; !ok {
		e["effective_context_window_percent"] = effectivePct
	}
	e["slug"], e["display_name"], e["priority"], e["visibility"] = id, name, priority, "list"
	if s, _ := e["base_instructions"].(string); s == "" {
		// an entry with model_messages alone carries its instructions
		// there; one with neither, those official Codex sends its model
		msgs, _ := e["model_messages"].(map[string]any)
		s, _ = msgs["instructions_template"].(string)
		if slug, _ := raw["slug"].(string); s == "" {
			s = Official(slug)
		}
		e["base_instructions"] = s
	}
	return e
}

// shownName is the name Codex's picker shows m by: its Name, with what
// tells it from another entry of that name after it (catalog.Model.Apart).
// The picker alone shows it: the description, and the names magpie adds
// to spawn_agent, which go upstream, keep Name.
func shownName(m catalog.Model) string {
	if m.Apart == "" {
		return m.Name
	}
	return m.Name + " · " + m.Apart
}

// listedEntry is the entry m's vendor lists it under in Codex's own shape
// (catalog.Model.Codex, a sub2api's), under magpie's id: as the vendor
// gave it — its instructions, tools, truncation, tiers and the rest, which
// magpie neither guesses nor rewrites — but for what the user set. A
// window, compaction or levels the user gave the model, its images
// switched, and the auto-review model picked in magpie, are said as the
// entries magpie makes say them; with none of them it is the vendor's
// entry, its description ending " via magpie" so it is told from Codex's
// own (catalog.MagpieAdded). False when m has none, or one magpie can't
// read, which is then made as any other model's.
func listedEntry(m catalog.Model, priority int, work settings.Settings, autoReview string, v1 bool, effort string) (map[string]any, bool) {
	var raw map[string]any
	if len(m.Codex) == 0 || json.Unmarshal(m.Codex, &raw) != nil || raw == nil {
		return nil, false
	}
	e := ownEntry(raw, m.ID, shownName(m), priority)
	desc, _ := e["description"].(string)
	e["description"] = cmp.Or(strings.TrimSpace(desc), m.Name) + " via magpie"
	if c := m.Context; c > 0 {
		// the window magpie resolves (the user's, else this entry's own),
		// and where the user has Codex compact it, theirs alone: not
		// magpie's default working window, as the vendor said its own
		Window(e, c, 0)
		w := c
		if m.Compact > 0 {
			w = min(c, m.Compact)
		} else if work.CompactAt > 0 {
			w = work.Working(c)
		}
		if w < c {
			e["auto_compact_token_limit"] = w
		}
	} else if w := m.Compact; w > 0 || work.CompactAt > 0 && !work.FullContext {
		w = cmp.Or(w, work.CompactAt)
		// no window magpie knows: the user's threshold still holds,
		// under the vendor's own window and limit where it says them
		if n, ok := e["context_window"].(float64); ok && n > 0 && int(n) <= w {
			w = 0
		}
		if l, ok := e["auto_compact_token_limit"].(float64); ok && l > 0 && int(l) < w {
			w = 0
		}
		if w > 0 {
			e["auto_compact_token_limit"] = w
		}
	}
	// the levels, where the user's differ from the vendor's: those the
	// vendor lists kept as it wrote them, with their descriptions
	listed, _ := e["supported_reasoning_levels"].([]any)
	byEffort := map[string]any{}
	var had []string
	for _, l := range listed {
		o, _ := l.(map[string]any)
		if ef, _ := o["effort"].(string); ef != "" {
			byEffort[ef] = l
			had = append(had, ef)
		}
	}
	if !slices.Equal(had, m.Efforts) {
		levels := []any{}
		for _, ef := range m.Efforts {
			l, ok := byEffort[ef]
			if !ok {
				l = map[string]any{"effort": ef}
			}
			levels = append(levels, l)
		}
		e["supported_reasoning_levels"] = levels
		if d, _ := e["default_reasoning_level"].(string); !slices.Contains(m.Efforts, d) {
			if len(m.Efforts) == 0 {
				e["default_reasoning_level"] = nil
			} else {
				e["default_reasoning_level"] = ModelDefault(m.Efforts, effort)
			}
		}
	}
	// every level says its description: Codex 0.166 fails the whole
	// catalog on one without the field (#5024), so a level the vendor
	// listed without one — kept as it wrote it, or made for an effort of
	// the user's the vendor doesn't — says the effort's display name
	levels, _ := e["supported_reasoning_levels"].([]any)
	for _, l := range levels {
		o, _ := l.(map[string]any)
		if o == nil {
			continue
		}
		if _, ok := o["description"].(string); ok {
			continue
		}
		ef, _ := o["effort"].(string)
		o["description"] = effortLabel(ef)
	}
	// images, where the user's say (or magpie describing them) differs
	if in, ok := e["input_modalities"].([]any); ok {
		has := slices.Contains(in, any("image"))
		if m.Images && !has {
			e["input_modalities"] = append(slices.Clone(in), "image")
		} else if !m.Images && has {
			e["input_modalities"] = slices.DeleteFunc(slices.Clone(in), func(x any) bool { return x == "image" })
		}
	}
	if v1 && (m.Fast || m.Seals) {
		Stamp(e)
	} else if _, ok := e["multi_agent_version"]; !ok && m.AgentsV2 {
		e["multi_agent_version"] = "v2"
	}
	AutoReview(e, autoReview)
	if autoReview == settings.CodexAutoReviewSession {
		e["auto_review_model_override"] = m.ID
	}
	return e, true
}

// effortLabels are the reasoning efforts' display names, as the pickers say
// them (T3 Code's labels, internal/agent/t3code.go).
var effortLabels = map[string]string{"none": "None", "minimal": "Minimal", "low": "Low", "medium": "Medium", "high": "High", "xhigh": "Extra High", "max": "Max", "ultra": "Ultra"}

// effortLabel is the display name of an effort, the effort itself when it
// has none.
func effortLabel(ef string) string {
	if l, ok := effortLabels[ef]; ok {
		return l
	}
	return ef
}

// Window has one of Codex's own entries say a context window of n tokens
// (none: as it says), and most at the most it may be raised to (0: as it
// says). Only those two fields change. An entry whose max_context_window is
// below its window — one the user set past what OpenAI lists — would say
// two things at once, and Codex may hold the window to the max, so the max
// is raised to it.
func Window(e map[string]any, n, most int) {
	if n <= 0 {
		return
	}
	e["context_window"] = n
	if most > 0 {
		e["max_context_window"] = most
	}
	if cur, ok := e["max_context_window"].(float64); ok && int(cur) < n {
		e["max_context_window"] = n
	} else if cur, ok := e["max_context_window"].(int); ok && cur < n {
		e["max_context_window"] = n
	}
}

// Cap holds one of Codex's own entries to a window of at most n tokens
// (settings.ContextCap), where the user set none for it: its window, the
// most it may be raised to and the threshold it compacts at come down to n
// when they are above it, and nothing else changes.
func Cap(e map[string]any, n int) {
	if n <= 0 {
		return
	}
	for _, k := range []string{"context_window", "max_context_window", "auto_compact_token_limit"} {
		switch v := e[k].(type) {
		case float64:
			if int(v) > n {
				e[k] = n
			}
		case int:
			if v > n {
				e[k] = n
			}
		}
	}
}

// Codex keeps the model list it was handed in models_cache.json, with the
// list's ETag, and asks again only once the cache has aged (minutes) — or
// when a reply's X-Models-Etag differs from it; the same one only makes the
// cache young again. Passed on as the ChatGPT backend gave it, the ETag
// said nothing of magpie's models, so a provider added since stayed out of
// Codex's /model for as long as Codex kept getting replies. The ETag magpie
// hands on carries a tag of its models too.

// Tag names a list of magpie's models: another list, another tag.
func Tag(ms []catalog.Model) string {
	b, _ := json.Marshal(ms)
	b = append(b, catalogFormat...)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

// UsedTag names the set of models Codex used (Fit's used): the list Fit
// makes changes with the set, not with which of them came last.
func UsedTag(used []string) string {
	set := slices.Clone(used)
	slices.Sort(set)
	b, _ := json.Marshal(set)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

// catalogFormat changes when the entries made of the same models change
// shape, so a Codex holding the list magpie gave before asks again: v2 put
// the window in context_window and the threshold in
// auto_compact_token_limit (10-30); v3 offered Ultra on another vendor's
// models that reason (ultraHere).
const catalogFormat = "v3"

// tagMark is where a tag starts in an ETag magpie made.
const tagMark = "+magpie-"

// WithTag is an ETag of the backend's (or none) with magpie's tag in it:
// W/"abc" becomes W/"abc+magpie-<tag>".
func WithTag(etag, tag string) string {
	if strings.HasSuffix(etag, `"`) && len(etag) > 1 {
		return strings.TrimSuffix(etag, `"`) + tagMark + tag + `"`
	}
	return etag + tagMark + tag
}

// Tagged reports whether an ETag carries this tag.
func Tagged(etag, tag string) bool {
	return strings.Contains(etag, tagMark+tag)
}
