// Package catalog knows which models exist. It reads the models.dev catalog
// (from magpie's own cache or OpenCode's), Codex's model cache, and falls back
// to a small built-in list so the picker is never empty.
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lazyre"
)

// Model is one entry the picker can offer.
type Model struct {
	ID          string // e.g. "claude-sonnet-5"
	Name        string // display name
	Provider    string // models.dev provider id
	Released    string // YYYY-MM-DD, used for ordering
	Efforts     []string
	Temperature *bool  // false when the model refuses temperature/top_p
	Price       *Price // USD per million tokens, when models.dev lists it
	// Keys, for a vendor whose keys each see models of their own, are the
	// keys (by fingerprint) whose list has this one; empty is every key.
	Keys []string `json:",omitempty"`
	// EffortsBorrowed is set when Efforts are not the source's word but
	// borrowed for its id (a plugin's model that thinks with no variants):
	// a model listed by another id (provider's localIDs) borrows them for
	// that id again.
	EffortsBorrowed bool `json:"-"`
	// APIs, when the vendor says, are the APIs the model is served on
	// ("chat", "responses", "anthropic"); empty is not known.
	APIs []string `json:",omitempty"`
	// Endpoints, in magpie's own models for an agent, are the gateway
	// paths a request for it is relayed on as it is
	// (provider.NativeEndpoints, /v1/models' native_endpoints); a group's
	// are those every member relays (provider.GroupEndpoints), which
	// /v1/models leaves out; none for a model translated anyway.
	Endpoints []string `json:"-"`
	// Images is set on a model that takes images as input.
	Images bool `json:",omitempty"`
	// ImageInput is the source's explicit answer; nil means it did not say.
	ImageInput *bool `json:",omitempty"`
	// InputModalities and OutputModalities are what the model takes and
	// gives (text, image, audio, video, pdf) as its source listed them;
	// nil is not said. Images (and the user's override of it) still
	// decides "image" (provider.Entry.Modalities).
	InputModalities  []string `json:",omitempty"`
	OutputModalities []string `json:",omitempty"`
	// Context is how many tokens a prompt may hold, when known: models.dev's
	// input limit, else its context window.
	Context int `json:",omitempty"`
	// MaxContext is the most a prompt may hold when asked for, above
	// Context: Codex's max_context_window (872k on GPT-6, 272k by default).
	MaxContext int `json:",omitempty"`
	// Output is the most tokens a reply may hold, when known.
	Output int `json:",omitempty"`
	// OutputSet is set when Output is a Max output the user typed for the
	// model or its provider (provider.Entry.OutputSet): an agent that trims
	// a vendor's figure for its own reasons hands this one on as it is.
	OutputSet bool `json:"-"`
	// Compact is where the user has Codex compact a conversation on this
	// model (#876), its own or its provider's (settings.ModelCompacts), or
	// Context itself when that is a window the user typed (#1809,
	// provider.CompactFor): the window it is told when Context is longer.
	// 0 is none set, for the one for every model (settings.Compact).
	Compact int `json:",omitempty"`
	// Plain, on a model of another magpie's list, is its name there alone
	// (display_name), when Name is its label there, with that magpie's
	// provider after it (magpie_label): the name Provider in model names
	// Off has here (ARNO on Discord).
	Plain string `json:",omitempty"`
	// Via, on a model of another magpie's list, is the magpies it passes
	// through to its vendor, that one last (magpie_via.go)
	Via []string `json:",omitempty"`
	// Apart, in a list magpie hands Codex, is what tells the model from
	// another entry Codex's picker would call the same (two providers
	// named Codex each serving GPT-6.1-Sol, nb5p on Discord): its
	// provider's id. Said in the entry's display_name alone
	// (codexcat.Entries), which stays in Codex's picker; never in its
	// description or Name, which go upstream in spawn_agent's list of
	// models. Kept in the list's tag, so a change has Codex ask again.
	Apart string `json:",omitempty"`
	// Fast is set on a model Codex may ask for priority processing (its
	// Fast mode): one a ChatGPT account serves.
	Fast bool `json:",omitempty"`
	// Tiers, in one ChatGPT account's own model list, are the service
	// tiers its plan offers on the model ("priority", "ultrafast"); in
	// another magpie's, the tiers it offers its Codex on the model
	// (#1234); in a provider plugin's, the tiers it says its vendor serves
	// the model at (#2514).
	Tiers []string `json:",omitempty"`
	// OwnTier is set on a model of a provider the user added by its
	// address, which the gateway sends the tier Codex asks for as it is
	// (gateway Request.Tier): Codex offers it the tiers of its own entry
	// for the same model, Fast on another GPT model.
	OwnTier bool `json:",omitempty"`
	// CodexFast is set on a GPT model a ChatGPT account serves itself, not
	// through a routing group: one Codex's /fast (service_tier "priority")
	// is asked of, which OpenCode is offered as fast variants (#2379).
	// Not kept with a list: it is the provider's, worked out each time.
	CodexFast bool `json:"-"`
	// AgentsV2 is set on a model Codex is told multi-agent V2 for, so its
	// Ultra hands work to Codex's agents: one offering Ultra that no
	// ChatGPT account answers for (provider.Entry's).
	AgentsV2 bool `json:",omitempty"`
	// Draws is set on a vendor-listed model that makes images (gpt-image-1,
	// a relay's flux): kept with the list for Settings → Models → Images, never
	// offered to agents as a model to talk to.
	Draws bool `json:",omitempty"`
	// Films is set on a model another magpie lists as one it makes videos
	// with (a Remote magpie's Grok Imagine Video): kept with the list for
	// its videos API, never offered as a model to talk to or draw with.
	Films bool `json:",omitempty"`
	// Serves is set on a vendor-listed model made for an API other than
	// chat, images or video (ServesEmbedding, ServesRerank, ServesSpeech,
	// ServesTranscription): kept with the list for that API's routes and
	// their groups of one kind (provider.KindGroup), never offered to
	// agents as a model to talk to (#5339, XYenon on Discord).
	Serves string `json:",omitempty"`
	// Decides marks a decision model another magpie explicitly lists,
	// including models whose ids don't name Jev.
	Decides bool `json:",omitempty"`
	// Free is set on a model a subscription serves at no cost to its
	// allowance: WorkBuddy's "credits": "x0.00".
	Free bool `json:",omitempty"`
	// Rate is what a request costs of a subscription's credits, as a
	// multiple, when its vendor lists it: Qoder's price_factor (0.5),
	// WorkBuddy's "credits": "x0.03". 0 is not listed, or Free.
	Rate float64 `json:",omitempty"`
	// RateWas is the rate before a discount running now, when the vendor
	// says it: Qoder's Qwen3.8-Flash at 0× with 0.1× struck through.
	RateWas float64 `json:",omitempty"`
	// Reasoning is set on a model that thinks, whether or not it takes
	// levels: mimo-v2.6-flash thinks with a switch alone (#402).
	Reasoning bool `json:",omitempty"`
	// AlwaysThinks is set on a model its vendor's list says always thinks,
	// with no way to turn it off: Kimi Code's supports_thinking_type
	// "only" (reasoning.type). Kimi Code is told always_thinking for it.
	AlwaysThinks bool `json:",omitempty"`
	// DynamicTools is set on a model its vendor's list says loads tools
	// mid-conversation: Kimi Code's supports_dynamic_tools
	// (tools.dynamic_load). Kimi Code is told dynamically_loaded_tools.
	DynamicTools bool `json:",omitempty"`
	// DefaultEffort is the effort the vendor's list says the model thinks
	// at when none is asked: Kimi Code's think_efforts.default_effort
	// (reasoning.effort.default). "" is not said.
	DefaultEffort string `json:",omitempty"`
	// WebSearch is how another magpie (a Remote magpie) searches the web
	// for the model when a web search tool is offered: "native", its own
	// provider by itself, or "magpie", with that magpie's searcher; "" it
	// doesn't, or didn't say. A plugin's model it says searches by itself
	// (m.searches) is "native" too.
	WebSearch string `json:",omitempty"`
	// Codex, on a model a vendor lists the way Codex's own catalog does
	// (a sub2api asked with Codex's client_version), is that entry as the
	// vendor gave it (CodexEntry): Codex is handed it as it is, its
	// instructions and window and all, rather than an entry magpie made up
	// from models.dev (ctopsc on Discord). Its context_window,
	// max_context_window, input_modalities and reasoning levels are read
	// into the fields above too, so the rest of magpie says what it says.
	Codex json.RawMessage `json:",omitempty"`
}

// CodexLevels are the efforts of a Codex catalog entry's
// supported_reasoning_levels; false when raw is no such entry.
func CodexLevels(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 || !CodexEntry(raw) {
		return nil, false
	}
	var e struct {
		Levels any `json:"supported_reasoning_levels"`
	}
	json.Unmarshal(raw, &e)
	return levelsOf(e.Levels), true
}

// CodexEntry reports whether raw, one row of a model list, is a model as
// Codex's own catalog lists it (/backend-api/codex/models, codex-rs's
// ModelInfo): a slug, its reasoning levels, its shell and its
// instructions. A plain OpenAI list's row has none of them.
func CodexEntry(raw json.RawMessage) bool {
	var e struct {
		Slug         string          `json:"slug"`
		Levels       json.RawMessage `json:"supported_reasoning_levels"`
		Shell        json.RawMessage `json:"shell_type"`
		Instructions json.RawMessage `json:"base_instructions"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	return e.Slug != "" && len(e.Levels) > 0 && len(e.Shell) > 0 && len(e.Instructions) > 0
}

func imageInput(modalities []string) *bool {
	if modalities == nil {
		return nil
	}
	yes := slices.Contains(Kinds(modalities), "image")
	return &yes
}

// Kinds is a list's modalities as models.dev words them (text, image,
// audio, video, pdf), each once and in the order first met. A vendor may
// list the media types it takes instead: OpenCode Go's /models gives
// deepseek-v4.1-flash ["text","image/jpeg","image/png","image/gif",
// "image/webp"] and Gemini's application/pdf, which read word for word
// were no "image", so every model there that sees was taken for
// text-only (zeo9901 on Discord). An unknown word is kept as it is; nil
// stays nil, a list that said nothing.
func Kinds(modalities []string) []string {
	if modalities == nil {
		return nil
	}
	out := make([]string, 0, len(modalities))
	for _, m := range modalities {
		k := strings.ToLower(strings.TrimSpace(m))
		if typ, _, ok := strings.Cut(k, "/"); ok {
			switch {
			case typ == "image" || typ == "video" || typ == "audio" || typ == "text":
				k = typ
			case k == "application/pdf":
				k = "pdf"
			}
		}
		if k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// Price is what a model costs, in USD per million tokens.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	// CacheWrite1h is what a cache write kept for an hour costs, Anthropic's
	// 1-hour TTL, which it bills at 2× input where a 5-minute write (the
	// CacheWrite above) is 1.25×. 0 is not given: such a write is then
	// counted at the 5-minute price, as no other vendor has an hour's
	// writes to bill apart; a Claude model's is 2× input (OneHourFor).
	CacheWrite1h float64 `json:"cache_write_1h,omitempty"`
	// Tiers are the prices a request is billed at, whole, once its input
	// goes over a size (OpenAI's gpt-6-astra: 2× input and cache, 1.5×
	// output above 272K), lowest first. models.dev lists them as "tiers" of
	// type "context".
	Tiers []Tier `json:"tiers,omitempty"`
	// Peak, when set, says this is an off-peak price, billed at PeakFactor
	// times it in Peak's hours (Price.In): a DeepSeek model's list price at
	// a provider that bills by DeepSeek's hours. A price the user set has
	// none.
	Peak Peak `json:"peak,omitempty"`
}

// Tier is a price from a request's input size on: what the whole request
// costs once its input, cached and cache-written tokens included, is over
// Above tokens.
type Tier struct {
	Above        int     `json:"above"`
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite   float64 `json:"cache_write"`
	CacheWrite1h float64 `json:"cache_write_1h,omitempty"`
}

// UnmarshalJSON reads a price as magpie writes it, and as models.dev does,
// whose tiers name their size as {"tier": {"type": "context", "size": N}}
// and may leave a part out, which is then the base price's. A tier of
// another type, or of no size, is left out.
func (p *Price) UnmarshalJSON(b []byte) error {
	type flat Price
	var raw struct {
		flat
		Tiers []struct {
			Above        int      `json:"above"`
			Input        *float64 `json:"input"`
			Output       *float64 `json:"output"`
			CacheRead    *float64 `json:"cache_read"`
			CacheWrite   *float64 `json:"cache_write"`
			CacheWrite1h *float64 `json:"cache_write_1h"`
			Tier         *struct {
				Type string `json:"type"`
				Size int    `json:"size"`
			} `json:"tier"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*p = Price(raw.flat)
	p.Tiers = nil
	or := func(v *float64, base float64) float64 {
		if v != nil {
			return *v
		}
		return base
	}
	for _, t := range raw.Tiers {
		above := t.Above
		if t.Tier != nil {
			if t.Tier.Type != "" && t.Tier.Type != "context" {
				continue
			}
			above = t.Tier.Size
		}
		if above <= 0 {
			continue
		}
		p.Tiers = append(p.Tiers, Tier{Above: above, Input: or(t.Input, p.Input), Output: or(t.Output, p.Output),
			CacheRead: or(t.CacheRead, p.CacheRead), CacheWrite: or(t.CacheWrite, p.CacheWrite), CacheWrite1h: or(t.CacheWrite1h, 0)})
	}
	slices.SortFunc(p.Tiers, func(a, b Tier) int { return a.Above - b.Above })
	return nil
}

// OneHourFor gives a Claude model's price its 1-hour cache write where none
// is given — models.dev lists none, and a price the user gives may leave it
// out: 2× input, as Anthropic bills it for every model, wherever the model
// has a (5-minute) cache write price at all. Any other model's stays
// unset, its writes all at the 5-minute price: only Anthropic has a 1-hour
// one (PAMI on Discord: gpt-6-astra was shown a $20 one that isn't).
func OneHourFor(id string, p *Price) {
	if p == nil || p.CacheWrite <= 0 || p.CacheWrite1h > 0 || !strings.Contains(strings.ToLower(id), "claude") {
		return
	}
	p.CacheWrite1h = 2 * p.Input
	for i := range p.Tiers {
		if p.Tiers[i].CacheWrite1h <= 0 {
			p.Tiers[i].CacheWrite1h = 2 * p.Tiers[i].Input
		}
	}
}

// Times is the price at r times each of its parts.
func (p Price) Times(r float64) Price {
	out := Price{Input: p.Input * r, Output: p.Output * r, CacheRead: p.CacheRead * r, CacheWrite: p.CacheWrite * r, CacheWrite1h: p.CacheWrite1h * r, Peak: p.Peak}
	for _, t := range p.Tiers {
		out.Tiers = append(out.Tiers, Tier{Above: t.Above, Input: t.Input * r, Output: t.Output * r, CacheRead: t.CacheRead * r, CacheWrite: t.CacheWrite * r, CacheWrite1h: t.CacheWrite1h * r})
	}
	return out
}

// Same is whether two prices are the same in every part and tier.
func (p Price) Same(q Price) bool {
	return p.Input == q.Input && p.Output == q.Output && p.CacheRead == q.CacheRead && p.CacheWrite == q.CacheWrite &&
		p.CacheWrite1h == q.CacheWrite1h && slices.Equal(p.Tiers, q.Tiers) && p.Peak == q.Peak
}

// At is the flat price a request whose input, cached and cache-written
// tokens included, comes to prompt tokens is billed at: that of the highest
// tier it is over, else the base price. OpenAI counts a request's input
// with its cached tokens in it, and bills the whole request at the tier.
func (p Price) At(prompt int) Price {
	flat := Price{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite, CacheWrite1h: p.CacheWrite1h, Peak: p.Peak}
	for _, t := range p.Tiers {
		if prompt > t.Above {
			flat = Price{Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite, CacheWrite1h: t.CacheWrite1h, Peak: p.Peak}
		}
	}
	return flat
}

// OneHour is what a 1-hour cache write costs at this flat price: the price
// given, else the 5-minute one (a Claude model's is given, OneHourFor).
func (p Price) OneHour() float64 {
	if p.CacheWrite1h > 0 {
		return p.CacheWrite1h
	}
	return p.CacheWrite
}

// Cost of a call at this price, every cache write a 5-minute one.
// Reasoning tokens are billed as output by every vendor, and are already
// inside the output count.
func (p Price) Cost(input, output, cacheRead, cacheWrite int) float64 {
	return p.CostSplit(input, output, cacheRead, cacheWrite, 0)
}

// CostSplit is Cost where cacheWrite1h of the cacheWrite tokens were
// written for an hour, billed at that price, and the rest for 5 minutes.
// The whole call is billed at the tier its input reaches.
func (p Price) CostSplit(input, output, cacheRead, cacheWrite, cacheWrite1h int) float64 {
	cacheWrite1h = min(max(cacheWrite1h, 0), max(cacheWrite, 0))
	f := p.At(input + cacheRead + cacheWrite)
	return (float64(input)*f.Input + float64(output)*f.Output +
		float64(cacheRead)*f.CacheRead + float64(cacheWrite-cacheWrite1h)*f.CacheWrite +
		float64(cacheWrite1h)*f.OneHour()) / 1e6
}

type mdProvider struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Env  []string `json:"env"`
	// API is the base URL models.dev says the provider serves its models
	// at (AMD's https://developer.amd.com.cn/radeon/api/v1)
	API    string             `json:"api"`
	Models map[string]mdModel `json:"models"`
}

type mdModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ReleaseDate string `json:"release_date"`
	// Family is the line models.dev puts the model in (gpt-sol, mimo):
	// the levels a new one it lists without any most likely takes
	Family      string `json:"family"`
	Temperature *bool  `json:"temperature"` // false: rejects temperature/top_p
	Thinks      bool   `json:"reasoning"`
	Reasoning   []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
	} `json:"reasoning_options"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost *Price `json:"cost"`
	// Canonical is the model models.dev says this entry is, as
	// "maker/model": DeepSeek lists V4.1 Flash as deepseek-flash, its
	// canonical deepseek/deepseek-v4.1-flash, the id everyone else serves
	// it under (#1520)
	Canonical string `json:"canonical_model_id"`
	// Provider, on a model its vendor serves another way than the rest,
	// names the SDK that talks to it ("@ai-sdk/openai": the Responses API)
	Provider *struct {
		NPM string `json:"npm"`
	} `json:"provider"`
	Limit struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
}

// reasoningOffProviders are models.dev providers whose APIs have verified
// that their toggled models accept the off effort (#1098).
var reasoningOffProviders = map[string]struct{}{
	"deepseek":              {},
	"kimi-code-plan-cn":     {},
	"kimi-code-plan-global": {},
}

// efforts are the reasoning levels models.dev says the model takes. A
// toggle adds the off level only for providers whose APIs verified it;
// other providers keep their listed levels because they may reject "none".
func (m mdModel) efforts(providerID string) []string {
	var out []string
	toggle := false
	for _, r := range m.Reasoning {
		switch r.Type {
		case "toggle":
			toggle = true
		case "effort":
			out = r.Values
		}
	}
	if _, ok := reasoningOffProviders[providerID]; ok && toggle && len(out) > 0 && !slices.Contains(out, "none") {
		return append([]string{"none"}, out...)
	}
	return out
}

// levels are the reasoning levels p gives m: its own, else, for one it
// says thinks but lists without levels — a model just added, as OpenAI's
// gpt-6.1-sol was (NextCommit on Discord) — those of the newest of p's
// thinking models of its family released by then (gpt-6-sol's). A family
// whose models all take a thinking switch alone (Xiaomi's mimo) gives
// none, as before (#214).
func (p mdProvider) levels(m mdModel, providerID string) []string {
	if e := m.efforts(providerID); len(e) > 0 || !m.Thinks || m.Family == "" || m.budgeted() {
		return e
	}
	// the kin is kept by its id, date and levels, not as &x: a pointer to
	// the loop's copy put every model of p on the heap, for every model
	// asked (~6 GB in 48 minutes of a user's magpie, #1956)
	var kinID, kinDate string
	var kin []string
	for id := range p.Models {
		x := p.Models[id]
		if x.Family != m.Family || !x.Thinks || x.ID == m.ID {
			continue
		}
		if m.ReleaseDate != "" && x.ReleaseDate > m.ReleaseDate {
			continue
		}
		if kin != nil && (x.ReleaseDate < kinDate || x.ReleaseDate == kinDate && x.ID <= kinID) {
			continue
		}
		if e := x.efforts(providerID); len(e) > 0 {
			kinID, kinDate, kin = x.ID, x.ReleaseDate, e
		}
	}
	return kin
}

// window is the tokens a prompt to m may hold: the input limit where
// models.dev gives one (gpt-5's 272K of its 400K), else the whole context.
// A row whose input limit sits above its window (Cloudflare AI Gateway's
// gpt-5 at 272K over 128K) holds the window: the cap never exceeds it.
//
// Either limit under LeastWindow says nothing (Window), so the other one
// answers, or none does.
func (m mdModel) window() int {
	in, ctx := Window(m.Limit.Input), Window(m.Limit.Context)
	if in > 0 {
		if ctx > 0 && ctx < in {
			return ctx
		}
		return in
	}
	return ctx
}

// LeastWindow is the smallest context window a list is taken at its word
// for. Less can't hold an agent's system prompt, let alone a turn, and an
// agent told it compacts at once or won't send at all (H20 on Discord: hy3
// and Cline's free models listed at 512, which Claude Code and Codex took
// as the model's window). The smallest real chat models' windows are
// about 4096 (gpt-3.5-turbo-instruct's 4095, mythomax's 4096): those stand.
const LeastWindow = 2048

// Window is n as a model's context window: n, or 0 — not known, for
// models.dev, a family's window or the agent's default to answer — when n
// is under LeastWindow.
func Window(n int) int {
	if n < LeastWindow {
		return 0
	}
	return n
}

var modelsDevURL = "https://models.dev/api.json" // a var for tests

var (
	// loadMu guards loading and Reset: a background Sync (fresh.go) resets
	// while a call is pricing its model
	loadMu sync.Mutex
	loaded bool
	// built are Provider's lists of this load, by provider: dsh's wiring
	// asks for them every 30 seconds, and each built every model again.
	// gen counts Resets, so a list built from an older load isn't kept.
	built map[string][]Model
	gen   int
	mdev  map[string]mdProvider
	// images are the models, by bare id, most of the providers serving
	// them say take images (a few mislabel a text model)
	images map[string]bool
	// windows are the models' context windows, by bare id, as most of the
	// providers serving them give it
	windows map[string]int
	// outputs are the most tokens their replies may hold, likewise
	outputs map[string]int
	// efforts are the models' reasoning levels, by bare id, as most of the
	// providers that give any for them give them
	efforts map[string][]string
	// thinks are the models, by bare id, most of the providers serving
	// them say reason, levels or not
	thinks map[string]bool
	// names are the models' names, by bare id, as most of the providers
	// serving them give it
	names map[string]string
	// listed are the bare ids of every model models.dev lists, whatever it
	// says of them: an id listed is never taken for another (renamed)
	listed map[string]bool
	// labs are the models models.dev lists under their lab alone
	// (models.json), by bare id: none of its providers lists them
	labs map[string]mdModel
	// canonical are the bare ids of every model models.dev says an entry is
	// (canonical_model_id, #1520): DeepSeek's deepseek-flash is
	// deepseek/deepseek-v4.1-flash, the id WorkBuddy and the relays serve it
	// under, which no catalog lists by it. One is a model as a listed one
	// is, so it is never taken for another either (renamed)
	canonical map[string]bool
	// normal is the spelling, of the bare ids models.dev knows (listed,
	// canonical or a lab's), that a model's SameModel name is looked up
	// by when no spelling matches as given: kimi-k3.1-pro for Volcengine
	// Ark's kimi-k3-1-pro-261024 (xyenon on Discord). Of several
	// spellings, the one the most entries give (kimi-k3.1-pro over
	// Venice's kimi-k3-1-pro); a tie goes to the first in order.
	normal map[string]string

	syncCh = make(chan struct{}, 1)
)

// CachePath is where `magpie sync` stores the models.dev catalog.
func CachePath() string { return appdir.Join(appdir.Cache(), "models.json") }

func opencodeCache() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "opencode", "models.json")
}

// Source reports which catalog file is in use ("" when only built-ins are).
func Source() string {
	for _, p := range []string{CachePath(), opencodeCache()} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func load() map[string]mdProvider {
	all, _ := loadGen()
	return all
}

// loadGen is load with the Reset count it was loaded at, read under the
// same lock, so what is built of it is kept for that load alone.
func loadGen() (map[string]mdProvider, int) {
	loadMu.Lock()
	defer loadMu.Unlock()
	if !loaded {
		loaded = true
		func() {
			// magpie's own copy, OpenCode's, then the one magpie ships
			// with (bundled.go): a file that can't be read or parsed is
			// passed over for the next, never read as no catalog
			for _, read := range []func() ([]byte, error){
				func() ([]byte, error) { return os.ReadFile(CachePath()) },
				func() ([]byte, error) { return os.ReadFile(opencodeCache()) },
				func() ([]byte, error) { return bundledCatalog(), nil },
			} {
				b, err := read()
				if err != nil || b == nil {
					continue
				}
				var m map[string]mdProvider
				if json.Unmarshal(b, &m) == nil && len(m) > 0 {
					mdev = m
					votes, reasons := map[string]int{}, map[string]int{}
					named := map[string]map[string]int{}
					sizes, outs := map[string]map[int]int{}, map[string]map[int]int{}
					levels := map[string]map[string]int{}
					voted := map[string]bool{}
					spelt := map[string]int{}
					listed = map[string]bool{}
					canonical = map[string]bool{}
					// vote is what one entry says of a model, counted for
					// the id it names it by: its window, reply limit,
					// thinking, images, levels and name. One vote a
					// provider for each list it gives a model: a gateway
					// listing it under each host it routes to (llmgateway's
					// deepinfra/…, xiaomi/…) votes its levels once, not
					// once a host.
					vote := func(who, own, by string, x mdModel) {
						if routerIDs[who] {
							return
						}
						spelt[who]++
						if x.Name != "" && x.Name != own && x.Name != who {
							if named[who] == nil {
								named[who] = map[string]int{}
							}
							named[who][x.Name]++
						}
						// This vote answers what the catalog lists for the
						// model across providers. A verified provider's toggle
						// support is applied only when that provider itself is
						// queried; it must not leak into relays or aggregates.
						if e := x.efforts(""); len(e) > 0 {
							l := strings.Join(e, ",")
							if levels[who] == nil {
								levels[who] = map[string]int{}
							}
							if k := by + "\x00" + who + "\x00" + l; !voted[k] {
								voted[k] = true
								levels[who][l]++
							}
						}
						if x.Thinks || len(x.efforts("")) > 0 {
							reasons[who]++
						} else {
							reasons[who]--
						}
						if slices.Contains(x.Modalities.Input, "image") {
							votes[who]++
						} else {
							votes[who]--
						}
						if w := x.window(); w > 0 {
							if sizes[who] == nil {
								sizes[who] = map[int]int{}
							}
							sizes[who][w]++
						}
						if o := x.Limit.Output; o > 0 {
							if outs[who] == nil {
								outs[who] = map[int]int{}
							}
							outs[who][o]++
						}
					}
					for pid, p := range m {
						for id, x := range p.Models {
							listed[bareID(id)] = true
							if _, _, ok := strings.Cut(x.Canonical, "/"); ok {
								canonical[bareID(x.Canonical)] = true
							}
							OneHourFor(id, x.Cost)
							vote(bareID(id), id, pid, x)
						}
					}
					// and a model no catalog lists by its id, one entries
					// only name as their canonical_model_id (#1520), takes
					// their word for it too: its window, thinking, images
					// and name are the model's, whoever serves it. One
					// listed by its id keeps the answers it has, so nothing
					// known changes.
					for pid, p := range m {
						for id, x := range p.Models {
							if _, _, ok := strings.Cut(x.Canonical, "/"); !ok {
								continue
							}
							if c := bareID(x.Canonical); !listed[c] {
								vote(c, id, pid, x)
							}
						}
					}
					// and a model no provider lists at all, only its lab
					// (models.dev's models.json): a gated one, as Claude
					// Mythos 5.1 is since models.dev took it out of
					// anthropic's list, that a vendor's own list or the
					// account's still serves (iebb on Discord). Its lab
					// file holds what is the model's on every host — name,
					// window, reply limit, thinking, images — and no
					// price. One a provider lists keeps its answers.
					labs = map[string]mdModel{}
					for id, x := range readLabs() {
						b := bareID(id)
						if listed[b] || canonical[b] {
							continue
						}
						if x.ID == "" {
							x.ID = id
						}
						if _, ok := labs[b]; !ok || id < labs[b].ID {
							labs[b] = x
						}
						vote(b, b, "lab:"+id, x)
					}
					for b := range labs {
						listed[b] = true
					}
					windows = map[string]int{}
					for id, by := range sizes {
						windows[id] = mostGiven(by)
					}
					outputs = map[string]int{}
					for id, by := range outs {
						outputs[id] = mostGiven(by)
					}
					efforts = map[string][]string{}
					for id, by := range levels {
						efforts[id] = strings.Split(mostListed(by), ",")
					}
					normal = map[string]string{}
					for id, n := range spelt {
						k := SameModel(id)
						if was, ok := normal[k]; !ok || n > spelt[was] || n == spelt[was] && id < was {
							normal[k] = id
						}
					}
					names = map[string]string{}
					for id, by := range named {
						names[id] = mostNamed(by)
					}
					thinks = map[string]bool{}
					for id, v := range reasons {
						if v > 0 {
							thinks[id] = true
						}
					}
					images = map[string]bool{}
					for id, v := range votes {
						if v > 0 {
							images[id] = true
						}
					}
					labKinEfforts(m)
					return
				}
			}
		}()
	}
	return mdev, gen
}

// Reset forgets the loaded catalog so the next call re-reads the cache,
// and tells Forget: what the provider package built of the old one (the
// windows and output limits of its entries) is kept between requests
// for a moment, and would answer for the old catalog meanwhile.
func Reset() {
	loadMu.Lock()
	loaded = false
	built = nil
	gen++
	mdev, images, windows, outputs, efforts, thinks, names, listed, canonical, labs, normal = nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil
	loadMu.Unlock()
	if Forget != nil {
		Forget()
	}
}

// Sync downloads the models.dev catalog into CachePath. It serializes with
// itself so a background refresh and a manual one cannot interleave writes.
func Sync(ctx context.Context) error {
	return syncLock(ctx, false)
}

// SyncIfMissing is Sync, but returns nil if a catalog already exists once the
// lock is acquired.
func SyncIfMissing(ctx context.Context) error {
	return syncLock(ctx, true)
}

func syncLock(ctx context.Context, onlyIfMissing bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case syncCh <- struct{}{}:
	}
	defer func() { <-syncCh }()
	if onlyIfMissing && Source() != "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "magpie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("models.dev: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var probe map[string]mdProvider
	if err := json.Unmarshal(b, &probe); err != nil || len(probe) == 0 {
		return fmt.Errorf("models.dev: unexpected payload")
	}
	// written whole through a rename: a reader never sees it half
	// written, and a failed write keeps the last good copy
	if err := writeWhole(CachePath(), b, time.Time{}); err != nil {
		return err
	}
	// the lab catalog beside it is best effort: without it the provider
	// catalog answers alone, and one kept before is kept
	_ = syncLabs(ctx)
	Reset()
	Touched() // names, reasoning levels and context windows may be others
	return nil
}

// Stale reports whether no catalog exists or the cache is older than a day
// (fresh.go), or magpie's has no lab catalog beside it yet (labs.go).
func Stale() bool {
	src := Source()
	if src == "" || labsMissing() {
		return true
	}
	st, err := os.Stat(src)
	return err != nil || time.Since(st.ModTime()) > staleAfter
}

// PriceOf is the list price of a models.dev provider's model, if known.
func PriceOf(providerID, modelID string) (Price, bool) {
	if p, ok := load()[providerID]; ok {
		if m, ok := p.Models[modelID]; ok && m.Cost != nil {
			return *m.Cost, true
		}
	}
	return Price{}, false
}

// PricedBy is the list price the first of providers (models.dev ids)
// pricing a model of this id gives it: by its own key first, then by its
// id without a path or case ("openai/GPT-6-Sol" is gpt-6-sol), a Bedrock
// profile's geography, a "(variant)" or a ":tag"; then with a dot taken
// for a dash, then as the model an entry names as its canonical_model_id
// (DeepSeek's deepseek-flash is deepseek-v4.1-flash).
func PricedBy(providers []string, id string) (Price, bool) {
	all, at := loadGen()
	return pricedOnce(at, 'p', providers, id, func() (Price, bool) { return pricedBy(all, at, providers, id) })
}

func pricedBy(all map[string]mdProvider, at int, providers []string, id string) (Price, bool) {
	for _, pid := range providers {
		if m, ok := all[pid].Models[id]; ok && m.Cost != nil {
			return *m.Cost, true
		}
	}
	b := bareID(id)
	if r, ok := unprofiled(b); ok {
		b = r
	}
	for _, want := range []string{b, cutAt(b, '('), cutAt(b, ':')} {
		for _, pid := range providers {
			if key, ok := priceIndexOf(all, at, pid).bare[want]; ok {
				return *all[pid].Models[key].Cost, true
			}
		}
	}
	// and last with a dot and a dash taken for the same: Copilot and the
	// relays spell Anthropic's models claude-opus-4.6, Anthropic's own entry
	// claude-opus-4-6. Only where nothing above priced it, so a vendor that
	// lists the id as given, at a price of its own, is still the one asked.
	want := dashed(b)
	for _, pid := range providers {
		if key, ok := priceIndexOf(all, at, pid).dashed[want]; ok {
			return *all[pid].Models[key].Cost, true
		}
	}
	// and by what models.dev says an entry is (canonical_model_id): DeepSeek
	// sells V4.1 Flash as deepseek-flash, whose canonical is
	// deepseek/deepseek-v4.1-flash, the id WorkBuddy and the relays serve it
	// under, and lists no deepseek-v4.1-flash of its own (#1520). Only where
	// nothing above priced it, so an id a vendor lists keeps its own price.
	if pr, ok := canonicallyPriced(all, at, providers, b); ok {
		return pr, true
	}
	// and a relay's rename of a model none of them lists as given
	// (claude-deepseek-v4.1-flash[1M] is deepseek-v4.1-flash, #1383)
	if r, _, ok := renamed(b); ok {
		return PricedBy(providers, r)
	}
	return Price{}, false
}

// priceIndex is what PricedBy looks a model up by in one models.dev
// provider, built once a load rather than from every model of the
// provider at each look-up: a model no catalog prices went through every
// model of every vendor asked, lower-cased, five times over, and a model
// list priced row by row (/v1/models' pricing) spent 30ms on it for 250
// models.
type priceIndex struct {
	// bare and dashed are the first key, in key order, of the priced
	// entries with that bare id, and with that bare id's dots as dashes
	bare, dashed map[string]string
	// canon and canonDashed are the price the vendor's own entries under a
	// canonical id (bare, and dashed) give, nil where they disagree
	canon, canonDashed map[string]*Price
}

var priceIndexes struct {
	sync.Mutex
	gen int
	by  map[string]*priceIndex
}

// priceIndexOf is pid's priceIndex in all, the catalog loaded at Reset
// count at.
func priceIndexOf(all map[string]mdProvider, at int, pid string) *priceIndex {
	priceIndexes.Lock()
	defer priceIndexes.Unlock()
	if priceIndexes.by == nil || priceIndexes.gen != at {
		priceIndexes.gen, priceIndexes.by = at, map[string]*priceIndex{}
	}
	if x, ok := priceIndexes.by[pid]; ok {
		return x
	}
	x := &priceIndex{bare: map[string]string{}, dashed: map[string]string{}, canon: map[string]*Price{}, canonDashed: map[string]*Price{}}
	first := func(by map[string]string, k, key string) {
		if was, ok := by[k]; !ok || key < was {
			by[k] = key
		}
	}
	seen := map[*map[string]*Price]map[string]bool{&x.canon: {}, &x.canonDashed: {}}
	agree := func(by *map[string]*Price, k string, p *Price) {
		if !seen[by][k] {
			seen[by][k] = true
			(*by)[k] = p
		} else if was := (*by)[k]; was != nil && !was.Same(*p) {
			(*by)[k] = nil
		}
	}
	for key, m := range all[pid].Models {
		if m.Cost == nil {
			continue
		}
		b := bareID(key)
		first(x.bare, b, key)
		first(x.dashed, dashed(b), key)
		maker, _, ok := strings.Cut(m.Canonical, "/")
		if !ok || !strings.EqualFold(maker, pid) || m.Cost.Same(Price{}) {
			continue
		}
		c := bareID(m.Canonical)
		agree(&x.canon, c, m.Cost)
		agree(&x.canonDashed, dashed(c), m.Cost)
	}
	priceIndexes.by[pid] = x
	return x
}

// canonicallyPriced is the list price the first of providers gives the
// model whose canonical_model_id is b (a bare id), by that id, then with a
// dot and a dash taken for the same. Only a vendor's own model counts — an
// entry whose canonical names the provider listing it as its maker
// (deepseek's deepseek-flash, deepseek/deepseek-v4.1-flash) — and only at
// a price: a reseller's entry under a canonical is often a variant of the
// model, sold at a price of its own (OpenCode's mimo-v2.6-flash-free at $0,
// Baseten's -Fast at twice the price), and a model a relay doesn't list by
// its id is priced at its maker's, as it was. A vendor listing several
// entries under one canonical at different prices names none of them:
// which one was called isn't known.
func canonicallyPriced(all map[string]mdProvider, at int, providers []string, b string) (Price, bool) {
	for _, by := range []func(*priceIndex) (*Price, bool){
		func(x *priceIndex) (*Price, bool) { p, ok := x.canon[b]; return p, ok },
		func(x *priceIndex) (*Price, bool) { p, ok := x.canonDashed[dashed(b)]; return p, ok },
	} {
		for _, pid := range providers {
			if p, ok := by(priceIndexOf(all, at, pid)); ok && p != nil {
				return *p, true
			}
		}
	}
	return Price{}, false
}

// madeBy is who a model is, by models.dev's canonical_model_id: the labs
// that make it, and the providers that list it as their own (ownAPI).
type madeBy struct{ makers, owners []string }

// makerOwners are, by bare id and by that id with its dots as dashes, the
// makers of the models models.dev's providers list and those of the
// providers that are a maker's own API, for the Reset count gen: an entry
// whose canonical_model_id names the provider listing it, or one it is an
// API of, as its maker (deepseek's deepseek-flash, deepseek/deepseek-v4.1-flash-1017;
// meta's muse-spark-1.2; stepfun-ai's step-5-preview), under the entry's
// id and its canonical's. An entry at $0 counts for neither: a maker's
// free tier (nvidia's) is not what the model costs.
var makerOwners struct {
	sync.Mutex
	gen int
	by  map[string]*madeBy
}

// ownAPI is whether the models.dev provider pid is maker's own API: its
// id, or its id and a region or edition (stepfun-ai is StepFun's global
// API, alibaba-cn Alibaba's in China). A plan's catalog ("-plan") is not:
// it lists what a membership charges, not the model's price.
func ownAPI(pid, maker string) bool {
	pid, maker = strings.ToLower(pid), strings.ToLower(maker)
	return (pid == maker || strings.HasPrefix(pid, maker+"-")) && !strings.Contains(pid, "-plan")
}

// madeByOf is who the bare id b is made and listed as their own by, nil
// when no provider lists it as its own.
func madeByOf(all map[string]mdProvider, at int, b string) *madeBy {
	makerOwners.Lock()
	defer makerOwners.Unlock()
	if makerOwners.by == nil || makerOwners.gen != at {
		by := map[string]*madeBy{}
		add := func(k, maker, pid string) {
			for _, k := range []string{k, dashed(k)} {
				x := by[k]
				if x == nil {
					x = &madeBy{}
					by[k] = x
				}
				if !slices.Contains(x.makers, maker) {
					x.makers = append(x.makers, maker)
				}
				if !slices.Contains(x.owners, pid) {
					x.owners = append(x.owners, pid)
				}
			}
		}
		for pid, p := range all {
			for key, m := range p.Models {
				maker, _, ok := strings.Cut(m.Canonical, "/")
				if !ok || !ownAPI(pid, maker) || m.Cost == nil || m.Cost.Same(Price{}) {
					continue
				}
				maker = strings.ToLower(maker)
				add(bareID(key), maker, pid)
				add(bareID(m.Canonical), maker, pid)
			}
		}
		for _, x := range by {
			slices.Sort(x.makers)
			slices.Sort(x.owners)
		}
		makerOwners.gen, makerOwners.by = at, by
	}
	if x := makerOwners.by[b]; x != nil {
		return x
	}
	return makerOwners.by[dashed(b)]
}

// MakerPriced is the list price the model's own maker gives it: priced by
// PricedBy at one provider alone, the first of prefer that is an API of
// the model's maker (ownAPI), else the first by id of the providers that
// list it as their own (madeByOf). A model a lab sells itself is costed at
// the lab's price rather than a reseller's that a maker catalog asked
// before it lists: Mistral sells DeepSeek's deepseek-v4.1-flash-1017 at
// $0.30/$1.20, DeepSeek's own price is $0.15/$0.60 (bestony on Discord: a
// Command Code call costed at the model's official price), and Meta's
// muse-spark-1.2 and Sakana's fugu-ultra, which no maker preset lists, are
// priced too. False when no provider lists it as its own, or none of
// those prices it.
func MakerPriced(prefer []string, id string) (Price, bool) {
	all, at := loadGen()
	return pricedOnce(at, 'm', prefer, id, func() (Price, bool) { return makerPriced(all, at, prefer, id) })
}

func makerPriced(all map[string]mdProvider, at int, prefer []string, id string) (Price, bool) {
	b := bareID(id)
	if r, ok := unprofiled(b); ok {
		b = r
	}
	x := madeByOf(all, at, b)
	if x == nil {
		return Price{}, false
	}
	var asked []string
	for _, pid := range prefer {
		if slices.ContainsFunc(x.makers, func(mk string) bool { return ownAPI(pid, mk) }) {
			asked = append(asked, pid)
		}
	}
	for _, pid := range append(asked, x.owners...) {
		if pr, ok := PricedBy([]string{pid}, id); ok {
			return pr, true
		}
	}
	return Price{}, false
}

// dashed is an id with its dots as dashes: the spelling two ids are compared
// in when one vendor writes a model's version 4.6 and another 4-6.
func dashed(id string) string { return strings.ReplaceAll(id, ".", "-") }

// APIOf is the API a models.dev provider's model is served on, when the
// catalog says it's one of its own: "responses", "anthropic" or "gemini"
// for a model OpenCode serves through OpenAI's, Anthropic's or Google's
// SDK (Gemini's generateContent: Zen's Gemini models, Go's Gemini 4 Argon
// Contributor, #2168), not the OpenAI-compatible one the rest of its
// models go through.
func APIOf(providerID, modelID string) string {
	if p, ok := load()[providerID]; ok {
		if m, ok := p.Models[modelID]; ok && m.Provider != nil {
			switch m.Provider.NPM {
			case "@ai-sdk/openai":
				return "responses"
			case "@ai-sdk/anthropic":
				return "anthropic"
			case "@ai-sdk/google":
				return "gemini"
			}
		}
	}
	return ""
}

// Provider returns the text models of one models.dev provider, newest first.
// The list comes from the synced models.dev catalog; nothing is compiled in.
// A vendor's own /models answer, once fetched, is layered over it by the
// provider package (see provider.Provider.Available).
func Provider(id string) []Model {
	loadMu.Lock()
	out, ok := built[id]
	at := gen
	loadMu.Unlock()
	if ok {
		return slices.Clone(out)
	}
	out = provider(load(), id)
	loadMu.Lock()
	if at == gen {
		if built == nil {
			built = map[string][]Model{}
		}
		built[id] = out
	}
	loadMu.Unlock()
	return slices.Clone(out)
}

func provider(all map[string]mdProvider, id string) []Model {
	p, ok := all[id]
	if !ok {
		return nil
	}
	var out []Model
	for _, m := range p.Models {
		if !textModel(m) {
			continue
		}
		mm := Model{ID: m.ID, Name: m.Name, Provider: id, Released: m.ReleaseDate, Price: m.Cost, Temperature: m.Temperature,
			Images: slices.Contains(m.Modalities.Input, "image"), ImageInput: imageInput(m.Modalities.Input), Context: m.window(), Output: m.Limit.Output,
			InputModalities: m.Modalities.Input, OutputModalities: m.Modalities.Output}
		mm.Efforts = p.levels(m, id)
		mm.Reasoning = m.Thinks || len(mm.Efforts) > 0
		out = append(out, mm)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Released != out[j].Released {
			return out[i].Released > out[j].Released
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ProviderName is a models.dev provider's display name ("GitHub Copilot"
// for "github-copilot"), or "" when the catalog doesn't know it.
func ProviderName(id string) string {
	return load()[id].Name
}

// Thinks reports whether models.dev says a model of this id reasons, as
// most of the providers it lists serving it do, whether or not they give
// it levels; for a vendor it doesn't list, serving a model it knows from
// others.
func Thinks(id string) bool {
	load()
	v, _ := lookup(id, thinks)
	return v
}

// NameOf is the name models.dev gives a model of this id, as most of the
// providers it lists serving it do, matched as SeesImages matches it:
// without a vendor's prefix, in any case. "" when no provider names it
// other than by its id.
func NameOf(id string) string {
	load()
	b := bareID(id)
	if n, ok := found(b, names); ok {
		return n
	}
	r, w, ok := renamedBy(b, false)
	if !ok {
		return ""
	}
	n, _ := found(r, names)
	if n != "" && w > 0 {
		// two of a relay's models apart by their window alone read apart:
		// "DeepSeek V4.1 Flash [1M]", the mark as the id writes it
		n += " " + id[strings.LastIndexByte(id, '['):]
	}
	return n
}

// Spelling is the spelling models.dev gives a model most, of every bare
// id it knows that is the same model (SameModel): glm-5.3-flash for
// glm-5-3-flash, GLM-5.3-Flash or Ark's glm-5-3-flash-260828, as most of
// its providers spell it (the normal index normalFor reads). Lowercase,
// without a vendor's prefix; "" when it knows the model by no spelling.
func Spelling(id string) string {
	load()
	return normal[SameModel(id)]
}

// Named gives a model the list names by its id alone the name models.dev
// knows it by (NameOf), so one model reads the same under every provider:
// a GLM Coding Plan's glm-5-turbo, which Zhipu's catalog doesn't list, is
// GLM-5-Turbo as ZCode's is. Its id, and where requests go, stay as they
// are. A supplemented name shared by different ids falls back to the id.
// Already named models in reserved count toward collisions but stay as given.
func Named(ms []Model, reserved ...Model) []Model {
	out := slices.Clone(ms)
	fallback := make([]string, len(ms))
	for i, m := range ms {
		if m.Name == "" || m.Name == m.ID {
			fallback[i] = NameOf(m.ID)
		}
	}
	fillNames(out, fallback, reserved...)
	return out
}

// fillNames applies catalog names without giving different ids the same
// name. The ids are reserved too, so falling back to one cannot collide
// with another model's supplemented name. The caller owns ms.
func fillNames(ms []Model, fallback []string, reserved ...Model) {
	owners := make(map[string]string)
	collisions := make(map[string]bool)
	claim := func(name, id string) {
		if name == "" {
			return
		}
		if owner, ok := owners[name]; ok && owner != id {
			collisions[name] = true
		}
		owners[name] = id
	}
	for _, m := range reserved {
		claim(m.ID, m.ID)
		claim(m.Name, m.ID)
	}
	for i, m := range ms {
		claim(m.ID, m.ID)
		name := m.Name
		if fallback[i] != "" {
			name = fallback[i]
		}
		claim(name, m.ID)
	}
	for i, name := range fallback {
		if name == "" {
			continue
		}
		if collisions[name] {
			name = ms[i].ID
		}
		ms[i].Name = name
	}
}

// Knows reports whether models.dev lists a model of this id at all, under
// any provider, as ContextOf and EffortsOf match it.
//
// A model it lists under another spelling alone (normalFor) counts.
func Knows(id string) bool {
	return ContextOf(id) > 0 || len(EffortsOf(id)) > 0 || Thinks(id)
}

// IsOneOf reports whether the model of this id is one of ids (bare,
// lowercase), matched as ContextOf and EffortsOf match it: by the id, a
// relay's rename (codex-gpt-6.1-sol, a tag in brackets), another spelling,
// or the model the user said it is the same as (UserSame). It is how a
// rule kept for some models by name reaches them on a relay that renames
// them.
func IsOneOf(id string, ids map[string]bool) bool {
	load()
	_, ok := lookup(id, ids)
	return ok
}

// KnowsAsWritten is Knows without another spelling (normalFor): the
// usage report sends a model's id only when models.dev knows it as
// written, so an id a user typed that only folds into a known one
// ("Kimi K3.1 Pro 261024") is not sent.
func KnowsAsWritten(id string) bool {
	if contextAs(id, false) > 0 {
		return true
	}
	load()
	if e, _ := lookupAs(id, efforts, false); len(e) > 0 {
		return true
	}
	v, _ := lookupAs(id, thinks, false)
	return v
}

// Lists reports whether models.dev lists a model by this very id (its
// last part, any case), not one found by another spelling: claude-opus-5.5
// only when that is how one of its providers spells it.
func Lists(id string) bool {
	load()
	return listed[bareID(id)]
}

// SeesImages reports whether models.dev says a model of this id takes
// images, as most of the providers it lists serving it do; for a vendor it
// doesn't list, serving a model it knows from others ("z-ai/glm-5.3" is
// glm-5.3).
func SeesImages(id string) bool {
	load()
	v, _ := lookup(id, images)
	return v
}

// ContextOf is the context window models.dev gives a model of this id, as
// most of the providers it lists serving it do, or 0 when it doesn't know
// the model. Like SeesImages it reaches a vendor models.dev doesn't list: a
// proxy serving "openai/gpt-5.4", or "gpt-5.4(high)" with the reasoning
// level CLIProxyAPI takes in the id, which a static model list would
// otherwise leave at the agent's default.
//
// A relay's rename of a model it knows (renamed) that names its window
// ("[1M]", "[200K]", Claude Code's "[1m]") has the window it names: what
// the relay serves it at, as Claude Code takes claude-opus-5[1m] for 1M.
//
// Last, a model it lists under another spelling (normalFor): Volcengine
// Ark's kimi-k3-1-pro-261024 has kimi-k3.1-pro's window. A window the
// vendor's own list gives is read before this, by the caller, and stays.
func ContextOf(id string) int { return contextAs(id, true) }

// contextAs is ContextOf, with another spelling (normalFor), the model
// the user said it is the same as (UserSame) and a tag in brackets taken
// off (untagged) only when spellings is true.
func contextAs(id string, spellings bool) int {
	load()
	return contextIn(bareID(id), spellings, spellings)
}

// contextIn is contextAs for a bare id, the user's word (UserSame) heard
// only with user: the model it names is looked up as any other, but not
// sent to the user's word again.
func contextIn(b string, spellings, user bool) int {
	if w, ok := found(b, windows); ok {
		return w
	}
	if s, ok := userSame(b, user); ok {
		if w := contextIn(s, true, false); w > 0 {
			return w
		}
	}
	if r, w, ok := renamed(b); ok {
		if w > 0 {
			return w
		}
		if w, ok := found(r, windows); ok {
			return w
		}
	}
	if n, ok := normalFor(b); spellings && ok {
		w, _ := found(n, windows)
		return w
	}
	if t, ok := untagged(b); spellings && ok {
		return contextIn(t, true, user)
	}
	return 0
}

// OutputOf is the most tokens a reply from a model of this id may hold, as
// most of the providers serving it give it, or 0 when not known.
func OutputOf(id string) int {
	load()
	o, _ := lookup(id, outputs)
	return o
}

// EffortsOf is the reasoning levels models.dev gives a model of this id, as
// most of the providers giving any for it do, or nil when none does. Like
// ContextOf it reaches a vendor models.dev doesn't list: a custom provider
// serving "glm-5.3-flash" takes the levels Z.ai and the others serving it
// say it does, where it would otherwise be taken for a model that doesn't
// reason, and an agent offer no levels for it.
func EffortsOf(id string) []string {
	load()
	e, _ := lookup(id, efforts)
	return slices.Clone(e)
}

// lookup is what table holds for a model of this id: as found finds it,
// else as the relay's rename of a model found finds (renamed), else as
// the spelling models.dev lists it by (normalFor). Every capability is
// looked up the same way, so a model the catalog knows has all of them
// or none.
func lookup[T any](id string, table map[string]T) (T, bool) {
	return lookupAs(id, table, true)
}

// lookupAs is lookup, with another spelling (normalFor), the model the
// user said it is the same as (UserSame) and a tag in brackets taken off
// (untagged) only when spellings is true.
func lookupAs[T any](id string, table map[string]T, spellings bool) (T, bool) {
	return lookupIn(bareID(id), table, spellings, spellings)
}

// lookupIn is lookupAs for a bare id, the user's word heard only with
// user, as contextIn.
func lookupIn[T any](b string, table map[string]T, spellings, user bool) (T, bool) {
	if v, ok := found(b, table); ok {
		return v, true
	}
	if s, ok := userSame(b, user); ok {
		if v, ok := lookupIn(s, table, true, false); ok {
			return v, true
		}
	}
	if r, _, ok := renamed(b); ok {
		if v, ok := found(r, table); ok {
			return v, true
		}
	}
	if n, ok := normalFor(b); spellings && ok {
		return found(n, table)
	}
	if t, ok := untagged(b); spellings && ok {
		return lookupIn(t, table, true, user)
	}
	var zero T
	return zero, false
}

// UserSame, when set, is the model the user said a model of this bare id
// (lowercase, without a vendor's prefix) is the same as, by the id any
// vendor spells it with (settings' ModelSameAs; the settings package sets
// it). A model models.dev lists by its very id is that model; one it
// doesn't is looked up as the model the user named, before any guess of
// magpie's (renamed, normalFor): a relay's rename no rule can read,
// "ds.4.1(WB)" for deepseek-v4.1-flash (pinmmis on X), has that model's
// window, reply limit, levels and images.
var UserSame func(bare string) (string, bool)

// userSame is UserSame's answer for b, as a bare id, when user is true
// and it names a model other than b.
func userSame(b string, user bool) (string, bool) {
	if !user || UserSame == nil {
		return "", false
	}
	s, ok := UserSame(b)
	if s = bareID(strings.TrimSpace(s)); !ok || s == "" || s == b {
		return "", false
	}
	return s, true
}

// tagPairs are the brackets a relay or a user puts a channel's tag in,
// before or after a model's id: "deepseek-v4.1-flash (WB)", "[WB]
// deepseek-v4.1-flash", and their full-width forms.
var tagPairs = [][2]string{{"(", ")"}, {"[", "]"}, {"（", "）"}, {"【", "】"}, {"「", "」"}}

// untagged is b without a tag in brackets at its start or its end, and
// whether it had one: "deepseek-v4.1-flash (wb)" is deepseek-v4.1-flash.
// A "[...]" written onto the end of the id with no space before it is not
// a tag ("deepseek-v4.1-flash[fast]" may be a variant). A window mark ("[1m]", renamed's) is not a tag, nor is what is left
// with no letter in it.
func untagged(b string) (string, bool) {
	t := strings.TrimSpace(b)
	if r, _ := unmarked(t); r != t {
		return "", false
	}
	for _, p := range tagPairs {
		if strings.HasSuffix(t, p[1]) {
			// "[fast]" written onto the id may be a variant's
			// (TestRenameNeverTakesAModelForAnother): only one set
			// apart by a space is a tag
			if i := strings.LastIndex(t, p[0]); i > 0 && (p[0] != "[" || t[i-1] == ' ') {
				t = strings.TrimSpace(t[:i])
				break
			}
		}
		if strings.HasPrefix(t, p[0]) {
			if i := strings.Index(t, p[1]); i > 0 && i+len(p[1]) < len(t) {
				t = strings.TrimSpace(t[i+len(p[1]):])
				break
			}
		}
	}
	t = strings.Trim(t, " -_·.")
	if t == b || t == "" || !strings.ContainsFunc(t, unicode.IsLetter) {
		return "", false
	}
	return t, true
}

// normalFor is the bare id models.dev knows a model by when it knows it
// under another spelling alone: the same SameModel name, so a version's
// dot or dash ("kimi-k3.1-pro", "kimi-k3-1-pro"), case and gaps, and a
// snapshot date (Ark's six digits, "-261024"; "-20260801") don't keep
// them apart, while a variant does (-pro-max is not -pro, -mini not the
// model, k3.1 not k3). False for an id it knows as given, or none.
func normalFor(b string) (string, bool) {
	n, ok := normal[SameModel(b)]
	if !ok || n == b {
		return "", false
	}
	return n, true
}

// found is what table holds for a bare id: by the id itself, a Bedrock
// profile's without its geography, "gpt-5.4(high)" (CLIProxyAPI's level in
// the id) as gpt-5.4, or "glm-5:free" (":batch", ":thinking") as glm-5.
func found[T any](b string, table map[string]T) (T, bool) {
	if v, ok := table[b]; ok {
		return v, true
	}
	if r, ok := unprofiled(b); ok {
		return found(r, table)
	}
	if i := strings.IndexByte(b, '('); i > 0 && strings.HasSuffix(b, ")") {
		if v, ok := table[b[:i]]; ok {
			return v, true
		}
		b = b[:i]
	}
	if i := strings.IndexByte(b, ':'); i > 0 {
		v, ok := table[b[:i]]
		return v, ok
	}
	var zero T
	return zero, false
}

// relayPrefixes are what relays put before a model's id to rename it, so
// Claude Code, which takes a model for a Claude one by its name, serves it:
// claude-deepseek-v4.1-flash (#1383).
var relayPrefixes = []string{"claude-"}

// channelPrefix is any other word a relay puts before a model's id to name
// the channel it serves it on: nyro-gpt-5.5, nyro-gemini-3.8-flash (#2157).
// It is letters alone, as every such word models.dev's own lists put before
// a model another of them lists is (openai-, databricks-, doubao-, zai-):
// the model after it is the same model, its window, levels and images
// included.
var channelPrefix = lazyre.New(`^[a-z]{2,24}-`)

// renamed is the model a relay renamed to b, when models.dev lists none
// by b (found), and the window the rename names, if any: a window mark
// taken off ("deepseek-v4.1-flash[1m]", "[200k]", and Claude Code's
// claude-opus-5[1m], which is claude-opus-5), then a relay's prefix
// ("claude-deepseek-v4.1-flash", and a channel's word, "nyro-gpt-5.5").
// An id models.dev lists, or knows as an entry's canonical_model_id, is
// always its own model, never renamed (cortecs's claude-sonnet-4 is
// claude-sonnet-4-0, not sonnet-4-0); a prefix comes off only when what is
// left is known — listed, or an entry's canonical_model_id (#1520) — and
// names a version ("claude-auto" is not the router "auto").
func renamed(b string) (string, int, bool) { return renamedBy(b, true) }

// renamedBy is renamed, a channel's word (channelPrefix) taken off only
// with channels: the name a model is shown by keeps a channel's word, so
// a relay's nyro-gpt-5.5 and its gpt-5.5 still read apart.
func renamedBy(b string, channels bool) (string, int, bool) {
	if _, ok := found(b, listed); ok || canonical[b] {
		return "", 0, false
	}
	r, w := unmarked(b)
	if r != b {
		if knownModel(r) {
			return r, w, true
		}
	}
	for _, pre := range relayPrefixes {
		rest, ok := strings.CutPrefix(r, pre)
		if !ok || !strings.ContainsAny(rest, "0123456789") {
			continue
		}
		if knownModel(rest) {
			return rest, w, true
		}
	}
	if pre := channelPrefix.FindString(r); channels && pre != "" {
		if rest := r[len(pre):]; strings.ContainsAny(rest, "0123456789") && knownModel(rest) {
			return rest, w, true
		}
	}
	return "", 0, false
}

// knownModel is whether models.dev knows b as a model: listed by its id,
// or named as an entry's canonical_model_id (#1520) — a model no catalog
// lists by it, which relays serve and Claude Code asks for under a prefix.
func knownModel(b string) bool {
	if _, ok := found(b, listed); ok {
		return true
	}
	return canonical[b]
}

// unmarked is a bare id without a window mark at its end ("[1m]",
// "[200k]", "[1.5m]") and the window it names, or the id and 0 without one.
func unmarked(b string) (string, int) {
	if !strings.HasSuffix(b, "]") {
		return b, 0
	}
	i := strings.LastIndexByte(b, '[')
	if i <= 0 {
		return b, 0
	}
	mark := b[i+1 : len(b)-1]
	unit := 0
	switch {
	case strings.HasSuffix(mark, "k"):
		unit = 1_000
	case strings.HasSuffix(mark, "m"):
		unit = 1_000_000
	default:
		return b, 0
	}
	num := mark[:len(mark)-1]
	if strings.Trim(num, "0123456789.") != "" {
		return b, 0
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil || n <= 0 {
		return b, 0
	}
	return b[:i], int(n * float64(unit))
}

// ListedBy is the reasoning levels the first of providers (models.dev
// ids) listing a model of this id gives it — none, for one listed with a
// thinking switch alone or nothing at all — and whether any of them says.
// One listed with a thinking budget and no levels says nothing of them
// (Anthropic's claude-sonnet-4-5, whose budget an effort is sent as). The
// id is matched as EffortsOf matches it: without a vendor's prefix, in any
// case. It is how a model's maker is heard before its resellers: Xiaomi
// lists mimo-v2.6-flash with a switch alone, where gateways reselling it
// give levels up to max, which Xiaomi turns away (#214).
func ListedBy(providers []string, id string) ([]string, bool) {
	return listedBy(providers, id, true)
}

// ListedByBorrowed is ListedBy for levels borrowed from another provider:
// it uses the levels the catalog lists, without adding a verified provider's
// toggle-only off level. A provider's API accepting "none" does not verify
// that a relay, reseller, or another provider does too.
func ListedByBorrowed(providers []string, id string) ([]string, bool) {
	return listedBy(providers, id, false)
}

func listedBy(providers []string, id string, providerOverrides bool) ([]string, bool) {
	all, at := loadGen()
	b := bareID(id)
	if r, _, ok := renamed(b); ok {
		b = r
	}
	if r, ok := unprofiled(b); ok {
		b = r
	}
	pick := func(pid string, m mdModel) ([]string, bool) {
		providerID := ""
		if providerOverrides {
			providerID = pid
		}
		if e := all[pid].levels(m, providerID); len(e) > 0 || !m.budgeted() {
			return slices.Clone(e), true
		}
		return nil, false
	}
	for _, want := range []string{b, cutAt(b, '('), cutAt(b, ':')} {
		for _, pid := range providers {
			for _, key := range levelIndexOf(all, at, pid).bare[want] {
				if e, ok := pick(pid, all[pid].Models[key]); ok {
					return e, true
				}
			}
		}
	}
	// and last under another spelling (SameModel), only where no provider
	// lists it as given, and only for a name models.dev knows (normal): Ark
	// lists glm-5.3 on its plan, glm-5-3-flash-260828 pay-as-you-go. Of a
	// provider's several such entries, the first in key order.
	n := SameModel(b)
	if _, ok := normal[n]; !ok {
		return nil, false
	}
	for _, pid := range providers {
		for _, key := range levelIndexOf(all, at, pid).same[n] {
			if e, ok := pick(pid, all[pid].Models[key]); ok {
				return e, true
			}
		}
	}
	return nil, false
}

// levelIndex is a provider's model keys by what listedBy looks them up
// by, each in key order: their id without a path or case (bare), and
// SameModel's spelling of it (same). listedBy, asked for every model's
// levels on each look at the providers, read every vendor's every model
// through SameModel's regexp each time: 0.28 GB of the 2.6 GB a real
// config allocated over 20 polls. KinEfforts looks its thinking models up
// by their versionless shape (kin), where it split and joined every key
// on each ask: 66 MB of 1.4 GB.
type levelIndex struct {
	bare, same, kin map[string][]string
}

var levelIndexes struct {
	sync.Mutex
	gen int
	by  map[string]*levelIndex
}

// levelIndexOf is pid's levelIndex in all, the catalog loaded at Reset
// count at.
func levelIndexOf(all map[string]mdProvider, at int, pid string) *levelIndex {
	levelIndexes.Lock()
	defer levelIndexes.Unlock()
	if levelIndexes.by == nil || levelIndexes.gen != at {
		levelIndexes.gen, levelIndexes.by = at, map[string]*levelIndex{}
	}
	if x, ok := levelIndexes.by[pid]; ok {
		return x
	}
	x := &levelIndex{bare: map[string][]string{}, same: map[string][]string{}, kin: map[string][]string{}}
	for _, key := range slices.Sorted(maps.Keys(all[pid].Models)) {
		b := bareID(key)
		x.bare[b] = append(x.bare[b], key)
		n := SameModel(b)
		x.same[n] = append(x.same[n], key)
		if m := all[pid].Models[key]; (m.Thinks || len(m.efforts("")) > 0) && !m.budgeted() {
			if s, ok := versionless(b); ok {
				x.kin[s] = append(x.kin[s], key)
			}
		}
	}
	levelIndexes.by[pid] = x
	return x
}

// variantSuffix is a word a proxy puts after a model's id to name the
// level or speed it serves the model at: CLIProxyAPI's Antigravity
// channel lists gemini-3.8-flash-high, claude-opus-5-5-high,
// claude-opus-4-6-thinking and gpt-oss-120b-medium, and its xAI channel
// grok-3-mini-fast. Its "(high)" form is found's.
var variantSuffix = lazyre.New(`(?i)-(minimal|low|medium|high|xhigh|max|thinking|fast)$`)

// Unsuffixed is id with a proxy's level or speed word taken off
// (variantSuffix), and whether it had one. It is for borrowing levels a
// model has no word on alone: the vendor's own list, which may name a
// model so, goes first.
func Unsuffixed(id string) (string, bool) {
	if loc := variantSuffix.FindStringIndex(id); loc != nil && loc[0] > 0 {
		return id[:loc[0]], true
	}
	return "", false
}

// KinEfforts are the reasoning levels of a model none of the providers
// named lists, taken from the newest of their thinking models of the same
// name but for its version (gpt-6.1-sol's from gpt-6-sol, mimo-v2.7-flash's
// from mimo-v2.6-flash): a model newer than the catalog on this machine
// offers its line's levels, not none. False when none of them has one.
func KinEfforts(providers []string, id string) ([]string, bool) {
	all, at := loadGen()
	shape, ok := versionless(bareID(id))
	if !ok {
		return nil, false
	}
	var (
		levels             []string
		newest, from, name string
		found              bool
	)
	for _, pid := range providers {
		for _, key := range levelIndexOf(all, at, pid).kin[shape] {
			m := all[pid].Models[key]
			// the newest; of two the same day, the first provider's, then
			// the greater id, whatever order the map gives
			if !found || m.ReleaseDate > newest || m.ReleaseDate == newest && pid == from && key > name {
				levels, newest, from, name, found = all[pid].levels(m, ""), m.ReleaseDate, pid, key, true
			}
		}
	}
	return slices.Clone(levels), found
}

// versionless is a model id with each of its version parts (6.1, v2.6) as
// "#", and whether it has one beside a name: gpt-#-sol.
func versionless(b string) (string, bool) {
	parts := strings.Split(b, "-")
	versioned, named := false, false
	for i, s := range parts {
		v := strings.TrimPrefix(s, "v")
		if v != "" && strings.Trim(v, "0123456789.") == "" && v[0] != '.' {
			parts[i], versioned = "#", true
		} else {
			named = true
		}
	}
	return strings.Join(parts, "-"), versioned && named
}

// budgeted reports whether models.dev says the model takes a thinking
// budget.
func (m mdModel) budgeted() bool {
	for _, r := range m.Reasoning {
		if r.Type == "budget_tokens" {
			return true
		}
	}
	return false
}

// cutAt is s before sep: "gpt-5.4(high)" is gpt-5.4, "glm-5:free" glm-5.
func cutAt(s string, sep byte) string {
	if i := strings.IndexByte(s, sep); i > 0 {
		return s[:i]
	}
	return s
}

// mostListed is the list most providers give; a tie goes to the shorter,
// then the first in order, so the answer doesn't change from run to run.
func mostListed(by map[string]int) string {
	best, n := "", 0
	for l, c := range by {
		if c > n || c == n && (strings.Count(l, ",") < strings.Count(best, ",") || strings.Count(l, ",") == strings.Count(best, ",") && l < best) {
			best, n = l, c
		}
	}
	return best
}

// mostNamed is the name most providers give; a tie goes to the shorter,
// then the first in order, so the answer doesn't change from run to run.
func mostNamed(by map[string]int) string {
	best, n := "", 0
	for s, c := range by {
		if c > n || c == n && (len(s) < len(best) || len(s) == len(best) && s < best) {
			best, n = s, c
		}
	}
	return best
}

// mostGiven is the size most providers give; a tie goes to the smaller,
// which a prompt fits either way.
func mostGiven(by map[int]int) int {
	best, n := 0, 0
	for w, c := range by {
		if c > n || c == n && w < best {
			best, n = w, c
		}
	}
	return best
}

// routerIDs are ids that name a vendor's router, not a model: OpenRouter's
// openrouter/auto, model-oracle-ai's auto, Kilo's kilo-auto/free and the
// rest are each their own, and a plugin's "auto" (豆包工作 picks Doubao's
// model) is none of them. Without a path they were taken for one model,
// and every provider's "auto" was given that model's levels, images and
// window: pi offered thinking levels and image input for 豆包工作's auto.
// What a provider's own list says of its router (ListedBy) still holds.
// Only ids no vendor gives a model are here: "fast" and "balanced" are
// trustedrouter's and Kilo's routers, but a vendor may name a model so.
var routerIDs = map[string]bool{"auto": true, "default": true, "free": true}

// bareID is a model's id without the vendor's prefix, lowercase.
func bareID(id string) string {
	id = strings.ToLower(id)
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// bedrockGeos are the geographies a Bedrock inference profile's id starts
// with.
var bedrockGeos = []string{"global.", "us.", "us-gov.", "eu.", "apac.", "jp.", "au.", "ca.", "in."}

// unprofiled is a Bedrock inference profile's id without its geography
// (apac.anthropic.claude-opus-5-5 is anthropic.claude-opus-5-5): models.dev
// lists each model in only some of them, the model id in all.
func unprofiled(b string) (string, bool) {
	for _, g := range bedrockGeos {
		if r, ok := strings.CutPrefix(b, g); ok && strings.Contains(r, ".") {
			return r, true
		}
	}
	return "", false
}

// The APIs a model Serves is made for.
const (
	ServesEmbedding     = "embedding"
	ServesRerank        = "rerank"
	ServesSpeech        = "speech"
	ServesTranscription = "transcription"
)

// ServesOf is the API a model's id says it is made for, other than chat,
// images or video: "" for any other model. Read from the id alone, as
// vendors' OpenAI-style lists say no more (text-embedding-3-small,
// bge-reranker-v2-m3, tts-1, gpt-4o-mini-tts, whisper-1,
// gpt-4o-transcribe). Voyage AI makes embedding models alone, and names
// them voyage-* (voyage-4, voyage-code-3), with no "embed" in the id.
func ServesOf(id string) string {
	id = strings.ToLower(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	switch {
	case strings.Contains(id, "rerank"):
		return ServesRerank
	case strings.Contains(id, "embed") || strings.HasPrefix(id, "bge-") || strings.HasPrefix(id, "voyage-"):
		return ServesEmbedding
	case strings.Contains(id, "whisper") || strings.Contains(id, "transcribe"):
		return ServesTranscription
	case strings.HasPrefix(id, "tts-") || strings.Contains(id, "-tts"):
		return ServesSpeech
	}
	return ""
}

func textModel(m mdModel) bool {
	if len(m.Modalities.Output) > 0 {
		text := false
		for _, o := range m.Modalities.Output {
			if o == "text" {
				text = true
			}
		}
		if !text {
			return false
		}
	}
	id := m.ID
	for _, bad := range []string{"embed", "-tts", "image", "audio", "-live", "robotics", "computer-use", "deep-research", "transcribe", "realtime", "moderation", "whisper", "dall-e", "sora"} {
		if strings.Contains(id, bad) {
			return false
		}
	}
	return true
}

// draws is whether a models.dev row is a model that makes images: one
// whose only output is images (gpt-image-1, imagen, flux), or that answers
// in text and images and is named for them (gemini-2.5-flash-image,
// gpt-5-image) — not a text model that can also put a chart in its answer
// (deep-research, openrouter/auto).
func draws(m mdModel) bool {
	if !slices.Contains(m.Modalities.Output, "image") {
		return false
	}
	id := strings.ToLower(m.ID)
	if strings.Contains(id, "deep-research") || strings.HasSuffix(id, "/auto") {
		return false
	}
	return !slices.Contains(m.Modalities.Output, "text") || DrawsID(id)
}

// DrawsID is whether a model's id names one that makes images, for a model
// the catalog doesn't know (one typed in, or a vendor's own list's). Banana
// is the Nano Banana the Gemini image models are called on a relay, whatever
// its spelling there (#2071). SenseNova's U1 models (sensenova-u1.5-lite,
// -fast) draw only, on its images API (lileo0668 on Discord).
func DrawsID(id string) bool {
	id = strings.ToLower(id)
	for _, w := range []string{"image", "imagen", "imagine", "dall-e", "flux", "seedream", "cogview", "stable-diffusion", "sdxl", "wanx", "kolors", "hidream", "banana", "sensenova-u1"} {
		if strings.Contains(id, w) {
			return true
		}
	}
	return false
}

// ImagesAPI is whether a model draws on an images API rather than answering
// in chat with pictures: gpt-image, dall-e, imagen, flux, seedream… — not
// gemini-*-image or gpt-5-image, which chat. iFlytek Loomy's Hy-Image
// generators (Hy-Image-3.5-preview) draw on its images API too (#4913).
func ImagesAPI(id string) bool {
	id = strings.ToLower(id)
	for _, w := range []string{"gpt-image", "chatgpt-image", "dall-e", "imagen", "imagine", "qwen-image", "wanx", "wan2", "seedream", "cogview", "flux", "stable-diffusion", "sdxl", "kolors", "hidream", "sensenova-u1", "hy-image"} {
		if strings.Contains(id, w) {
			return true
		}
	}
	return false
}

// FilmsID is whether a model's id names one that makes videos, for a model
// whose list says nothing of a kind: OpenAI's sora-2 and sora-2-pro are
// made at its OpenAI-shaped videos API, which the gateway speaks, but
// textModel counts their ids out of the chat models, and a list that
// marks no kind left them unlisted at all — never offered to talk to, and
// never marked a video model in the provider's editor (#4954). The other
// video families keep the faces their own APIs are asked on: Ark's
// Seedance at its tasks (gateway arkVideoModel), a MiniMax key's H3
// (video_minimax.go), and a model the user says makes videos (#1399).
func FilmsID(id string) bool {
	return strings.Contains(strings.ToLower(id), "sora")
}

// PointsToImagesAPI is whether a vendor's refusal of a chat request names
// its images API as the one the model is asked on: Agnes answers
// agnes-image-2.5-flash on chat completions with 400 "Model
// agnes-image-2.5-flash is an image model. Use /v1/images/generations"
// (#1837), a model ImagesAPI can't tell by its name from gemini's -flash
// image ones, which chat. The vendor's own words decide it then.
func PointsToImagesAPI(text string) bool {
	return strings.Contains(strings.ToLower(text), "images/generations")
}

// PointsToChat is PointsToImagesAPI the other way round: a refusal on the
// images API that names chat completions as where the model draws.
func PointsToChat(text string) bool {
	return strings.Contains(strings.ToLower(text), "chat/completions")
}

// WrongEndpoint recognizes the errors OpenAI-compatible servers give when a
// model exists but isn't served on the endpoint asked: the gateway asks the
// model on another API the provider has then, and so does a model's test.
func WrongEndpoint(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	// the words are matched decoded, too (#1669)
	msg := strings.ToLower(string(Words(body)))
	for _, phrase := range []string{
		"not a chat model",
		"not supported in the v1/chat/completions",
		"not supported in /v1/chat/completions",
		"not supported in the v1/responses",
		"not supported in /v1/responses",
		"only supported in v1/responses",
		"only supported in /v1/responses",
		"use v1/completions",
		"use /v1/completions",
		"use v1/responses",
		"use /v1/responses",
		"use v1/chat/completions",
		"use /v1/chat/completions",
		"not accessible via the", // Copilot
		"unsupported_api_for_model",
		// Copilot's /v1/messages, for a model it serves on other APIs
		// alone (GPT-4.1, Gemini, GPT-5.6: #754); a model it doesn't
		// serve at all is model_not_supported there
		"no model endpoints available given user constraints",
		"is not supported for format",    // OpenCode: "Model grok-4.7 is not supported for format anthropic"
		"does not support this protocol", // OpenCode, with a key: "Model does not support this protocol"
		// a relay serving a model with tools on Responses alone (#1308:
		// "gpt-6.1-sol requires Responses for tool calls; this account
		// only supports Chat Completions")
		"requires responses",
		// a relay that says it in Chinese (#3766, char1eslu: AgentRouter's
		// "模型 gpt-5.6-sol 不支持 /v1/chat/completions 接口，请改用
		// /v1/responses（OpenAI Responses API）。")
		"不支持 /v1/chat/completions 接口",
		"改用 /v1/responses",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// Words is an error body as its words are matched: a JSON body that
// escapes what isn't ASCII as \uXXXX (Python's json.dumps does by default)
// has its strings decoded and put after it, one to a line, so "用量",
// "频率" or "将在 … 重置" are read whether the vendor escaped them or not
// (#1669). Any other body is matched as it is.
func Words(body []byte) []byte {
	if !bytes.Contains(body, []byte(`\u`)) {
		return body
	}
	var v any
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	out := append([]byte(nil), body...)
	var walk func(any)
	walk = func(x any) {
		switch x := x.(type) {
		case string:
			out = append(append(out, '\n'), x...)
		case map[string]any:
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(v)
	return out
}

// Drawers are the models of one models.dev provider that make images,
// newest first.
func Drawers(id string) []Model {
	p, ok := load()[id]
	if !ok {
		return nil
	}
	var out []Model
	for _, m := range p.Models {
		if !draws(m) {
			continue
		}
		out = append(out, Model{ID: m.ID, Name: m.Name, Provider: id, Released: m.ReleaseDate, Price: m.Cost,
			Images: slices.Contains(m.Modalities.Input, "image"), ImageInput: imageInput(m.Modalities.Input)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Released != out[j].Released {
			return out[i].Released > out[j].Released
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// AtEndpoint are the models.dev providers whose models are served at base
// (their "api"), in order: the catalog of a provider the user added by its
// endpoint alone. A trailing slash and the case of the address don't count.
func AtEndpoint(base string) []string {
	want := endpointKey(base)
	if want == "" {
		return nil
	}
	all, at := loadGen()
	endpoints.Lock()
	defer endpoints.Unlock()
	if endpoints.by == nil || endpoints.gen != at {
		// every provider's API, keyed once a load rather than each
		// look-up: a provider's price asks for it, and a model list
		// priced row by row asked it once a model (/v1/models' pricing)
		endpoints.gen, endpoints.by = at, map[string][]string{}
		for id, p := range all {
			if k := endpointKey(p.API); k != "" {
				endpoints.by[k] = append(endpoints.by[k], id)
			}
		}
		for _, ids := range endpoints.by {
			slices.Sort(ids)
		}
	}
	return slices.Clone(endpoints.by[want])
}

// endpoints are the models.dev providers by the API they serve at
// (endpointKey), for the load of Reset count gen.
var endpoints struct {
	sync.Mutex
	gen int
	by  map[string][]string
}

func endpointKey(u string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(u), "/"))
}

// Providers returns models.dev provider ids known to the catalog.
func Providers() []string {
	var ids []string
	for id := range load() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// CodexHome is where Codex CLI keeps its state: $CODEX_HOME, else ~/.codex.
func CodexHome() string { return appdir.CodexHome() }

// CodexModelsCache is the model list Codex CLI keeps, under CodexHome.
func CodexModelsCache() string { return filepath.Join(CodexHome(), "models_cache.json") }

// Codex returns the models Codex itself lists, straight from the cache the
// Codex CLI writes; there is no compiled-in list to fall back to.
func Codex() []Model { return slices.Clone(CodexShared()) }

// CodexShared is Codex's list without a copy, for a caller that only
// reads it: the effort field asks for it on every poll.
func CodexShared() []Model {
	out, _ := filememo.Read("codex models", CodexModelsCache(), parseCodex)
	return out
}

func parseCodex(b []byte) ([]Model, error) {
	var cache struct {
		ETag   string `json:"etag"`
		Models []struct {
			Slug        string   `json:"slug"`
			DisplayName string   `json:"display_name"`
			Description string   `json:"description"`
			Visibility  string   `json:"visibility"`
			Priority    int      `json:"priority"`
			Input       []string `json:"input_modalities"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Context int `json:"context_window"`
			Max     int `json:"max_context_window"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &cache); err != nil || len(cache.Models) == 0 {
		return nil, err
	}
	sort.SliceStable(cache.Models, func(i, j int) bool { return cache.Models[i].Priority < cache.Models[j].Priority })
	var out []Model
	for _, m := range cache.Models {
		if m.Visibility == "hide" || MagpieAdded(cache.ETag, m.Slug, m.Description) {
			continue
		}
		// the window Codex holds a conversation to (272K on a ChatGPT
		// plan), which models.dev's input limit for the id, OpenAI's
		// API's (gpt-5.5's 922K), would otherwise stand in for
		mm := Model{ID: m.Slug, Name: m.DisplayName, Provider: "openai", ImageInput: imageInput(m.Input), Context: Window(m.Context)}
		if m.Max > m.Context && m.Context > 0 {
			mm.MaxContext = m.Max
		}
		if mm.ImageInput != nil {
			mm.Images = *mm.ImageInput
		}
		for _, l := range m.Levels {
			mm.Efforts = append(mm.Efforts, l.Effort)
		}
		out = append(out, mm)
	}
	return out, nil
}

// MagpieAdded reports whether an entry of Codex's models_cache.json is one
// of magpie's, not Codex's own: the list Codex keeps is the one it was last
// handed, and handed through the gateway it has magpie's models in it too
// ("group/semantic", "deepseek/deepseek-v4", "codex/gpt-5.5" — magpie's ids,
// which Codex's slugs never look like). Read back as Codex's own, they were
// listed under OpenAI, and kept there after Codex was routed elsewhere.
func MagpieAdded(etag, slug, description string) bool {
	return strings.HasSuffix(description, " via magpie") ||
		strings.Contains(etag, "+magpie-") && strings.Contains(slug, "/")
}

// EffortRank orders the reasoning levels agents and vendors name, weakest
// first.
var EffortRank = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// EffortAliases are levels some vendors name in words of their own, each
// with the level of EffortRank's it is: Trae's light and extra_high (its 轻
// and 极高), which a plugin serving Trae's models (Our Free Model's) lists
// as they are (nianshou5 on Discord).
var EffortAliases = map[string]string{"light": "low", "extra_high": "xhigh"}

// EffortLevel is the level of EffortRank's that e names: e itself, or the
// one a vendor's word for it is (EffortAliases).
func EffortLevel(e string) string {
	if l, ok := EffortAliases[strings.ToLower(e)]; ok {
		return l
	}
	return e
}

// NearestEffort is the level of levels nearest want in EffortRank's order,
// a tie going up, never "none" unless it is want; want itself when levels
// are none, hold it, or want is no level EffortRank knows. The gateway
// fits an effort to a model's levels by it on the way out (fitEffort), and
// Codex's catalog gives a model the effort set for new sessions by it
// (codexcat.ModelDefault), so the level a model starts at is the one it
// would be sent at. A level is placed by the one it names (EffortLevel),
// so low is a model's light, and a model's levels go in its own words.
func NearestEffort(want string, levels []string) string {
	at := slices.Index(EffortRank, EffortLevel(want))
	if len(levels) == 0 || at < 0 || slices.Contains(levels, want) {
		return want
	}
	best, dist := want, len(EffortRank)
	for _, l := range levels {
		i := slices.Index(EffortRank, EffortLevel(l))
		if i < 0 || l == "none" {
			continue
		}
		d := i - at
		if d < 0 {
			d = -d
		}
		if d < dist || d == dist && i > at {
			best, dist = l, d
		}
	}
	return best
}

// Efforts returns the reasoning levels a model supports, if known.
func Efforts(models []Model, id string) []string {
	for _, m := range models {
		if m.ID == id && len(m.Efforts) > 0 {
			return m.Efforts
		}
	}
	return nil
}
