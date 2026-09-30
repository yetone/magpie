package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
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
	cfg := os.Getenv("XDG_CONFIG_HOME")
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
		omo(home),
		goose(home, cfg),
		cursor(home),
		copilot(home),
		crush(home, cfg),
		dsh(home),
		commandCode(home),
		fx(home),
		omp(home),
		devin(home, cfg),
		hermes(home),
		kimi(home),
		droid(home),
		cline(home),
		qoder(home),
		qoderCN(home),
		grok(home),
		zcode(home),
		workbuddy(home),
		hanako(home),
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
			name = p
		}
		opts := group(name, options(catalog.Provider(p), p+"/"))
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
			e["variants"] = openCodeVariants(m.Efforts)
			ms[m.ID] = e
		}
		return map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "magpie",
			"options": map[string]any{"baseURL": gw + "/v1", "apiKey": gateway.Token}, "models": ms}
	case "crush":
		var ms []map[string]any
		for _, m := range models {
			window := m.Context
			if window == 0 {
				window = 200000
			}
			ms = append(ms, map[string]any{"id": m.ID, "name": m.Name, "context_window": window, "default_max_tokens": 16384,
				"can_reason": len(m.Efforts) > 0})
		}
		if ms == nil {
			ms = []map[string]any{}
		}
		return map[string]any{"type": "openai", "name": "magpie", "base_url": gw + "/v1", "api_key": gateway.Token, "models": ms}
	case "pi":
		var ms []map[string]any
		for _, m := range models {
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
			if levels := piThinkingLevels(m.Efforts, e["api"] == "anthropic-messages"); levels != nil {
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
			ms = append(ms, e)
		}
		if ms == nil {
			ms = []map[string]any{}
		}
		return map[string]any{"name": "magpie", "baseUrl": gw + "/v1", "api": "openai-completions", "apiKey": gateway.Token, "models": ms}
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
// empty set, which OpenCode 2 takes as none rather than guessing.
func openCodeVariants(efforts []string) map[string]any {
	out := map[string]any{}
	for _, e := range efforts {
		out[e] = map[string]any{"reasoningEffort": e}
	}
	return out
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
// except on Anthropic's Messages API, where Pi's off sends thinking
// disabled, which Claude takes whatever its levels; there off is left out,
// for Pi to offer as before. A model whose levels magpie doesn't know gets
// no map: Pi's own defaults, as before.
func piThinkingLevels(efforts []string, anthropic bool) map[string]any {
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
	if levels["off"] == nil && anthropic {
		delete(levels, "off")
	}
	return levels
}

// openCodeLike is OpenCode and the forks that keep its config shape
// (mimocode): a provider block with npm/@ai-sdk/openai-compatible, model and
// small_model fields, and its own auth file beside its config.
func openCodeLike(id, name, icon, bin, dir, auth string, ua []string, aliases ...string) *Agent {
	// the first of its files there is, as the agent looks for them; with
	// none, a new <id>.json
	path := filepath.Join(dir, id+".json")
	for _, name := range []string{id + ".jsonc", id + ".json", "config.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			path = filepath.Join(dir, name)
			break
		}
	}
	provider := func() any { return magpieProviderJSONFor("opencode", id) }
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
		return usesMagpie(get("model"), get("small_model")) || id == "opencode" && openChamberOnMagpie()
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
			v, err := openCodeRef(path, id, v)
			if err != nil {
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: key, Value: v})
		}
	}
	return &Agent{
		ID: id, Name: name, Icon: icon, Aliases: aliases,
		UA:  ua,
		Bin: bin, Dir: dir, Path: path,
		Check: func() string {
			if !usesMagpie(get("model"), get("small_model")) {
				return ""
			}
			return wiringOff(name, path, func(k string) (string, bool) { return edit.GetJSON(path, "provider."+magpieID+".options."+k) },
				"baseURL", gatewayV1(), "apiKey", gateway.Token)
		},
		Sync: func() error {
			// a model of magpie's chosen, but its provider gone from the
			// file: put it back, or the agent has nothing to send it to
			if _, ok := edit.GetJSON(path, "provider."+magpieID); !ok && onMagpie() {
				return edit.SetJSON(path, edit.KV{Path: "provider." + magpieID, Value: provider()})
			}
			return syncJSON(path, "provider."+magpieID, provider)
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
	ref, ok := strings.CutPrefix(v, magpieID+"/")
	if !ok || !isMagpie(ref) {
		return v, nil
	}
	if own := ownGatewayProvider(path, ref); own != "" {
		return own + "/" + ref, nil
	}
	return v, edit.SetJSON(path, edit.KV{Path: "provider." + magpieID, Value: magpieProviderJSONFor("opencode", id)})
}

// ownGatewayProvider is the provider in an OpenCode config, other than
// magpie's own, whose baseURL is magpie's gateway and that lists the model
// ref, as a layout of one provider per family of magpie's models has; ""
// when there is none.
func ownGatewayProvider(path, ref string) string {
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
		if _, has := p.Models[ref]; name != magpieID && has && sameGateway(p.Options.BaseURL) {
			return name
		}
	}
	return ""
}

// sameGateway reports whether a base URL is magpie's gateway's, its host
// spelled 127.0.0.1 or localhost.
func sameGateway(base string) bool {
	norm := func(u string) string {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		return strings.Replace(u, "://localhost:", "://127.0.0.1:", 1)
	}
	return base != "" && norm(base) == norm(gatewayV1())
}

func opencode(home, cfg string) *Agent {
	return openCodeLike("opencode", "OpenCode", "opencode", "opencode",
		openCodeDir(cfg), filepath.Join(home, ".local", "share", "opencode", "auth.json"),
		[]string{"opencode"}, "oc")
}

// openCodeDir is the folder OpenCode reads its user config from:
// $OPENCODE_CONFIG_DIR when set, else $XDG_CONFIG_HOME/opencode (cfg).
// OpenCode 2, the one OpenChamber bundles (#321), reads that folder in place
// of the other; OpenCode 1 reads both, the variable's last, so what magpie
// writes there wins in either.
func openCodeDir(cfg string) string {
	if d := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG_DIR")); d != "" {
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
	if h := os.Getenv("MIMOCODE_HOME"); filepath.IsAbs(h) {
		dir = filepath.Join(h, "config")
	}
	return openCodeLike("mimocode", "MiMo Code", "mimocode", "mimo",
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
		if d := homeDir(at.home, os.Getenv("PI_CODING_AGENT_DIR")); d != "" {
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
	writeMagpie := func() error {
		return edit.SetJSON(modelsPath, edit.KV{Path: "providers." + magpieID, Value: magpieProviderJSONAt("pi", id, at.gw())})
	}
	return &Agent{
		ID: id, Name: name, Icon: id, Bin: id, Dir: dir, Path: path,
		Check: func() string {
			if p, _ := get("defaultProvider"); p != magpieID {
				return ""
			}
			return wiringOff(name, modelsPath, func(k string) (string, bool) { return edit.GetJSON(modelsPath, "providers."+magpieID+"."+k) },
				"baseUrl", at.v1(), "apiKey", gateway.Token)
		},
		Sync: func() error {
			return syncJSON(modelsPath, "providers."+magpieID, func() any { return magpieProviderJSONAt("pi", id, at.gw()) })
		},
		Fields: []Field{
			{
				Key: "model", Label: "model",
				// what a new session starts on, which enabledModels decides
				Get: func() string { return piStartup(path, pairGet(get, "defaultProvider", "defaultModel")()) },
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
					return append(ownOptions(auth, cur["model"]), viaMagpie(id, magpieID+"/")...)
				},
			},
			{
				// Pi's startup thinking level, the same list its /thinking offers;
				// Pi clamps it to what the model supports. Only this field is
				// written, and only when it changed: rebuilding providers.magpie
				// here replaced model fields a person had edited, such as a
				// contextWindow. Picking the model, and the catalog sync, still
				// refresh the provider.
				Key: "effort", Label: "thinking",
				Get: func() string { v, _ := get("defaultThinkingLevel"); return v },
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
				Options: func(map[string]string) []Option {
					return static("off", "minimal", "low", "medium", "high", "xhigh", "max")
				},
			},
		},
	}
}

func goose(home, cfg string) *Agent {
	path := filepath.Join(cfg, "goose", "config.yaml")
	if runtime.GOOS == "windows" {
		if app := os.Getenv("APPDATA"); app != "" {
			path = filepath.Join(app, "Block", "goose", "config", "config.yaml")
		}
	}
	get := func(k string) (string, bool) { return edit.GetYAMLTop(path, k) }
	set := func(kvs ...edit.KV) error { return edit.SetYAMLTop(path, kvs...) }
	return &Agent{
		ID: "goose", Name: "Goose", Icon: "goose", Bin: "goose", Dir: filepath.Dir(path), Path: path,
		UA: []string{"goose"},
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
			Get: pairGet(get, "GOOSE_PROVIDER", "GOOSE_MODEL"),
			Set: func(v string) error {
				if v == "" {
					return edit.DelYAMLTop(path, "GOOSE_PROVIDER", "GOOSE_MODEL")
				}
				return pairSet(set, "GOOSE_PROVIDER", "GOOSE_MODEL")(v)
			},
			Options: func(cur map[string]string) []Option {
				return ownOptions("", cur["model"], "anthropic", "openai", "google", "openrouter")
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
	if runtime.GOOS == "windows" {
		if app := os.Getenv("LOCALAPPDATA"); app != "" {
			path = filepath.Join(app, "crush", "crush.json")
		}
	}
	get := func(k string) (string, bool) { return edit.GetJSON(path, k) }
	set := func(kvs ...edit.KV) error { return edit.SetJSON(path, kvs...) }
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
	setter := func(pKey, mKey string) func(string) error {
		pair := pairSet(set, pKey, mKey)
		return func(v string) error {
			if v == "" {
				if err := edit.DelJSON(path, strings.TrimSuffix(pKey, ".provider")); err != nil {
					return err
				}
				large := pairGet(get, "models.large.provider", "models.large.model")()
				small := pairGet(get, "models.small.provider", "models.small.model")()
				if usesMagpie(large, small) {
					return nil
				}
				return edit.DelJSON(path, "providers."+magpieID)
			}
			if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
				if err := set(edit.KV{Path: "providers." + magpieID, Value: magpieProviderJSON("crush")}); err != nil {
					return err
				}
			}
			return pair(v)
		}
	}
	return &Agent{
		ID: "crush", Name: "Crush", Icon: "crush", Bin: "crush", Dir: filepath.Dir(path), Path: path,
		UA: []string{"crush"},
		Check: func() string {
			large, _ := get("models.large.provider")
			small, _ := get("models.small.provider")
			if large != magpieID && small != magpieID {
				return ""
			}
			return wiringOff("Crush", path, func(k string) (string, bool) { return get("providers." + magpieID + "." + k) },
				"base_url", gatewayV1(), "api_key", gateway.Token)
		},
		Sync: func() error {
			return syncJSON(path, "providers."+magpieID, func() any { return magpieProviderJSON("crush") })
		},
		Fields: []Field{
			{Key: "model", Label: "large", Get: pairGet(get, "models.large.provider", "models.large.model"), Set: setter("models.large.provider", "models.large.model"), Options: opts("model")},
			{Key: "small", Label: "small", Get: pairGet(get, "models.small.provider", "models.small.model"), Set: setter("models.small.provider", "models.small.model"), Options: opts("small")},
			{
				// the large model's reasoning_effort, which Crush's schema
				// takes as low, medium or high (for OpenAI-style models)
				Key: "effort", Label: "effort",
				Get: func() string { v, _ := get("models.large.reasoning_effort"); return v },
				Set: func(v string) error {
					if v == "" {
						return edit.DelJSON(path, "models.large.reasoning_effort")
					}
					if m, _ := get("models.large.model"); m == "" {
						return fmt.Errorf("pick Crush's large model first; the effort is kept with it")
					}
					return set(edit.KV{Path: "models.large.reasoning_effort", Value: v})
				},
				Options: func(map[string]string) []Option { return static("low", "medium", "high") },
			},
		},
	}
}
