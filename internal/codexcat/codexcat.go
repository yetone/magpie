// Package codexcat renders models the way Codex describes them: the entries
// of its models.json, for `model_catalog_json` and for the model list magpie
// hands Codex in place of the ChatGPT backend's.
package codexcat

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// Prompt is Codex's generic system prompt (Apache-2.0, openai/codex,
// core/gpt-5.2-codex_prompt.md). Third-party models need one because the
// bundled catalog only carries prompts for OpenAI models.
//
//go:embed codex_prompt.md
var Prompt string

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

// Catalog renders models as a whole models.json.
func Catalog(ms []catalog.Model) []byte {
	out := struct {
		Models []any `json:"models"`
	}{Models: Entries(ms, 0)}
	if out.Models == nil {
		out.Models = []any{}
	}
	b, _ := json.MarshalIndent(out, "", " ")
	return b
}

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
	Slug          string  `json:"slug"`
	DisplayName   string  `json:"display_name"`
	Description   string  `json:"description"`
	Instructions  string  `json:"base_instructions"`
	DefaultEffort *string `json:"default_reasoning_level"`
	Efforts       []level `json:"supported_reasoning_levels"`
	Shell         string  `json:"shell_type"`
	Visibility    string  `json:"visibility"`
	InAPI         bool    `json:"supported_in_api"`
	Priority      int     `json:"priority"`
	Verbosity     bool    `json:"support_verbosity"`
	DefVerbosity  *string `json:"default_verbosity"`
	ApplyPatch    string  `json:"apply_patch_tool_type"`
	Truncation    struct {
		Mode  string `json:"mode"`
		Limit int    `json:"limit"`
	} `json:"truncation_policy"`
	Tools      []string `json:"experimental_supported_tools"`
	Modalities []string `json:"input_modalities"`
	Context    *int     `json:"context_window,omitempty"`
	// the model's whole window when Context is the working one
	// (settings.Working): Codex's model_context_window may raise it
	// that far, as it does OpenAI's own models'
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
}

// Entries renders models as models.json entries, ranked after the first
// `after`. Only fields Codex requires or that change behaviour are set; the
// rest take Codex's defaults.
func Entries(ms []catalog.Model, after int) []any {
	own := CacheEntries()
	v1 := V1()
	work := settings.Load()
	var entries []any
	for i, m := range ms {
		if raw, ok := own[strings.TrimPrefix(m.ID, "codex/")]; ok && strings.HasPrefix(m.ID, "codex/") {
			e := ownEntry(raw, m.ID, m.Name, after+i+1)
			// the window as magpie resolves it — the one the user set,
			// else the account's list's — not the cache's, which is what
			// magpie last handed Codex (#674)
			Window(e, m.Context, 0)
			if v1 {
				Stamp(e)
			}
			AutoReview(e)
			entries = append(entries, e)
			continue
		}
		e := model{
			Slug: m.ID, DisplayName: m.Name, Description: m.Name + " via magpie",
			Instructions: Prompt, Efforts: []level{},
			Shell: "unified_exec", Visibility: "list", InAPI: true, Priority: after + i + 1,
			ApplyPatch: "freeform", Tools: []string{}, Modalities: []string{"text"},
			Tiers: []tier{}, SearchTool: true, Parallel: true,
			AutoReview: work.CodexAutoReview,
		}
		// Fast mode: a ChatGPT account's GPT model Codex has no entry for,
		// or a group one is in, gets the tier Codex's own catalog gives its
		// GPT models
		if slug, ok := strings.CutPrefix(m.ID, "codex/"); m.Fast || ok && strings.HasPrefix(slug, "gpt-") {
			e.Tiers = append(e.Tiers, tier{ID: "priority", Name: "Fast", Description: "1.5x speed, increased usage"})
		} else if m.OwnTier {
			e.Tiers = ownTiers(own, m)
		}
		// an OpenAI model: a ChatGPT account's (codex/), or a group one is
		// in (Fast, see provider.codexListed)
		if v1 && (strings.HasPrefix(m.ID, "codex/") || m.Fast) {
			e.MultiAgent = "v1"
		} else if m.AgentsV2 {
			e.MultiAgent = "v2"
		}
		if m.Images {
			e.Modalities = append(e.Modalities, "image")
		}
		if c := m.Context; c > 0 {
			// the model's or its provider's threshold, else the one for
			// every model (#876); one at or above c is its whole window
			w := work.Working(c)
			if m.Compact > 0 {
				w = min(c, m.Compact)
			}
			e.Context = &w
			if w < c {
				e.MaxContext = &c
			}
		}
		e.Truncation.Mode, e.Truncation.Limit = "tokens", 10000
		for _, ef := range m.Efforts {
			e.Efforts = append(e.Efforts, level{Effort: ef})
		}
		e.AgentsEffort = agentsEffort(own, m)
		if len(m.Efforts) > 0 {
			d := DefaultEffort(m.Efforts)
			e.DefaultEffort = &d
		}
		entries = append(entries, &e)
	}
	return entries
}

// datedSuffix is a snapshot's date after a model's id (-2026-09-14,
// -20260914).
var datedSuffix = regexp.MustCompile(`-(\d{4}-\d{2}-\d{2}|\d{8})$`)

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
var gptModel = regexp.MustCompile(`^(gpt-|o\d)`)

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
// changes. Codex keeps what it was handed in models_cache.json, versions
// and all, so what the backend itself said is kept aside (originals) and
// put back when the cache is read again (CacheEntries).

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
// every entry magpie hands Codex names that model.

// AutoReview has one of Codex's own entries name the auto-review model the
// user picked. With none picked, one magpie put there before — a magpie id,
// which has a "/" where OpenAI's slugs have none, kept in Codex's cache — is
// taken out, and one OpenAI gave it is left.
func AutoReview(e map[string]any) {
	if v := settings.Load().CodexAutoReview; v != "" {
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
func ownEntry(raw map[string]any, id, name string, priority int) map[string]any {
	e := make(map[string]any, len(raw))
	for k, v := range raw {
		e[k] = v
	}
	delete(e, "availability_nux")
	delete(e, "upgrade")
	e["slug"], e["display_name"], e["priority"], e["visibility"] = id, name, priority, "list"
	if s, _ := e["base_instructions"].(string); s == "" {
		e["base_instructions"] = Prompt
	}
	return e
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
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

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
