package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// The gateway knows each agent's requests by what this package says of it.
func init() {
	usage.Agents = func() []usage.Known {
		var out []usage.Known
		for _, a := range Clients() {
			out = append(out, usage.Known{ID: a.ID, Names: append([]string{a.ID}, a.Aliases...), UA: a.UA})
		}
		return out
	}
	// the Sessions page reads the sessions of the agents in WSL distros
	sessions.WSLHomes = wslHomes
	sessions.WSLRunning = WSLRunning
}

// others are clients that reach the gateway without being agents magpie
// sets up: known only by their requests, to be drawn with a logo.
var others = []*Agent{
	{ID: "magpie", Name: "magpie", Icon: "magpie", UA: []string{"magpie"}},
	{ID: "curl", Name: "curl", Icon: "curl", UA: []string{"curl"}},
}

// Clients is everyone whose requests the gateway knows by name: every
// agent, detected or not, and the others. It is what the Usage and
// Routing views draw a request's client with.
func Clients() []*Agent {
	var out []*Agent
	for _, a := range All() {
		// a WSL agent's requests are its Windows twin's by their UA
		if a.WSL == "" {
			out = append(out, a)
		}
	}
	return append(out, others...)
}

// All returns every agent magpie knows about, detected or not.
func All() []*Agent {
	home, _ := os.UserHomeDir()
	cfg := appdir.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return append([]*Agent{
		claude(home),
		claudeDesktop(home),
		codex(home),
		gemini(home),
		agy(home),
		opencode(home, cfg),
		openChamber(home, cfg),
		mimocode(home, cfg),
		pi(home),
		aside(home),
		omo(home),
		goose(home, cfg),
		cursor(home),
		cursorLocal(),
		zed(home, cfg),
		vscode(home, cfg),
		vscodeInsidersAgent(home, cfg),
		vscodium(home, cfg),
		air(home, cfg),
		copilot(home),
		crush(home, cfg),
		dsh(home),
		reasonix(home),
		commandCode(home),
		fx(home),
		omp(home),
		devin(home, cfg),
		hermes(home),
		morph(home),
		kimi(home),
		muse(cfg),
		empryo(home),
		miniMax(home),
		droid(home),
		cline(home),
		qoder(home),
		qoderCN(home),
		grok(home),
		zcode(home),
		workbuddy(home),
		codebuddy(home),
		pencil(home),
		t3code(home),
		hanako(home),
		atomcode(home),
		alma(),
		cindy(),
	}, wslAgents()...)
}

// ---- accessors -------------------------------------------------------------

func jsonGet(path, key string) func() string {
	return func() string { v, _ := edit.GetJSON(path, key); return v }
}

func jsonSet(path, key string) func(string) error {
	return func(v string) error {
		if v == "" {
			return edit.DelJSON(path, key)
		}
		return edit.SetJSON(path, edit.KV{Path: key, Value: v})
	}
}

// usesMagpie reports whether any of the values is a magpie/… reference.
func usesMagpie(vals ...string) bool {
	for _, v := range vals {
		if strings.HasPrefix(v, magpieID+"/") {
			return true
		}
	}
	return false
}

// prefixed is an agent's Spelled whose values of magpie's are all
// magpie/<ref>, as its provider/model names them.
func prefixed(v string) bool { return strings.HasPrefix(v, magpieID+"/") }

// openCodeSpelled is the Spelled of an OpenCode config at path: magpie's
// provider, or one of the file's own that sends to magpie's gateway (v1),
// as openCodeRef names a model there.
func openCodeSpelled(path string, v1 func() string) func(string) bool {
	return func(v string) bool {
		if prefixed(v) {
			return true
		}
		p, ref, ok := strings.Cut(v, "/")
		if !ok || p == "" {
			return false
		}
		if !strings.Contains(p, ".") {
			base, _ := edit.GetJSON(path, "provider."+p+".options.baseURL")
			return sameGateway(base, v1())
		}
		return ownGatewayProvider(path, ref, v1()) == p
	}
}

// pair joins a provider field and a model field into one "provider/model"
// value, which is how OpenCode already spells it and how people think of it.
func pairGet(get func(string) (string, bool), pKey, mKey string) func() string {
	return func() string {
		p, _ := get(pKey)
		m, _ := get(mKey)
		switch {
		case m == "":
			return ""
		case p == "":
			return m
		}
		return p + "/" + m
	}
}

// magpieEffort is the reasoning_effort of the models.<type> object obj in
// Crush's data file when magpie set it: the object names magpie's provider
// and has no field but provider, model and reasoning_effort, which is all
// magpie ever writes there. An object Crush's picker saved is not magpie's.
func magpieEffort(data, obj, magpieID string) (string, bool) {
	raw, ok := edit.GetJSON(data, obj)
	if !ok {
		return "", false
	}
	var cur map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &cur) != nil {
		return "", false
	}
	var prov, effort string
	for k, v := range cur {
		switch k {
		case "provider":
			json.Unmarshal(v, &prov)
		case "model":
		case "reasoning_effort":
			json.Unmarshal(v, &effort)
		default:
			return "", false
		}
	}
	if prov != magpieID || effort == "" {
		return "", false
	}
	return effort, true
}

func pairSet(set func(...edit.KV) error, pKey, mKey string) func(string) error {
	return func(v string) error {
		p, m, ok := strings.Cut(v, "/")
		if !ok || p == "" || m == "" {
			return fmt.Errorf("expected provider/model, got %q", v)
		}
		return set(edit.KV{Path: pKey, Value: p}, edit.KV{Path: mKey, Value: m})
	}
}

func hostOf(u string) string { return provider.HostOf(u) }

// ---- option builders -------------------------------------------------------

func options(models []catalog.Model, prefix string) []Option {
	out := make([]Option, 0, len(models))
	for _, m := range models {
		out = append(out, Option{Value: prefix + m.ID, Note: m.Name, Icon: modelIcon(m.Provider, m.ID)})
	}
	return out
}

func static(vals ...string) []Option {
	out := make([]Option, len(vals))
	for i, v := range vals {
		out[i] = Option{Value: v}
	}
	return out
}

// ownOptions lists provider/model pairs an agent reaches on its own: the
// providers in its auth file, plus whatever the current value already uses.
func ownOptions(authFile string, cur string, extra ...string) []Option {
	return ownOptionsFrom(nil, authFile, cur, extra...)
}

// ownOptionsFrom is ownOptions for an agent with a model registry of its
// own (Pi, omp: nativemodels.go): a provider's models are the registry's
// when it has them, else models.dev's, and for openai-codex, which
// models.dev doesn't list, Codex CLI's (#709).
func ownOptionsFrom(native func(p string) []catalog.Model, authFile string, cur string, extra ...string) []Option {
	set := map[string]bool{}
	for _, p := range extra {
		set[p] = true
	}
	if p, _, ok := strings.Cut(cur, "/"); ok && p != magpieID {
		set[p] = true
	}
	if b, err := os.ReadFile(authFile); err == nil {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) == nil {
			for k := range m {
				set[k] = true
			}
		}
	}
	providers := make([]string, 0, len(set))
	for p := range set {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	var out []Option
	for _, p := range providers {
		name := catalog.ProviderName(p)
		if name == "" {
			name = nativeProviderNames[p]
		}
		if name == "" {
			name = p
		}
		var ms []catalog.Model
		if native != nil {
			ms = native(p)
		}
		if len(ms) == 0 {
			ms = catalog.Provider(p)
		}
		if len(ms) == 0 {
			ms = codexServed(p)
		}
		opts := group(name, options(ms, p+"/"))
		if ic := providerIcon(p); ic != "" {
			for i := range opts {
				opts[i].GroupIcon = ic
			}
		}
		out = append(out, opts...)
	}
	return out
}

// ---- agents ----------------------------------------------------------------

// magpieProviderJSON is the provider block agents with JSON configs get.
func magpieProviderJSON(shape string) any {
	return magpieProviderJSONFor(shape, shape)
}

// magpieProviderJSONFor is magpieProviderJSON for an agent whose catalog is
// narrowed under a different id than its config shape (mimocode shares
// OpenCode's shape but is seen as itself by magpie visible).
func magpieProviderJSONFor(shape, catalog string) any {
	return magpieProviderJSONAt(shape, catalog, gateway.URL())
}

// magpieProviderJSONAt is magpieProviderJSONFor for an agent that reaches
// the gateway at gw: one in a WSL distro under NAT (see wsl.go).
func magpieProviderJSONAt(shape, catalog, gw string) any {
	models := magpieModels(catalog) // the catalog is the agent's
	switch shape {
	case "opencode":
		ms := map[string]any{}
		for _, m := range models {
			e := map[string]any{"name": m.Name}
			if m.Images {
				e["attachment"] = true
				e["modalities"] = map[string]any{"input": []string{"text", "image"}, "output": []string{"text"}}
			}
			// without it OpenCode doesn't know when to compact, and a
			// group's context (magpie group set … context=) never reaches
			// it; an output of 0 is OpenCode's own default
			if m.Context > 0 {
				e["limit"] = map[string]any{"context": m.Context, "output": maxTokens(m)}
			}
			if openCodeReasons(m) {
				e["reasoning"] = true
			}
			e["variants"] = openCodeVariants(m.Efforts)
			ms[m.ID] = e
		}
		return map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "magpie",
			"options": map[string]any{"baseURL": gw + "/v1", "apiKey": keyAt(gw)}, "models": ms}
	case "crush":
		var ms []map[string]any
		for _, m := range models {
			window := m.Context
			if window == 0 {
				window = 200000
			}
			// without it Crush caps every reply at 16384 tokens, a model
			// that can write more of them never asked for it; an output
			// above the window is cut to it, as it is for every other
			// agent magpie hands a limit to
			tokens := maxTokens(m)
			if tokens == 0 {
				tokens = 16384
			}
			ms = append(ms, map[string]any{"id": m.ID, "name": m.Name, "context_window": window, "default_max_tokens": tokens,
				"can_reason": len(m.Efforts) > 0})
		}
		if ms == nil {
			ms = []map[string]any{}
		}
		return map[string]any{"type": "openai", "name": "magpie", "base_url": gw + "/v1", "api_key": keyAt(gw), "models": ms}
	case "pi":
		var ms []map[string]any
		for _, m := range models {
			ms = append(ms, piModelJSON(m, gw, true))
		}
		if ms == nil {
			ms = []map[string]any{}
		}
		return map[string]any{"name": "magpie", "baseUrl": gw + "/v1", "api": "openai-completions", "apiKey": keyAt(gw), "models": ms}
	}
	return nil
}

// piModelJSON is one of magpie's models as an entry of Pi's models.json.
// native asks it on the API its provider speaks natively (a model's own
// api and baseUrl); without it the model is left on the provider's
// openai-completions (Pencil, pencil.go).
func piModelJSON(m catalog.Model, gw string, native bool) map[string]any {
	// reasoning lets Pi offer its thinking levels for the model
	e := map[string]any{"id": m.ID, "name": m.Name, "reasoning": len(m.Efforts) > 0}
	// each model is asked on the API its provider speaks natively,
	// so the gateway relays what Pi sent as it is instead of
	// translating Chat. One served on OpenAI's Responses API alone,
	// or best there (a ChatGPT sign-in, GPT on OpenAI's API or
	// Copilot's), goes to baseUrl/responses; one on Anthropic's
	// Messages API alone to the gateway's /v1/messages (Anthropic's
	// SDK adds the /v1). A Claude that thinks only adaptively is
	// told so: Pi would otherwise ask it for a thinking budget,
	// which it turns away.
	switch {
	case !native:
	case slices.Contains(m.APIs, string(provider.Responses)):
		e["api"] = "openai-responses"
	case slices.Contains(m.APIs, string(provider.Anthropic)):
		e["api"], e["baseUrl"] = "anthropic-messages", gw
		if gateway.AdaptiveThinking(m.ID) {
			e["compat"] = map[string]any{"forceAdaptiveThinking": true}
		}
	}
	if m.Images {
		e["input"] = []string{"text", "image"}
	}
	if levels := piThinkingLevels(m.Efforts, e["api"] == "anthropic-messages" && gateway.ThinksOnlyWhenAsked(m.ID)); levels != nil {
		e["thinkingLevelMap"] = levels
	}
	// without it Pi takes every model for a 128K one, and compacts
	// a 272K or 922K one long before it has to
	if m.Context > 0 {
		e["contextWindow"] = m.Context
	}
	// without it Pi caps every reply at 16384 tokens, a model
	// that can write 128K included
	if m.Output > 0 {
		e["maxTokens"] = maxTokens(m)
	}
	// Pi shows a call's cost by it, and weighs warming a model's cache by
	// it and promptCache (#781): without either it warms nothing
	if m.Price != nil {
		e["cost"] = map[string]any{"input": m.Price.Input, "output": m.Price.Output, "cacheRead": m.Price.CacheRead, "cacheWrite": m.Price.CacheWrite}
	}
	if c := piPromptCache(e["api"], m.ID); c != nil {
		e["promptCache"] = c
	}
	return e
}

// piPromptCache is how long, in seconds, the vendor keeps a model's prompt
// cache for each of Pi's retention tiers, at the short end of what it
// publishes; nil where magpie doesn't relay Pi's request as it is, or the
// vendor's lifetime isn't known. Claude on Anthropic's Messages API keeps
// one 5 minutes, or an hour asked with a 1h cache_control (Pi's long); GPT
// on OpenAI's Responses API keeps one in memory 5 to 10 minutes, and only
// some models offer the 24h retention, so long is left out. A Claude
// subscription runs through Claude Code, which caches on its own: no api is
// set for it, and none is declared.
func piPromptCache(api any, id string) map[string]any {
	name := strings.ToLower(id[strings.LastIndex(id, "/")+1:])
	switch {
	case api == "anthropic-messages" && strings.HasPrefix(name, "claude"):
		return map[string]any{"short": 300, "long": 3600}
	case api == "openai-responses" && strings.HasPrefix(name, "gpt"):
		return map[string]any{"short": 300}
	}
	return nil
}

// openCodeVariants are the reasoning levels OpenCode offers for a model of
// magpie's, each asking the gateway for that effort as reasoning_effort
// (@ai-sdk/openai-compatible's reasoningEffort). OpenCode 1.x offers none
// for a model the config doesn't mark as reasoning, and adds these. OpenCode
// 2 makes low, medium and high for every model of an openai-compatible
// provider whose config names no variants (packages/core/src/variant.ts,
// config/plugin/provider.ts), so a model whose levels are none/high/max, or
// go past high to xhigh and max, or that has none, was offered levels it
// doesn't have and not the ones it has. A model without levels gets an
// empty set, which OpenCode 2 takes as none rather than guessing. They are
// written weakest first: OpenCode and OpenChamber list a model's variants in
// the object's key order, and a map had them alphabetical — default, high,
// low, max, medium, ultra, xhigh (#713).
func openCodeVariants(efforts []string) orderedJSON {
	out := orderedJSON{}
	for _, e := range gateway.ByStrength(efforts) {
		if !slices.ContainsFunc(out, func(p jsonPair) bool { return p.k == e }) {
			out = append(out, jsonPair{e, map[string]any{"reasoningEffort": e}})
		}
	}
	return out
}

// openCodeReasons is whether a model of magpie's is marked reasoning in
// opencode.json, which OpenCode 1 shows as 支持推理 in its model tooltip
// and takes for a model that thinks (#725: OpenCode Zen's
// mimo-v2.6-flash-free, which thinks with no levels to pick, said 不支持推理
// through magpie where it says it reasons under OpenCode's own Zen). OpenCode
// 1 also adds low, medium and high of its own to the variants of a reasoning
// model (ProviderTransform.variants; checked with 1.18.34), so one with levels
// is marked only when it has those three, else it would be offered levels it
// doesn't have; one with none gets them as it does under OpenCode's own
// provider, and the gateway asks a model with a thinking switch alone no more
// than high (fitFor). OpenCode 2 doesn't read the flag.
func openCodeReasons(m catalog.Model) bool {
	if len(m.Efforts) == 0 {
		return m.Reasoning
	}
	for _, e := range []string{"low", "medium", "high"} {
		if !slices.Contains(m.Efforts, e) {
			return false
		}
	}
	return true
}

// orderedJSON is a JSON object that keeps its keys in the order given,
// where a map's would come out sorted.
type orderedJSON []jsonPair

type jsonPair struct {
	k string
	v any
}

func (o orderedJSON) MarshalJSON() ([]byte, error) {
	b := []byte{'{'}
	for i, p := range o {
		if i > 0 {
			b = append(b, ',')
		}
		k, err := json.Marshal(p.k)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(p.v)
		if err != nil {
			return nil, err
		}
		b = append(append(append(b, k...), ':'), v...)
	}
	return append(b, '}'), nil
}

// piThinkingLevels is the thinkingLevelMap for a model's efforts: every one
// of Pi's levels (piLevels, pi-ai's EXTENDED_THINKING_LEVELS), the model's
// own mapped to themselves and the others null. Pi builds /thinking from
// the map (getSupportedThinkingLevels): a level set to null is hidden and
// skipped, while one left out is offered (all but xhigh and max), so a map
// naming only max had Pi offer minimal and medium too, which the vendor
// turned away (#243). Pi's off is the model's none when it takes one. A
// model without none has off hidden — Pi's off asks a Responses model for
// effort none and leaves a Chat model on the vendor's default thinking —
// except a Claude on Anthropic's Messages API (claudeOff), where Pi's off
// sends thinking disabled, which Claude takes whatever its levels; there
// off is left out, for Pi to offer as before. Another vendor's model there
// keeps off hidden: without none it always thinks, and ZCode's
// GLM-5.3-Flash turned Pi's off away ("该模型始终支持思考，不可关闭", #699),
// as a level left out is one Pi offers. A model whose levels magpie doesn't
// know gets no map: Pi's own defaults, as before.
func piThinkingLevels(efforts []string, claudeOff bool) map[string]any {
	if len(efforts) == 0 {
		return nil
	}
	levels := map[string]any{}
	for _, l := range piLevels {
		e := l
		if l == "off" {
			e = "none"
		}
		if slices.Contains(efforts, e) {
			levels[l] = e
		} else {
			levels[l] = nil
		}
	}
	if levels["off"] == nil && claudeOff {
		delete(levels, "off")
	}
	return levels
}

// openCodeLike is OpenCode and the forks that keep its config shape
// (mimocode): a provider block with npm/@ai-sdk/openai-compatible, model and
// small_model fields, and its own auth file beside its config.
// at is where it lives: this machine, or a WSL distro (see wsl.go), whose
// gateway address its provider names.
func openCodeLike(at place, id, name, icon, bin, dir, auth string, ua []string, aliases ...string) *Agent {
	// the first of its files there is, as the agent looks for them; with
	// none, a new <id>.json
	path := filepath.Join(dir, id+".json")
	for _, name := range []string{id + ".jsonc", id + ".json", "config.json"} {
		if at.exists(filepath.Join(dir, name)) {
			path = filepath.Join(dir, name)
			break
		}
	}
	provider := theirsKept(path, "provider."+magpieID, func() any { return magpieProviderJSONAt("opencode", id, at.gw()) }, "models")
	opts := func(key string) func(map[string]string) []Option {
		return func(cur map[string]string) []Option {
			return append(ownOptions(auth, cur[key]), viaMagpie(id, magpieID+"/")...)
		}
	}
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	// whether a model the file names, or one OpenChamber sends to the
	// OpenCode it runs on this config, is one of magpie's: magpie's
	// provider has to stay
	onMagpie := func() bool {
		return usesMagpie(get("model"), get("small_model")) || id == "opencode" && at.spell == nil && openChamberOnMagpie()
	}
	set := func(key string) func(string) error {
		return func(v string) error {
			if v == "" {
				if err := edit.DelJSON(path, key); err != nil {
					return err
				}
				if onMagpie() {
					return nil
				}
				return edit.DelJSON(path, "provider."+magpieID)
			}
			v, err := openCodeRefAt(at, path, id, v)
			if err != nil {
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: key, Value: v})
		}
	}
	return &Agent{
		ID: id, Name: name, Icon: icon, Aliases: aliases, Spelled: openCodeSpelled(path, at.v1),
		UA:  ua,
		Bin: bin, Dir: dir, Path: path,
		Check: func() string {
			if !usesMagpie(get("model"), get("small_model")) {
				return ""
			}
			return wiringOff(name, path, func(k string) (string, bool) { return edit.GetJSON(path, "provider."+magpieID+".options."+k) },
				"baseURL", at.v1(), "apiKey", at.gwKey())
		},
		Sync: func() error {
			// a model of magpie's chosen, but its provider gone from the
			// file: put it back, or the agent has nothing to send it to
			if _, ok := edit.GetJSON(path, "provider."+magpieID); !ok && onMagpie() {
				return edit.SetJSON(path, edit.KV{Path: "provider." + magpieID, Value: provider()})
			}
			return syncJSONInOrder(path, "provider."+magpieID, provider)
		},
		Fields: []Field{
			{Key: "model", Label: "model", Get: jsonGet(path, "model"), Set: set("model"), Options: opts("model")},
			{Key: "small", Label: "small", Get: jsonGet(path, "small_model"), Set: set("small_model"), Options: opts("small")},
		},
	}
}

// openCodeRef is the value that makes the OpenCode config at path (agent
// id's, whose catalog magpie's provider there lists) send model v. One of
// magpie's goes to a provider of the file's own that already sends it to
// magpie, named there rather than in a second list of the same models, else
// to magpie's provider, put in the file; any other is v as it is.
func openCodeRef(path, id, v string) (string, error) {
	return openCodeRefAt(here(""), path, id, v)
}

// openCodeRefAt is openCodeRef for the agent at a place, which reaches the
// gateway at its own address.
func openCodeRefAt(at place, path, id, v string) (string, error) {
	ref, ok := strings.CutPrefix(v, magpieID+"/")
	if !ok || !isMagpie(ref) {
		return v, nil
	}
	if own := ownGatewayProvider(path, ref, at.v1()); own != "" {
		return own + "/" + ref, nil
	}
	provider := theirsKept(path, "provider."+magpieID, func() any { return magpieProviderJSONAt("opencode", id, at.gw()) }, "models")
	return v, edit.SetJSON(path, edit.KV{Path: "provider." + magpieID, Value: provider()})
}

// ownGatewayProvider is the provider in an OpenCode config, other than
// magpie's own, whose baseURL is magpie's gateway (v1, as the agent reaches
// it) and that lists the model ref, as a layout of one provider per family
// of magpie's models has; "" when there is none.
func ownGatewayProvider(path, ref, v1 string) string {
	raw, ok := edit.GetJSON(path, "provider")
	if !ok {
		return ""
	}
	var ps map[string]struct {
		Options struct {
			BaseURL string `json:"baseURL"`
		} `json:"options"`
		Models map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal([]byte(raw), &ps) != nil {
		return ""
	}
	names := make([]string, 0, len(ps))
	for name := range ps {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := ps[name]
		if _, has := p.Models[ref]; name != magpieID && has && sameGateway(p.Options.BaseURL, v1) {
			return name
		}
	}
	return ""
}

// sameGateway reports whether a base URL is magpie's gateway's (v1), its
// host spelled 127.0.0.1 or localhost.
func sameGateway(base, v1 string) bool {
	norm := func(u string) string {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		return strings.Replace(u, "://localhost:", "://127.0.0.1:", 1)
	}
	return base != "" && norm(base) == norm(v1)
}

func opencode(home, cfg string) *Agent {
	return openCodeLike(here(home), "opencode", "OpenCode", "opencode", "opencode",
		openCodeDir(cfg), filepath.Join(home, ".local", "share", "opencode", "auth.json"),
		[]string{"opencode"}, "oc")
}

// opencodeIn is OpenCode in a WSL distro (see wsl.go): its config in
// ~/.config/opencode, as XDG_CONFIG_HOME and OPENCODE_CONFIG_DIR, the
// distro's variables, aren't read; OpenChamber, a Windows app, isn't there.
func opencodeIn(at place) *Agent {
	return openCodeLike(at, "opencode", "OpenCode", "opencode", "opencode",
		filepath.Join(at.home, ".config", "opencode"), filepath.Join(at.home, ".local", "share", "opencode", "auth.json"),
		[]string{"opencode"}, "oc")
}

// mimocodeIn is MiMo Code in a WSL distro: ~/.config/mimocode, as
// MIMOCODE_HOME isn't read there.
func mimocodeIn(at place) *Agent {
	return openCodeLike(at, "mimocode", "MiMo Code", "mimocode", "mimo",
		filepath.Join(at.home, ".config", "mimocode"), filepath.Join(at.home, ".local", "share", "mimocode", "auth.json"),
		[]string{"mimocode"}, "mimo")
}

// openCodeDir is the folder OpenCode reads its user config from:
// $OPENCODE_CONFIG_DIR when set, else $XDG_CONFIG_HOME/opencode (cfg).
// OpenCode 2, the one OpenChamber bundles (#321), reads that folder in place
// of the other; OpenCode 1 reads both, the variable's last, so what magpie
// writes there wins in either.
func openCodeDir(cfg string) string {
	if d := strings.TrimSpace(appdir.Getenv("OPENCODE_CONFIG_DIR")); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
	}
	return filepath.Join(cfg, "opencode")
}

// mimocode is MiMo Code, the CLI, and the engine inside Xiaomi MiMo, the
// desktop app (#249): both read the same config, which MIMOCODE_HOME moves
// to its own config folder.
func mimocode(home, cfg string) *Agent {
	dir := filepath.Join(cfg, "mimocode")
	if h := appdir.Getenv("MIMOCODE_HOME"); filepath.IsAbs(h) {
		dir = filepath.Join(h, "config")
	}
	return openCodeLike(here(home), "mimocode", "MiMo Code", "mimocode", "mimo",
		dir, filepath.Join(home, ".local", "share", "mimocode", "auth.json"),
		[]string{"mimocode"}, "mimo")
}

func pi(home string) *Agent { return piIn(here(home)) }

// piIn is Pi as it lives at a place: this machine's home, or a WSL
// distro's (see wsl.go), its models.json naming the gateway as it reaches
// it from there.
func piIn(at place) *Agent {
	a := piLike(at, "pi", "Pi", piDir(at))
	a.UA = []string{"pi-"}
	return a
}

// piDir is Pi's agent folder at a place: PI_CODING_AGENT_DIR's when set,
// "~" in it standing for home, else ~/.pi/agent (config.js, getAgentDir).
// A relative one is Pi's working directory's, which magpie can't know, so
// it is not taken; nor this machine's variable for a WSL distro's Pi.
func piDir(at place) string {
	if at.spell == nil {
		if d := homeDir(at.home, appdir.Getenv("PI_CODING_AGENT_DIR")); d != "" {
			return d
		}
	}
	return filepath.Join(at.home, ".pi", "agent")
}

// homeDir is the folder an agent's variable names, "~" and "~/…" expanded
// to home as Pi does; "" when it is empty or relative.
func homeDir(home, d string) string {
	if d == "~" || strings.HasPrefix(d, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(d, `~\`)) {
		d = filepath.Join(home, d[1:])
	}
	if !filepath.IsAbs(d) {
		return ""
	}
	return filepath.Clean(d)
}

// piLike is Pi, or a fork of it that keeps Pi's settings.json and
// models.json in an agent folder of its own (OmO, omo.go): id is its id,
// icon and command, dir its agent folder.
func piLike(at place, id, name, dir string) *Agent {
	path := filepath.Join(dir, "settings.json")
	modelsPath := filepath.Join(dir, "models.json")
	auth := filepath.Join(dir, "auth.json")
	get := func(k string) (string, bool) { return edit.GetJSON(path, k) }
	set := func(kvs ...edit.KV) error { return edit.SetJSON(path, kvs...) }
	pair := pairSet(set, "defaultProvider", "defaultModel")
	// the model a new session starts on, as the model field shows it
	startup := func() string { return piStartup(path, pairGet(get, "defaultProvider", "defaultModel")()) }
	block := theirsKept(modelsPath, "providers."+magpieID, func() any { return magpieProviderJSONAt("pi", id, at.gw()) }, "models")
	writeMagpie := func() error {
		return edit.SetJSON(modelsPath, edit.KV{Path: "providers." + magpieID, Value: block()})
	}
	return &Agent{
		ID: id, Name: name, Icon: id, Bin: id, Dir: dir, Path: path, Spelled: prefixed,
		Check: func() string {
			if p, _ := get("defaultProvider"); p != magpieID {
				return ""
			}
			return wiringOff(name, modelsPath, func(k string) (string, bool) { return edit.GetJSON(modelsPath, "providers."+magpieID+"."+k) },
				"baseUrl", at.v1(), "apiKey", at.gwKey())
		},
		Sync: func() error {
			return syncJSON(modelsPath, "providers."+magpieID, block)
		},
		Fields: []Field{
			{
				Key: "model", Label: "model",
				// what a new session starts on, which enabledModels decides
				Get: startup,
				Set: func(v string) error {
					if v == "" {
						if err := edit.DelJSON(path, "defaultProvider", "defaultModel"); err != nil {
							return err
						}
						if err := edit.DelJSON(modelsPath, "providers."+magpieID); err != nil {
							return err
						}
						return piScopeWithout(path)
					}
					if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
						if err := writeMagpie(); err != nil {
							return err
						}
					}
					if err := pair(v); err != nil {
						return err
					}
					// a model outside the user's Ctrl+P list would never start
					return piScopeWith(path, v)
				},
				Options: func(cur map[string]string) []Option {
					return append(ownOptionsFrom(piRegistry(id), auth, cur["model"]), viaMagpie(id, magpieID+"/")...)
				},
			},
			{
				// Pi's startup thinking level, the same list its /thinking offers;
				// Pi clamps it to what the model supports. Only this field is
				// written, and only when it changed: rebuilding providers.magpie
				// here replaced model fields a person had edited, such as a
				// contextWindow. Picking the model, and the catalog sync, still
				// refresh the provider.
				//
				// For a model of magpie's, the levels are those Pi offers for
				// it, from the entry magpie writes, and a level it doesn't
				// offer is shown as the one Pi runs it at: magpie showed max
				// for a group Pi offered off alone for, so ran without
				// reasoning (#597).
				Key: "effort", Label: "thinking",
				Get: func() string {
					v, _ := get("defaultThinkingLevel")
					if offered := piOffered(id, startup()); v != "" && offered != nil {
						return piClamp(v, offered)
					}
					return v
				},
				Set: func(v string) error {
					cur, _ := get("defaultThinkingLevel")
					if v == cur {
						return nil
					}
					if v == "" {
						return edit.DelJSON(path, "defaultThinkingLevel")
					}
					return set(edit.KV{Path: "defaultThinkingLevel", Value: v})
				},
				Options: func(cur map[string]string) []Option {
					if offered := piOffered(id, cur["model"]); offered != nil {
						return static(offered...)
					}
					return static(piLevels...)
				},
			},
		},
	}
}

func goose(home, cfg string) *Agent { return gooseIn(here(home), cfg) }

// gooseIn is Goose at a place whose config folder is cfg: this machine's,
// or a WSL distro's ~/.config (see wsl.go), where Goose keeps Linux's
// layout and its custom provider names the gateway as the distro reaches
// it, with the key it takes from there.
func gooseIn(at place, cfg string) *Agent {
	path := filepath.Join(cfg, "goose", "config.yaml")
	if runtime.GOOS == "windows" && at.spell == nil {
		if app := appdir.Getenv("APPDATA"); app != "" {
			path = filepath.Join(app, "Block", "goose", "config", "config.yaml")
		}
	}
	get := func(k string) (string, bool) { return edit.GetYAMLTop(path, k) }
	set := func(kvs ...edit.KV) error { return edit.SetYAMLTop(path, kvs...) }
	provider := gooseProviderPath(path)
	// the provider keys goose takes from its environment: this machine's
	// for this machine's goose, none of a distro's
	keyEnv := os.Getenv
	if at.spell != nil {
		keyEnv = func(string) string { return "" }
	}
	return &Agent{
		ID: "goose", Name: "Goose", Icon: "goose", Bin: "goose", Dir: filepath.Dir(path), Path: path, Spelled: prefixed,
		UA: []string{"goose"},
		Check: func() string {
			if p, _ := gooseActive(path); p != gooseProviderID {
				return ""
			}
			return wiringOff("Goose", provider, func(k string) (string, bool) { return edit.GetJSON(provider, k) },
				"base_url", at.v1(), "headers.Authorization", "Bearer "+at.gwKey())
		},
		Sync: func() error { return syncGooseProvider(provider, at) },
		// goose loads custom_providers when it starts
		Notice: func() string {
			if p, _ := gooseActive(path); p == gooseProviderID && Running(`Goose\.app/`, `(^|/)goose( |$)`) {
				return "Goose loads its providers at start-up — quit and reopen Goose (and open goose sessions) to use magpie's models."
			}
			return ""
		},
		// a goose on PATH may be pressly's database migration tool, a Go
		// program; Block's goose is Rust, so a Go goose is not the agent
		detect: func() bool {
			if isDir(filepath.Dir(path)) {
				return true
			}
			bin, err := exec.LookPath("goose")
			return err == nil && !goProgram(bin)
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			// the layout goose keeps it in, old or new (see goose.go)
			Get: func() string { return gooseModel(path) },
			Set: func(v string) error {
				if v == "" {
					if err := clearGooseModel(path); err != nil {
						return err
					}
					return removeGooseProvider(provider)
				}
				// a model of magpie's: magpie as a custom provider of
				// Goose's, each model with its window and whether Goose
				// can send it a thinking level (see goose.go)
				if ref, ok := strings.CutPrefix(v, gooseProviderID+"/"); ok && isMagpie(ref) {
					if err := writeGooseProvider(provider, at); err != nil {
						return err
					}
				}
				return setGooseModel(path, v)
			},
			Options: func(cur map[string]string) []Option {
				// only the native providers this goose is set up with (#987:
				// every one of four was listed, OpenRouter's hundreds of
				// models on a goose that had only magpie)
				return append(ownOptions("", cur["model"], gooseConfigured(path, keyEnv)...), viaMagpie("goose", gooseProviderID+"/")...)
			},
		}, {
			// GOOSE_THINKING_EFFORT, the effort goose asks of a model that
			// thinks, for every provider
			Key: "effort", Label: "effort",
			Get: func() string { v, _ := get("GOOSE_THINKING_EFFORT"); return v },
			Set: func(v string) error {
				if v == "" {
					return edit.DelYAMLTop(path, "GOOSE_THINKING_EFFORT")
				}
				return set(edit.KV{Path: "GOOSE_THINKING_EFFORT", Value: v})
			},
			Options: func(map[string]string) []Option {
				return static("off", "low", "medium", "high", "max")
			},
		}},
	}
}

func cursor(home string) *Agent {
	path := filepath.Join(home, ".cursor", "cli-config.json")
	return &Agent{
		ID: "cursor", Name: "Cursor", Icon: "cursor", Aliases: []string{"cursor-agent"},
		UA:  []string{"cursor"},
		Bin: "cursor-agent", Dir: filepath.Dir(path), Path: path,
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: jsonGet(path, "model.modelId"),
			Set: func(v string) error {
				if v == "" {
					return edit.DelJSON(path, "model", "hasChangedDefaultModel")
				}
				return edit.SetJSON(path,
					edit.KV{Path: "model.modelId", Value: v},
					edit.KV{Path: "model.displayModelId", Value: v},
					edit.KV{Path: "model.displayName", Value: v},
					edit.KV{Path: "hasChangedDefaultModel", Value: true},
				)
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: "auto", Note: "let Cursor pick", Icon: "cursor"}}
			},
		}},
	}
}

func copilot(home string) *Agent {
	dir := filepath.Join(home, ".copilot")
	path := filepath.Join(dir, "settings.json")
	return &Agent{
		ID: "copilot", Name: "Copilot CLI", Icon: "githubcopilot", Aliases: []string{"gh-copilot"},
		UA:  []string{"copilot", "github-copilot"},
		Bin: "copilot", Dir: dir, Path: path,
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: jsonGet(path, "model"),
			Set: jsonSet(path, "model"),
			Options: func(map[string]string) []Option {
				// "auto" is Copilot's own choice, not a model it lists; the rest
				// come from the list magpie last fetched from Copilot.
				out := []Option{{Value: "auto", Note: "let Copilot pick", Icon: "githubcopilot"}}
				if live, _, ok := catalog.Live("copilot"); ok {
					// magpie's list has Auto too, offered above
					live = slices.DeleteFunc(slices.Clone(live), func(m catalog.Model) bool { return m.ID == "auto" })
					out = append(out, options(live, "")...)
				}
				return out
			},
		}, {
			// effortLevel, which Copilot saves beside the model and clears
			// when its own /model changes the model; the levels are the
			// model's, low to xhigh when Copilot's list does not say
			Key: "effort", Label: "effort",
			Get: jsonGet(path, "effortLevel"),
			Set: jsonSet(path, "effortLevel"),
			Options: func(cur map[string]string) []Option {
				if live, _, ok := catalog.Live("copilot"); ok {
					if e := catalog.Efforts(live, cur["model"]); len(e) > 0 {
						return static(e...)
					}
				}
				return static("low", "medium", "high", "xhigh")
			},
		}},
	}
}

func crush(home, cfg string) *Agent {
	path := filepath.Join(cfg, "crush", "crush.json")
	// Crush saves the models its own picker chooses to its data file, and
	// reads that after crush.json, so a pick there wins over one in path.
	// The picks are read as Crush merges the two files and written where
	// Crush writes them; magpie's provider stays in path, beside the
	// library's MCP servers.
	data := filepath.Join(home, ".local", "share", "crush", "crush.json")
	if dir := appdir.Getenv("XDG_DATA_HOME"); dir != "" {
		data = filepath.Join(dir, "crush", "crush.json")
	}
	if runtime.GOOS == "windows" {
		if app := appdir.Getenv("LOCALAPPDATA"); app != "" {
			path = filepath.Join(app, "crush", "crush.json")
		}
		// %LOCALAPPDATA%\crush is Crush's data folder there
		data = path
	}
	return crushAt(here(home), path, data)
}

// crushIn is Crush in a WSL distro: ~/.config/crush/crush.json, Linux's
// place for it.
func crushIn(at place) *Agent {
	// Crush's data file is in Linux's place there too
	return crushAt(at, filepath.Join(at.home, ".config", "crush", "crush.json"),
		filepath.Join(at.home, ".local", "share", "crush", "crush.json"))
}

// crushAt is Crush with its config at path and its data file at data,
// reaching the gateway as at does.
func crushAt(at place, path, data string) *Agent {
	provider := theirsKept(path, "providers."+magpieID, func() any { return magpieProviderJSONAt("crush", "crush", at.gw()) }, "models")
	get := func(k string) (string, bool) { return edit.GetJSON(path, k) }
	set := func(kvs ...edit.KV) error { return edit.SetJSON(path, kvs...) }
	pick := func(k string) (string, bool) {
		if v, ok := edit.GetJSON(data, k); ok {
			return v, true
		}
		return get(k)
	}
	setPick := func(kvs ...edit.KV) error {
		// Crush keeps API keys in its data file, so one magpie is first to
		// make is made readable by its owner only
		if _, err := os.Lstat(data); errors.Is(err, fs.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(data, []byte("{}\n"), 0o600); err != nil {
				return err
			}
		}
		return edit.SetJSON(data, kvs...)
	}
	delPick := func(k string) error {
		if err := edit.DelJSON(data, k); err != nil {
			return err
		}
		return edit.DelJSON(path, k)
	}
	opts := func(key string) func(map[string]string) []Option {
		return func(cur map[string]string) []Option {
			var extra []string
			if b, err := os.ReadFile(path); err == nil {
				var c struct {
					Providers map[string]json.RawMessage `json:"providers"`
				}
				if json.Unmarshal(b, &c) == nil {
					for p := range c.Providers {
						if p != magpieID {
							extra = append(extra, p)
						}
					}
				}
			}
			return append(ownOptions("", cur[key], extra...), viaMagpie("crush", magpieID+"/")...)
		}
	}
	// replacePick puts v in place of the whole models.<type> object in the
	// data file. Crush's own picker saves its per-model settings there
	// (max_tokens, think, reasoning_effort, sampling, provider_options),
	// and Crush applies them to whatever model the object names, so they
	// must not stay attached to the model magpie picks: a max_tokens meant
	// for another model can go upstream as an over-limit request. Only an
	// effort magpie set itself is kept: one on the large pick when that pick
	// is already magpie's and carries nothing Crush adds.
	replacePick := func(obj string) func(string) error {
		return func(v string) error {
			p, m, ok := strings.Cut(v, "/")
			if !ok || p == "" || m == "" {
				return fmt.Errorf("expected provider/model, got %q", v)
			}
			next := map[string]any{"provider": p, "model": m}
			if obj == "models.large" {
				if e, ok := magpieEffort(data, obj, magpieID); ok {
					next["reasoning_effort"] = e
				}
			}
			return setPick(edit.KV{Path: obj, Value: next})
		}
	}
	setter := func(pKey, mKey string) func(string) error {
		pair := replacePick(strings.TrimSuffix(pKey, ".provider"))
		return func(v string) error {
			if v == "" {
				if err := delPick(strings.TrimSuffix(pKey, ".provider")); err != nil {
					return err
				}
				large := pairGet(pick, "models.large.provider", "models.large.model")()
				small := pairGet(pick, "models.small.provider", "models.small.model")()
				if usesMagpie(large, small) {
					return nil
				}
				return edit.DelJSON(path, "providers."+magpieID)
			}
			if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
				if err := set(edit.KV{Path: "providers." + magpieID, Value: provider()}); err != nil {
					return err
				}
			}
			return pair(v)
		}
	}
	return &Agent{
		ID: "crush", Name: "Crush", Icon: "crush", Bin: "crush", Dir: filepath.Dir(path), Path: path, Spelled: prefixed,
		UA: []string{"crush"},
		Check: func() string {
			large, _ := pick("models.large.provider")
			small, _ := pick("models.small.provider")
			if large != magpieID && small != magpieID {
				return ""
			}
			return wiringOff("Crush", path, func(k string) (string, bool) { return get("providers." + magpieID + "." + k) },
				"base_url", at.v1(), "api_key", at.gwKey())
		},
		Sync: func() error {
			return syncJSON(path, "providers."+magpieID, provider)
		},
		Fields: []Field{
			{Key: "model", Label: "large", Get: pairGet(pick, "models.large.provider", "models.large.model"), Set: setter("models.large.provider", "models.large.model"), Options: opts("model")},
			{Key: "small", Label: "small", Get: pairGet(pick, "models.small.provider", "models.small.model"), Set: setter("models.small.provider", "models.small.model"), Options: opts("small")},
			{
				// the large model's reasoning_effort, which Crush's schema
				// takes as low, medium or high (for OpenAI-style models)
				Key: "effort", Label: "effort",
				Get: func() string { v, _ := pick("models.large.reasoning_effort"); return v },
				Set: func(v string) error {
					if v == "" {
						return delPick("models.large.reasoning_effort")
					}
					if m, _ := pick("models.large.model"); m == "" {
						return fmt.Errorf("pick Crush's large model first; the effort is kept with it")
					}
					return setPick(edit.KV{Path: "models.large.reasoning_effort", Value: v})
				},
				Options: func(map[string]string) []Option { return static("low", "medium", "high") },
			},
		},
	}
}
