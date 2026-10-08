// Package catalog knows which models exist. It reads the models.dev catalog
// (from magpie's own cache or OpenCode's), Codex's model cache, and falls back
// to a small built-in list so the picker is never empty.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filememo"
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
	// APIs, when the vendor says, are the APIs the model is served on
	// ("chat", "responses", "anthropic"); empty is not known.
	APIs []string `json:",omitempty"`
	// Images is set on a model that takes images as input.
	Images bool `json:",omitempty"`
	// ImageInput is the source's explicit answer; nil means it did not say.
	ImageInput *bool `json:",omitempty"`
	// Context is how many tokens a prompt may hold, when known: models.dev's
	// input limit, else its context window.
	Context int `json:",omitempty"`
	// MaxContext is the most a prompt may hold when asked for, above
	// Context: Codex's max_context_window (872k on GPT-6, 272k by default).
	MaxContext int `json:",omitempty"`
	// Output is the most tokens a reply may hold, when known.
	Output int `json:",omitempty"`
	// Compact is where the user has Codex compact a conversation on this
	// model (#876), its own or its provider's (settings.ModelCompacts): the
	// window it is told when Context is longer. 0 is none set, for the
	// one for every model (settings.Compact).
	Compact int `json:",omitempty"`
	// Fast is set on a model Codex may ask for priority processing (its
	// Fast mode): one a ChatGPT account serves.
	Fast bool `json:",omitempty"`
	// Tiers, in one ChatGPT account's own model list, are the service
	// tiers its plan offers on the model ("priority", "ultrafast").
	Tiers []string `json:",omitempty"`
	// AgentsV2 is set on a model Codex is told multi-agent V2 for, so its
	// Ultra hands work to Codex's agents: one offering Ultra that no
	// ChatGPT account answers for (provider.Entry's).
	AgentsV2 bool `json:",omitempty"`
	// Draws is set on a vendor-listed model that makes images (gpt-image-1,
	// a relay's flux): kept with the list for Settings → Images, never
	// offered to agents as a model to talk to.
	Draws bool `json:",omitempty"`
	// Films is set on a model another magpie lists as one it makes videos
	// with (a Remote magpie's Grok Imagine Video): kept with the list for
	// its videos API, never offered as a model to talk to or draw with.
	Films bool `json:",omitempty"`
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
	// WebSearch is how another magpie (a Remote magpie) searches the web
	// for the model when a web search tool is offered: "native", its own
	// provider by itself, or "magpie", with that magpie's searcher; "" it
	// doesn't, or didn't say.
	WebSearch string `json:",omitempty"`
}

func imageInput(modalities []string) *bool {
	if modalities == nil {
		return nil
	}
	yes := slices.Contains(modalities, "image")
	return &yes
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
	out := Price{Input: p.Input * r, Output: p.Output * r, CacheRead: p.CacheRead * r, CacheWrite: p.CacheWrite * r, CacheWrite1h: p.CacheWrite1h * r}
	for _, t := range p.Tiers {
		out.Tiers = append(out.Tiers, Tier{Above: t.Above, Input: t.Input * r, Output: t.Output * r, CacheRead: t.CacheRead * r, CacheWrite: t.CacheWrite * r, CacheWrite1h: t.CacheWrite1h * r})
	}
	return out
}

// Same is whether two prices are the same in every part and tier.
func (p Price) Same(q Price) bool {
	return p.Input == q.Input && p.Output == q.Output && p.CacheRead == q.CacheRead && p.CacheWrite == q.CacheWrite &&
		p.CacheWrite1h == q.CacheWrite1h && slices.Equal(p.Tiers, q.Tiers)
}

// At is the flat price a request whose input, cached and cache-written
// tokens included, comes to prompt tokens is billed at: that of the highest
// tier it is over, else the base price. OpenAI counts a request's input
// with its cached tokens in it, and bills the whole request at the tier.
func (p Price) At(prompt int) Price {
	flat := Price{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite, CacheWrite1h: p.CacheWrite1h}
	for _, t := range p.Tiers {
		if prompt > t.Above {
			flat = Price{Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite, CacheWrite1h: t.CacheWrite1h}
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
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	Env    []string           `json:"env"`
	Models map[string]mdModel `json:"models"`
}

type mdModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ReleaseDate string `json:"release_date"`
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

// efforts are the reasoning levels models.dev says the model takes.
func (m mdModel) efforts() []string {
	var out []string
	for _, r := range m.Reasoning {
		if r.Type == "effort" {
			out = r.Values
		}
	}
	return out
}

// window is the tokens a prompt to m may hold: the input limit where
// models.dev gives one (gpt-5's 272K of its 400K), else the whole context.
// A row whose input limit sits above its window (Cloudflare AI Gateway's
// gpt-5 at 272K over 128K) holds the window: the cap never exceeds it.
func (m mdModel) window() int {
	if m.Limit.Input > 0 {
		if m.Limit.Context > 0 && m.Limit.Context < m.Limit.Input {
			return m.Limit.Context
		}
		return m.Limit.Input
	}
	return m.Limit.Context
}

var modelsDevURL = "https://models.dev/api.json" // a var for tests

var (
	// loadMu guards loading and Reset: a background Sync (fresh.go) resets
	// while a call is pricing its model
	loadMu sync.Mutex
	loaded bool
	mdev   map[string]mdProvider
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

	syncMu sync.Mutex
)

// CachePath is where `magpie sync` stores the models.dev catalog.
func CachePath() string { return filepath.Join(appdir.Cache(), "models.json") }

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
	loadMu.Lock()
	defer loadMu.Unlock()
	if !loaded {
		loaded = true
		func() {
			for _, p := range []string{CachePath(), opencodeCache()} {
				b, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				var m map[string]mdProvider
				if json.Unmarshal(b, &m) == nil && len(m) > 0 {
					mdev = m
					votes, reasons := map[string]int{}, map[string]int{}
					named := map[string]map[string]int{}
					sizes, outs := map[string]map[int]int{}, map[string]map[int]int{}
					levels := map[string]map[string]int{}
					// one vote a provider for each list it gives a model: a
					// gateway listing it under each host it routes to
					// (llmgateway's deepinfra/…, xiaomi/…) votes once, not once
					// a host
					voted := map[string]bool{}
					for pid, p := range m {
						for id, x := range p.Models {
							OneHourFor(id, x.Cost)
							if x.Name != "" && x.Name != id && x.Name != bareID(id) {
								if named[bareID(id)] == nil {
									named[bareID(id)] = map[string]int{}
								}
								named[bareID(id)][x.Name]++
							}
							if e := x.efforts(); len(e) > 0 {
								l := strings.Join(e, ",")
								if levels[bareID(id)] == nil {
									levels[bareID(id)] = map[string]int{}
								}
								if k := pid + "\x00" + bareID(id) + "\x00" + l; !voted[k] {
									voted[k] = true
									levels[bareID(id)][l]++
								}
							}
							if x.Thinks || len(x.efforts()) > 0 {
								reasons[bareID(id)]++
							} else {
								reasons[bareID(id)]--
							}
							if slices.Contains(x.Modalities.Input, "image") {
								votes[bareID(id)]++
							} else {
								votes[bareID(id)]--
							}
							if w := x.window(); w > 0 {
								if sizes[bareID(id)] == nil {
									sizes[bareID(id)] = map[int]int{}
								}
								sizes[bareID(id)][w]++
							}
							if o := x.Limit.Output; o > 0 {
								if outs[bareID(id)] == nil {
									outs[bareID(id)] = map[int]int{}
								}
								outs[bareID(id)][o]++
							}
						}
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
					return
				}
			}
		}()
	}
	return mdev
}

// Reset forgets the loaded catalog so the next call re-reads the cache.
func Reset() {
	loadMu.Lock()
	defer loadMu.Unlock()
	loaded = false
	mdev, images, windows, outputs, efforts, thinks, names = nil, nil, nil, nil, nil, nil, nil
}

// Sync downloads the models.dev catalog into CachePath. It serializes with
// itself so a background refresh and a manual one cannot interleave writes.
func Sync(ctx context.Context) error {
	syncMu.Lock()
	defer syncMu.Unlock()
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
	p := CachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return err
	}
	Reset()
	Touched() // names, reasoning levels and context windows may be others
	return nil
}

// Stale reports whether no catalog exists or the cache is older than a day
// (fresh.go).
func Stale() bool {
	src := Source()
	if src == "" {
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
// profile's geography, a "(variant)" or a ":tag".
func PricedBy(providers []string, id string) (Price, bool) {
	all := load()
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
			keys := make([]string, 0, len(all[pid].Models))
			for key, m := range all[pid].Models {
				if m.Cost != nil && bareID(key) == want {
					keys = append(keys, key)
				}
			}
			if len(keys) > 0 {
				slices.Sort(keys)
				return *all[pid].Models[keys[0]].Cost, true
			}
		}
	}
	// and last with a dot and a dash taken for the same: Copilot and the
	// relays spell Anthropic's models claude-opus-4.6, Anthropic's own entry
	// claude-opus-4-6. Only where nothing above priced it, so a vendor that
	// lists the id as given, at a price of its own, is still the one asked.
	want := dashed(b)
	for _, pid := range providers {
		keys := []string{}
		for key, m := range all[pid].Models {
			if m.Cost != nil && dashed(bareID(key)) == want {
				keys = append(keys, key)
			}
		}
		if len(keys) > 0 {
			slices.Sort(keys)
			return *all[pid].Models[keys[0]].Cost, true
		}
	}
	return Price{}, false
}

// dashed is an id with its dots as dashes: the spelling two ids are compared
// in when one vendor writes a model's version 4.6 and another 4-6.
func dashed(id string) string { return strings.ReplaceAll(id, ".", "-") }

// APIOf is the API a models.dev provider's model is served on, when the
// catalog says it's one of its own: "responses" or "anthropic" for a model
// OpenCode serves through OpenAI's or Anthropic's SDK, not the
// OpenAI-compatible one the rest of its models go through.
func APIOf(providerID, modelID string) string {
	if p, ok := load()[providerID]; ok {
		if m, ok := p.Models[modelID]; ok && m.Provider != nil {
			switch m.Provider.NPM {
			case "@ai-sdk/openai":
				return "responses"
			case "@ai-sdk/anthropic":
				return "anthropic"
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
	p, ok := load()[id]
	if !ok {
		return nil
	}
	var out []Model
	for _, m := range p.Models {
		if !textModel(m) {
			continue
		}
		mm := Model{ID: m.ID, Name: m.Name, Provider: id, Released: m.ReleaseDate, Price: m.Cost, Temperature: m.Temperature,
			Images: slices.Contains(m.Modalities.Input, "image"), ImageInput: imageInput(m.Modalities.Input), Context: m.window(), Output: m.Limit.Output}
		mm.Efforts = m.efforts()
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
	return thinks[bareID(id)]
}

// NameOf is the name models.dev gives a model of this id, as most of the
// providers it lists serving it do, matched as SeesImages matches it:
// without a vendor's prefix, in any case. "" when no provider names it
// other than by its id.
func NameOf(id string) string {
	load()
	return names[bareID(id)]
}

// Named gives a model the list names by its id alone the name models.dev
// knows it by (NameOf), so one model reads the same under every provider:
// a GLM Coding Plan's glm-5-turbo, which Zhipu's catalog doesn't list, is
// GLM-5-Turbo as ZCode's is. Its id, and where requests go, stay as they
// are.
func Named(ms []Model) []Model {
	out := slices.Clone(ms)
	for i, m := range out {
		if m.Name == "" || m.Name == m.ID {
			if n := NameOf(m.ID); n != "" {
				out[i].Name = n
			}
		}
	}
	return out
}

// Knows reports whether models.dev lists a model of this id at all, under
// any provider, as ContextOf and EffortsOf match it.
func Knows(id string) bool {
	return ContextOf(id) > 0 || len(EffortsOf(id)) > 0 || Thinks(id)
}

// SeesImages reports whether models.dev says a model of this id takes
// images, as most of the providers it lists serving it do; for a vendor it
// doesn't list, serving a model it knows from others ("z-ai/glm-5.3" is
// glm-5.3).
func SeesImages(id string) bool {
	load()
	return images[bareID(id)]
}

// ContextOf is the context window models.dev gives a model of this id, as
// most of the providers it lists serving it do, or 0 when it doesn't know
// the model. Like SeesImages it reaches a vendor models.dev doesn't list: a
// proxy serving "openai/gpt-5.4", or "gpt-5.4(high)" with the reasoning
// level CLIProxyAPI takes in the id, which a static model list would
// otherwise leave at the agent's default.
func ContextOf(id string) int {
	load()
	b := bareID(id)
	if w, ok := windows[b]; ok {
		return w
	}
	if r, ok := unprofiled(b); ok {
		return ContextOf(r)
	}
	if i := strings.IndexByte(b, '('); i > 0 && strings.HasSuffix(b, ")") {
		b = b[:i]
		if w, ok := windows[b]; ok {
			return w
		}
	}
	if i := strings.IndexByte(b, ':'); i > 0 { // ":free", ":batch"
		return windows[b[:i]]
	}
	return 0
}

// OutputOf is the most tokens a reply from a model of this id may hold, as
// most of the providers serving it give it, or 0 when not known.
func OutputOf(id string) int {
	load()
	b := bareID(id)
	if o, ok := outputs[b]; ok {
		return o
	}
	if r, ok := unprofiled(b); ok {
		return OutputOf(r)
	}
	if i := strings.IndexAny(b, "(:"); i > 0 {
		return outputs[b[:i]]
	}
	return 0
}

// EffortsOf is the reasoning levels models.dev gives a model of this id, as
// most of the providers giving any for it do, or nil when none does. Like
// ContextOf it reaches a vendor models.dev doesn't list: a custom provider
// serving "glm-5.3-flash" takes the levels Z.ai and the others serving it
// say it does, where it would otherwise be taken for a model that doesn't
// reason, and an agent offer no levels for it.
func EffortsOf(id string) []string {
	load()
	b := bareID(id)
	if e, ok := efforts[b]; ok {
		return slices.Clone(e)
	}
	if r, ok := unprofiled(b); ok {
		return EffortsOf(r)
	}
	if i := strings.IndexByte(b, '('); i > 0 && strings.HasSuffix(b, ")") {
		b = b[:i]
		if e, ok := efforts[b]; ok {
			return slices.Clone(e)
		}
	}
	if i := strings.IndexByte(b, ':'); i > 0 { // ":free", ":thinking"
		return slices.Clone(efforts[b[:i]])
	}
	return nil
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
	all := load()
	b := bareID(id)
	if r, ok := unprofiled(b); ok {
		b = r
	}
	for _, want := range []string{b, cutAt(b, '('), cutAt(b, ':')} {
		for _, pid := range providers {
			for key, m := range all[pid].Models {
				if bareID(key) != want {
					continue
				}
				if e := m.efforts(); len(e) > 0 || !m.budgeted() {
					return slices.Clone(e), true
				}
			}
		}
	}
	return nil, false
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
// the catalog doesn't know (one typed in, or a vendor's own list's).
func DrawsID(id string) bool {
	id = strings.ToLower(id)
	for _, w := range []string{"image", "imagen", "imagine", "dall-e", "flux", "seedream", "cogview", "stable-diffusion", "sdxl", "wanx", "kolors", "hidream"} {
		if strings.Contains(id, w) {
			return true
		}
	}
	return false
}

// ImagesAPI is whether a model draws on an images API (/images/generations)
// rather than answering in chat with pictures: gpt-image, dall-e, imagen,
// flux, seedream… — not gemini-*-image or gpt-5-image, which chat.
func ImagesAPI(id string) bool {
	id = strings.ToLower(id)
	for _, w := range []string{"gpt-image", "chatgpt-image", "dall-e", "imagen", "imagine", "qwen-image", "wanx", "wan2", "seedream", "cogview", "flux", "stable-diffusion", "sdxl", "kolors", "hidream"} {
		if strings.Contains(id, w) {
			return true
		}
	}
	return false
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
func CodexHome() string {
	if dir := appdir.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// CodexModelsCache is the model list Codex CLI keeps, under CodexHome.
func CodexModelsCache() string { return filepath.Join(CodexHome(), "models_cache.json") }

// Codex returns the models Codex itself lists, straight from the cache the
// Codex CLI writes; there is no compiled-in list to fall back to.
func Codex() []Model {
	out, _ := filememo.Read("codex models", CodexModelsCache(), parseCodex)
	return slices.Clone(out)
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
		mm := Model{ID: m.Slug, Name: m.DisplayName, Provider: "openai", ImageInput: imageInput(m.Input)}
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

// Efforts returns the reasoning levels a model supports, if known.
func Efforts(models []Model, id string) []string {
	for _, m := range models {
		if m.ID == id && len(m.Efforts) > 0 {
			return m.Efforts
		}
	}
	return nil
}
