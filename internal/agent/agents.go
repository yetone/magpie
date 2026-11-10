package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
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
	sessions.WSLOff = func() bool { return !wslLooks() }
	// an agy conversation started on magpie's gateway is picked up again
	// there, with the variables and --model it was started with
	sessions.AgyStart = func() string {
		home, _ := homes()
		return agy(home).Launch()
	}
	// a Copilot CLI signed in inside a running distro (#723)
	provider.CopilotWSLDirs = copilotWSLDirs
	// the Codex app's app-server is restarted when magpie becomes Codex's
	// provider for its used-up ChatGPT account, and when it stops being it;
	// and when Codex is connected or disconnected, or its route otherwise
	// changes, so a running app sends where its config now says
	provider.CodexAppRoute = codexAppRoute
	// but not when Codex's account is switched while its provider takes
	// no ChatGPT account: the app-server goes by none
	provider.CodexNoAccount = codexNoAccount
	// and whether the takeover was turned off by the user rather than
	// ended by the reset (Keep on ChatGPT, Always ChatGPT, a Disconnect)
	provider.CodexTakeoverOff = func() bool {
		home, _ := homes()
		a := codex(home)
		return a.AppOutOff != nil && a.AppOutOff()
	}
	// and when none starts in place of the one ended, the app is
	// reopened on Windows, where its stdout was held open
	provider.CodexAppOpen = codexAppOpen
	provider.CodexAppReopen = codexAppReopen
	provider.CodexAppCrashed = codexAppCrashed
	provider.CodexAppRelaunch = codexAppRelaunch
	// and what the Codex app holds is read while Codex takes magpie's
	// models beside its sign-in, Codex switched off in magpie or not
	// Claude Code's own sign-in is left where the user put it while its
	// requests go through the gateway, which moves them to an account with
	// room by itself (oreoft on Discord)
	provider.ClaudeThroughMagpie = func() bool {
		home, _ := homes()
		at := here(home)
		return claudeRouted(at, filepath.Join(at.claudeDir(), "settings.json"))
	}
	provider.CodexHoldMatters = func() bool {
		home, _ := homes()
		a := codex(home)
		return a.HoldMatters != nil && a.HoldMatters()
	}
}

// codexAppRoute is where Codex's config sends it, as the app-server reads
// it at its start: the keys magpie writes for that, never one the Codex
// app writes itself (model, model_reasoning_effort); and whether magpie
// is Codex's provider for its used-up ChatGPT account (AppOut). Not known
// when Codex's config can't be read.
//
// The model list (model_catalog_json, the file and what it holds) is not
// in it. A change of route ends the app's app-server, and the ChatGPT app
// takes that as a crash and relaunches. The catalog follows magpie's names
// and the effort new sessions start at, which the app saves on each pick,
// so a route that hashed it ended the app every few minutes
// (indekkusu141209 on X, #4550). A new list is read at the app's next
// launch, and the Agents row offers a Restart for it (StaleCopies).
func codexAppRoute() (route string, out, known bool) {
	// asked every 3s for as long as magpie runs: the config looked at once
	// for its keys, not once for each (#1665)
	defer filememo.Hold()()
	home, _ := homes()
	a := codex(home)
	// opened, not read: a config.toml of 1.5 MB (Codex keeps every
	// project's trust level in it) read whole every 3s was 1.55 GB of a
	// live magpie's 33 GB in 53 minutes; the keys below come from filememo
	if f, err := os.Open(a.Path); err == nil {
		f.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, false
	}
	out = a.AppOut != nil && a.AppOut()
	var b strings.Builder
	var mp string
	for _, k := range []string{"model_provider", "openai_base_url"} {
		v, _ := edit.GetTOMLTop(a.Path, k)
		if k == "model_provider" {
			mp = v
		}
		b.WriteString(k + "=" + v + "\n")
	}
	t, _ := edit.GetTOMLTable(a.Path, "model_providers."+magpieID)
	b.WriteString("magpie.base_url=" + t["base_url"] + "\n")
	// and, while magpie's table is Codex's provider, whether it requires
	// OpenAI auth, which decides whether the app has its ChatGPT account
	// at all (openaiAuth in codex.go): read as the app-server starts, like
	// the rest. The app-server reads the account of the provider Codex is
	// on (codex-rs account_state), so beside the sign-in, on Codex's
	// built-in provider, the key follows the account running out and
	// coming back (openaiAuth) with nothing for the app to read again:
	// counted there, it ended the app's app-server at the limit and again
	// at the reset, and the ChatGPT app showed an error and relaunched
	// each time (xiaoq6285 on Discord, #4550)
	if mp == magpieID {
		b.WriteString("magpie.requires_openai_auth=" + t["requires_openai_auth"] + "\n")
	}
	if out {
		b.WriteString("out\n")
	}
	return b.String(), out, true
}

// codexNoAccount is whether the provider Codex's config.toml names takes
// no ChatGPT account: a model_provider whose [model_providers] table
// doesn't say requires_openai_auth = true (magpie's with the user's own
// false, or magpie as the provider with no sign-in to keep). Codex then
// sends the table's own key and its app-server reads no account
// (codex-rs ModelProviderInfo::account_state, resolve_provider_auth), so
// switching the account in auth.json changes nothing a running Codex
// goes by. The built-in openai provider, a provider with no table here, a
// profile (which may name another provider) or a config that can't be
// read is false: those may go by the account.
func codexNoAccount() bool {
	defer filememo.Hold()()
	home, _ := homes()
	a := codex(home)
	if _, err := os.ReadFile(a.Path); err != nil {
		return false
	}
	if p, _ := edit.GetTOMLTop(a.Path, "profile"); p != "" {
		return false
	}
	id, _ := edit.GetTOMLTop(a.Path, "model_provider")
	if id == "" || id == "openai" {
		return false
	}
	t, err := edit.GetTOMLTable(a.Path, "model_providers."+id)
	if err != nil || t == nil {
		return false
	}
	return t["requires_openai_auth"] != "true"
}

// others are clients that reach the gateway without being agents magpie
// sets up: known only by their requests, to be drawn with a logo.
var others = []*Agent{
	{ID: "magpie", Name: "magpie", Icon: "magpie", UA: []string{"magpie"}},
	{ID: "curl", Name: "curl", Icon: "curl", UA: []string{"curl"}},
	// PI-Desktop (vastsa/PI-Desktop) runs Pi's own library, which says
	// "pi (…)"; given a User-Agent of its own in its provider's settings
	// ("pi-desktop/0.1.0"), it is told apart from Pi by that name, which
	// Pi's "pi-" would take otherwise (#1966, dgmico)
	{ID: "pi-desktop", Name: "PI-Desktop", Icon: "pi", UA: []string{"pi-desktop"}},
	// Kimi Code Desktop runs Kimi Code's engine on ~/.kimi-code, so it
	// reaches magpie through Kimi Code's config, under a client name of
	// its own ("kimi-code-desktop/…", jiabubu on Discord). Its icon is the
	// app's own, from KimiCode-1.2.1-win-x64.exe's build/icon.ico.
	{ID: "kimi-code-desktop", Name: "Kimi Code Desktop", Icon: "kimi-code-desktop", UA: []string{"kimi-code-desktop"}},
	// Raft (slock's raft-computer) runs Pi in each of its agents' folders;
	// Sessions lists those as Raft's (internal/sessions/raft.go), while
	// their requests say "pi (…)" and are Pi's
	{ID: "raft", Name: "Raft", Icon: "generic"},
}

// Clients is everyone whose requests the gateway knows by name: every
// agent, detected or not, and the others. It is what the Usage and
// Routing views draw a request's client with.
func Clients() []*Agent { return ClientsIn(All()) }

// ClientsIn is Clients among all, a list of All() the caller has built
// already.
func ClientsIn(all []*Agent) []*Agent {
	var out []*Agent
	for _, a := range all {
		// a WSL agent's requests are its Windows twin's by their UA
		if a.WSL == "" {
			out = append(out, a)
		}
	}
	return append(out, others...)
}

// All returns every agent magpie knows about, detected or not: its own,
// then those plugins add.
func All() []*Agent {
	home, cfg := homes()
	own := builtins(home, cfg)
	return append(own, pluggedAgents(home, own, nil)...)
}

func homes() (home, cfg string) {
	home, _ = os.UserHomeDir()
	cfg = appdir.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return home, cfg
}

// builtins are the agents magpie has wiring of its own for.
func builtins(home, cfg string) []*Agent {
	return append(onMachine(home, cfg), wslAgents()...)
}

// local is All() without the agents in WSL distros, which wsl.exe is asked
// for: what start-up moves look in, on every command, `magpie version`
// too, and the moves came before WSL agents. A wsl.exe that hangs held each
// command 20s, its two listings' timeouts.
func local() []*Agent {
	home, cfg := homes()
	own := onMachine(home, cfg)
	return append(own, pluggedAgents(home, own, nil)...)
}

// onMachine are builtins but those in WSL.
func onMachine(home, cfg string) []*Agent {
	return append([]*Agent{
		claude(home),
		claudeDesktop(home),
		claudeScience(),
		codex(home),
		gemini(home),
		agy(home),
		antigravity(home),
		opencode(home, cfg),
		openChamber(home, cfg),
		mimocode(home, cfg),
		deveco(home, cfg),
		pi(home),
		stepcode(home),
		mothx(home),
		penguin(home),
		aside(home),
		omo(home),
		primeAgent(home),
		goose(home, cfg),
		cursor(home),
		cursorLocal(),
		zed(home, cfg),
		vscode(home, cfg),
		vscodeInsidersAgent(home, cfg),
		vscodium(home, cfg),
		zooCode(home, cfg),
		copilotJetBrains(home),
		air(home, cfg),
		airIDE(home, cfg),
		copilot(home),
		crush(home, cfg),
		dsh(home),
		dsbot(home),
		reasonix(home),
		commandCode(home),
		fx(home),
		omp(home),
		devin(home, cfg),
		hermes(home),
		morph(home),
		kimi(home),
		qwen(home),
		muse(cfg),
		empryo(home),
		nanobot(home),
		ante(home),
		astrbot(home),
		qwenpaw(home),
		openClaw(home),
		jcode(home),
		miniMax(home),
		droid(home),
		cline(home),
		qoder(home),
		qoderCN(home),
		grok(home),
		grokApp(home),
		zcode(home),
		workbuddy(home),
		workbuddyAI(home),
		codebuddy(home),
		pencil(home),
		openPencil(home, cfg),
		openBot(home, cfg),
		newMax(home),
		liveAgent(home),
		raycast(home),
		kiroCrew(home),
		t3code(home),
		openMausBot(home),
		hanako(home),
		atomcode(home),
		snow(home),
		alma(),
		cindy(),
		aionui(),
		cherryStudio(home),
		lody(home),
	}, append(ompProfiles(home), qwenpawAgents(home)...)...)
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
			return sameGateway(openCodeBaseURL(path, p), v1())
		}
		return ownGatewayProvider(path, ref, v1()) == p
	}
}

// openCodeBaseURL is the base URL of the provider named p in an OpenCode
// config at path. OpenCode 2 keeps a provider under providers, its base
// URL in settings, and reads one there before the same name under
// provider, OpenCode 1's spelling (packages/core/src/config/normalize.ts,
// mergeMaps, at v2.1.4); OpenChamber's provider form saves one there and
// takes it out of provider (#3915).
func openCodeBaseURL(path, p string) string {
	if _, ok := edit.GetJSON(path, "providers."+p); ok {
		base, _ := edit.GetJSON(path, "providers."+p+".settings.baseURL")
		return base
	}
	base, _ := edit.GetJSON(path, "provider."+p+".options.baseURL")
	return base
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
	if b, err := readOwnAuth(authFile); err == nil {
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
		if native == nil {
			out = append(out, ownBuilt.shared(p, func() []Option { return ownProvider(nil, p) })...)
		} else {
			out = append(out, ownProvider(native, p)...)
		}
	}
	return out
}

// ownBuilt is ownProvider's options built last per provider, for agents
// without a registry of their own: Goose's model field asked them each
// poll, for the picker and again for Drift, each provider's models made
// into options anew, 20 MB of forty polls with real accounts.
var ownBuilt stampedLists[Option]

// ownProvider is the options of one provider an agent reaches on its own.
func ownProvider(native func(p string) []catalog.Model, p string) []Option {
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
	ic := providerIcon(p)
	for i := range opts {
		opts[i].GroupIcon = ic
		// asked of the vendor on the agent's own sign-in or key: picked on
		// a connected agent it takes the agent off magpie, which the
		// picker says and asks first (maxleebeta: Pi's own OpenCode Go
		// model turned 「接入」 off, with nothing said)
		opts[i].Direct = name
	}
	return opts
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
		// models in the order magpieModels has them (#4575): OpenCode lists
		// the models in the file's key order, and a map's keys marshal
		// sorted — the variants under each kept theirs since #713
		ms := orderedJSON{}
		for _, m := range models {
			e := map[string]any{"name": m.Name}
			// modalities alone says a model takes images: OpenCode 1 takes
			// its image input from them, and its attachment flag, which
			// nothing of OpenCode 1.18 reads, is a legacy field OpenCode 2
			// drops with a WARN per model on every load (#2209)
			if m.Images {
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
			e["variants"] = openCodeVariants(m.Efforts, m.CodexFast)
			// OpenCode counts a session's cost by it, in USD per million
			// tokens (models.dev's cost; OpenCode 2.0.25 reads input,
			// output, cache_read, cache_write), and counted 0 without it
			if p := m.Price; p != nil {
				e["cost"] = map[string]any{"input": p.Input, "output": p.Output, "cache_read": p.CacheRead, "cache_write": p.CacheWrite}
			}
			ms = append(ms, jsonPair{m.ID, e})
		}
		// setCacheKey: without it OpenCode sends no prompt_cache_key, and an
		// upstream that caches by it misses every turn (#5468): OpenCode
		// 2.1.4 and later read it as compatibility.supportsPromptCacheKey
		// (v1/config/migrate.ts) and send the session's prompt_cache_key,
		// OpenCode 1 its camelCase promptCacheKey (the gateway's
		// cacheKeyCamel), and 2.0.25 leaves it be. A setCacheKey of the
		// user's own stays (openCodeKept)
		return map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "magpie",
			"options": map[string]any{"baseURL": gw + "/v1", "apiKey": openCodeKeyAt(catalog, gw), "setCacheKey": true}, "models": ms}
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
			cm := map[string]any{"id": m.ID, "name": m.Name, "context_window": window, "default_max_tokens": tokens,
				"can_reason": len(m.Efforts) > 0}
			// Crush counts a session's cost by it (catwalk's cost_per_1m_*,
			// Crush 0.39.3): in_cached is a cache write, out_cached a read
			if p := m.Price; p != nil {
				cm["cost_per_1m_in"], cm["cost_per_1m_out"] = p.Input, p.Output
				cm["cost_per_1m_in_cached"], cm["cost_per_1m_out_cached"] = p.CacheWrite, p.CacheRead
			}
			ms = append(ms, cm)
		}
		if ms == nil {
			ms = []map[string]any{}
		}
		return map[string]any{"type": "openai", "name": "magpie", "base_url": gw + "/v1", "api_key": keyAt(gw), "models": ms}
	case "pi":
		var ms []map[string]any
		for _, m := range models {
			ms = append(ms, piModelJSON(piAsked(catalog, m), gw, true))
		}
		if ms == nil {
			ms = []map[string]any{}
		}
		// Pi names its session to a provider only when asked to:
		// sendSessionAffinityHeaders has it send it on Chat
		// (x-session-affinity, session_id) and Anthropic's Messages API
		// (x-session-affinity). On Responses it sends session_id anyway.
		// Without it a turn of Pi's on Chat or Messages reached the gateway
		// with no session: its usage had none and "Record gateway
		// conversations" kept none of it (akic404 on Discord).
		return map[string]any{"name": "magpie", "baseUrl": gw + "/v1", "api": "openai-completions", "apiKey": keyAt(gw),
			"compat": map[string]any{"sendSessionAffinityHeaders": true}, "models": ms}
	}
	return nil
}

// piBlockKept is theirsKept for magpie's provider in a Pi models.json at
// path, where a sendSessionAffinityHeaders the file already has, the
// user's own or pi-cache-optimizer's (#1103), stays as it is, as does a
// model's maxTokens the user typed there (piOutputsKept).
func piBlockKept(path string, value func() any) func() any {
	return theirsKept(path, "providers."+magpieID, func() any {
		v := value()
		piOutputsKept(path, v)
		if _, set := edit.GetJSON(path, "providers."+magpieID+".compat.sendSessionAffinityHeaders"); !set {
			return v
		}
		if m, ok := v.(map[string]any); ok {
			if c, ok := m["compat"].(map[string]any); ok {
				delete(c, "sendSessionAffinityHeaders")
				if len(c) == 0 {
					delete(m, "compat")
				}
			}
		}
		return v
	}, "models")
}

// openCodeKept is theirsKept for magpie's provider in an OpenCode config at
// path: magpie's models are its own, written whole, but a variant the user
// put on one of them stays (#2379), where it is or after magpie's, and one
// they wrote over under a name of magpie's stays theirs. A variant as magpie
// writes it under its name is magpie's: one the model has no longer, a
// level gone or a fast mode lost, goes.
//
// A setCacheKey the file has already is the user's, false too (#5468), and
// so is a model's limit.output they typed there (openCodeOutputsKept).
func openCodeKept(path string, value func() any) func() any {
	return theirsKept(path, "provider."+magpieID, func() any {
		v := value()
		if block, ok := v.(map[string]any); ok {
			if ms, ok := block["models"].(orderedJSON); ok {
				openCodeOutputsKept(path, ms)
			}
		}
		if _, set := edit.GetJSON(path, "provider."+magpieID+".options.setCacheKey"); set {
			v = withoutOption(v, "options", "setCacheKey")
		}
		raw, ok := edit.GetJSON(path, "provider."+magpieID+".models")
		if !ok {
			return v
		}
		var have map[string]json.RawMessage
		block, _ := v.(map[string]any)
		ms, _ := block["models"].(orderedJSON)
		if json.Unmarshal([]byte(raw), &have) != nil || ms == nil {
			return v
		}
		for id, h := range have {
			i := slices.IndexFunc(ms, func(p jsonPair) bool { return p.k == id })
			if i < 0 {
				continue
			}
			e, _ := ms[i].v.(map[string]any)
			ours, ok := e["variants"].(orderedJSON)
			var was struct {
				Variants json.RawMessage `json:"variants"`
			}
			if !ok || json.Unmarshal(h, &was) != nil || len(was.Variants) == 0 {
				continue
			}
			for _, p := range objectPairs(was.Variants) {
				if sameJSON(string(p.v.(json.RawMessage)), openCodeVariant(p.k)) {
					continue
				}
				if i := slices.IndexFunc(ours, func(o jsonPair) bool { return o.k == p.k }); i >= 0 {
					ours[i].v = p.v
				} else {
					ours = append(ours, p)
				}
			}
			e["variants"] = ours
		}
		return v
	}, "models")
}

// withoutOption is magpie's provider block v without key in its object
// under in, for a value the user set there themselves: a copy, so a block
// another writer holds is left as it was; in goes when nothing else is in it.
func withoutOption(v any, in, key string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	o, ok := m[in].(map[string]any)
	if !ok {
		return v
	}
	out := maps.Clone(m)
	o = maps.Clone(o)
	delete(o, key)
	if len(o) == 0 {
		delete(out, in)
	} else {
		out[in] = o
	}
	return out
}

// objectPairs are a JSON object's keys and values in the order it lists
// them; none when it is not an object.
func objectPairs(raw []byte) orderedJSON {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var out orderedJSON
	for d.More() {
		t, err := d.Token()
		k, ok := t.(string)
		var v json.RawMessage
		if err != nil || !ok || d.Decode(&v) != nil {
			return nil
		}
		out = append(out, jsonPair{k, v})
	}
	return out
}

// piAsked is m as agentID's models.json can ask it: without the Gemini API
// where nothing that reads the file can be trusted to stream
// google-generative-ai, so such a model stays on the provider's Chat,
// which the gateway translates.
//
//   - StepCode registers no google-generative-ai stream, only Pi's
//     anthropic-messages, openai-completions and openai-responses
//     (packages/providers/src/compat.ts BUILTIN_APIS, Step-Code at
//     90a57820), though its docs list it (#3282).
//   - Pi's own agent folder is read by every app that runs Pi's SDK on it,
//     not by Pi alone. Raft (slock's raft-daemon, the Pi runtime of its
//     agents) opens ~/.pi/agent/models.json (or PI_CODING_AGENT_DIR's) and
//     hands every request its own fetch (installProviderHttpClient), which
//     pi-ai's Google adapter refuses before sending: "Custom fetch is not
//     supported by the Google Generative AI adapter" (pi-ai 0.83.0 and
//     1.4.0, api/google-generative-ai.js; yunfei9958 on X). Per-request
//     fetch is a feature of Pi's SDK, so any other host of it can do the
//     same. OmO's and Prime Agent's folders are their own, and keep it.
func piAsked(agentID string, m catalog.Model) catalog.Model {
	if agentID == "stepcode" || agentID == "pi" {
		m.APIs = slices.DeleteFunc(slices.Clone(m.APIs), func(a string) bool { return a == string(provider.Gemini) })
	}
	return m
}

// piModelJSON is one of magpie's models as an entry of Pi's models.json.
// native selects the model's preferred gateway API (its own api and
// baseUrl); without it the model is left on the provider's
// openai-completions (Pencil, pencil.go).
func piModelJSON(m catalog.Model, gw string, native bool) map[string]any {
	// reasoning lets Pi offer its thinking levels for the model: as
	// /v1/models says it (provider.Entry.Thinks), so one that thinks with
	// no levels magpie knows (Trae CN's kimi-k3-max, #2380) is offered
	// Pi's own off to high, which the gateway fits to the model, where
	// it had off alone
	e := map[string]any{"id": m.ID, "name": m.Name, "reasoning": m.Reasoning || len(m.Efforts) > 0}
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
		// strict left out of a tool of a turn asked there is taken as a
		// strict-mode schema, every property required, optional ones
		// filled (#3055): pi-ai says strict: false on an ordinary tool
		// only when the model's compat says so
		e["api"] = "openai-responses"
		e["compat"] = map[string]any{"supportsStrictMode": true}
	case slices.Contains(m.APIs, string(provider.Anthropic)):
		e["api"], e["baseUrl"] = "anthropic-messages", gw
		if gateway.AdaptiveThinking(m.ID) {
			e["compat"] = map[string]any{"forceAdaptiveThinking": true}
		}
	case slices.Contains(m.APIs, string(provider.Gemini)):
		e["api"], e["baseUrl"] = "google-generative-ai", gw+"/v1beta"
	}
	if m.Images {
		e["input"] = []string{"text", "image"}
	}
	if levels := piThinkingLevels(m.Efforts, e["api"] == "anthropic-messages" && gateway.ThinksOnlyWhenAsked(m.ID)); levels != nil {
		// on google-generative-ai Pi resolves a level through the map to
		// one of Gemini's four (pi-ai's resolveGoogleThinkingLevel) and
		// throws before sending on any other name, so a level the model
		// calls something else (xhigh, max) is hidden there (#3282)
		if e["api"] == "google-generative-ai" {
			for l, v := range levels {
				if s, ok := v.(string); ok && l != "off" && !slices.Contains([]string{"minimal", "low", "medium", "high"}, strings.ToLower(s)) {
					levels[l] = nil
				}
			}
		}
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
	} else if native && e["api"] == nil {
		// a routing group has no api of its own (a Messages-only group stays
		// on Chat, provider.GroupAPI), and went out with no promptCache, which
		// stops Pi's cacheWarming with "cache lifetime unavailable" (#5037)
		// — while whatever Claude member it picks has the gateway's own
		// translation marking the request ephemeral for five minutes
		// (addedMark). That short lifetime the group declares; long stays
		// out, the gateway marks an hour only where a session's turns are
		// often far apart, so it isn't always true.
		if _, ms, ok := provider.GroupFinder()(m.ID); ok {
			for _, g := range ms {
				if g.Provider.Native(g.Model) == provider.Anthropic {
					e["promptCache"] = map[string]any{"short": 300}
					break
				}
			}
		}
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
//
// A model Codex's /fast is asked of (catalog.Model.CodexFast) has each level
// again fast after them, low-fast to xhigh-fast, or fast alone when it has
// none (#2379): OpenCode has no /fast, and a variant is one pick, level and
// tier together.
func openCodeVariants(efforts []string, fast bool) orderedJSON {
	out := orderedJSON{}
	for _, e := range gateway.ByStrength(efforts) {
		if !slices.ContainsFunc(out, func(p jsonPair) bool { return p.k == e }) {
			out = append(out, jsonPair{e, openCodeVariant(e)})
		}
	}
	if !fast {
		return out
	}
	if len(out) == 0 {
		return orderedJSON{{openCodeFast, openCodeVariant(openCodeFast)}}
	}
	for _, p := range slices.Clone(out) {
		k := p.k + "-" + openCodeFast
		out = append(out, jsonPair{k, openCodeVariant(k)})
	}
	return out
}

// openCodeFast is the name of a fast variant, alone or after a level's.
const openCodeFast = "fast"

// openCodeVariant is the variant magpie writes under name: a level asked as
// reasoning_effort (@ai-sdk/openai-compatible's reasoningEffort, which
// OpenCode 2 reads too), and a fast one asks for priority processing too,
// in the words Codex's /fast sends it: service_tier "priority". Each
// OpenCode puts that on the request its own way: OpenCode 1 hands a variant's
// options to @ai-sdk/openai-compatible under the provider's name, which
// sends the ones it doesn't know, service_tier among them, as they are;
// OpenCode 2 sends what extraBody holds (packages/core/src/v1/config/
// provider-options.ts) and drops a key it doesn't know. Checked with
// OpenCode 2.0.25 against a fake server: service_tier alone, or
// serviceTier, never reached it; with extraBody it went as
// "service_tier":"priority" beside reasoning_effort. OpenCode 1 sends
// extraBody on as a field of its own, which the gateway drops when it asks
// the ChatGPT backend on Responses.
func openCodeVariant(name string) map[string]any {
	level, fast := strings.CutSuffix(name, "-"+openCodeFast)
	if name == openCodeFast {
		level, fast = "", true
	}
	v := map[string]any{}
	if level != "" {
		v["reasoningEffort"] = level
	}
	if fast {
		v["service_tier"] = "priority"
		v["extraBody"] = map[string]any{"service_tier": "priority"}
	}
	return v
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

// modelEffort is the model's own name for level, one of pi-ai's (none for
// its off): level itself when efforts name it, else the one of efforts
// that names it in a vendor's words (catalog.EffortAliases: Trae's light
// and extra_high, as Our Free Model's plugin lists them), "" when it takes
// none such. dsh and Pi offered only the levels named in pi-ai's words, so
// such a model's light, high and extra_high showed in dsh as high alone
// (nianshou5 on Discord). The name returned is what the agent sends, so
// the vendor gets the level in the words it listed.
func modelEffort(efforts []string, level string) string {
	if slices.Contains(efforts, level) {
		return level
	}
	for _, e := range efforts {
		if catalog.EffortLevel(e) == level {
			return e
		}
	}
	return ""
}

// piThinkingLevels is the thinkingLevelMap for a model's efforts: every one
// of Pi's levels (piLevels, pi-ai's EXTENDED_THINKING_LEVELS), the model's
// own mapped to its name for it (modelEffort) and the others null. Pi
// builds /thinking from the map (getSupportedThinkingLevels): a level set
// to null is hidden and skipped, while one left out is offered (all but
// xhigh and max), so a map
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
		w := l
		if l == "off" {
			w = "none"
		}
		if e := modelEffort(efforts, w); e != "" {
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
	block := func() any { return magpieProviderJSONAt("opencode", id, at.gw()) }
	// the file as the agent reads it: in OpenCode 2's words for an
	// OpenCode 2, asked each time, as an update may have come since
	file := func() ocFile { return ocFile{path: path, two: id == "opencode" && openCodeTwo(at), block: block} }
	opts := func(key string) func(map[string]string) []Option {
		return func(cur map[string]string) []Option {
			return withVia(ownOptions(auth, cur[key]), id, magpieID+"/")
		}
	}
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	// whether a model the file names, or one OpenChamber sends to the
	// OpenCode it runs on this config, is one of magpie's: magpie's
	// provider has to stay
	onMagpie := func() bool {
		return usesMagpie(get("model"), file().small()) || id == "opencode" && at.spell == nil && openChamberOnMagpie()
	}
	set := func(key string) func(string) error {
		put := func(v string) error {
			if key == "small_model" {
				return file().setSmall(v)
			}
			if v == "" {
				return edit.DelJSON(path, key)
			}
			return edit.SetJSON(path, edit.KV{Path: key, Value: v})
		}
		return func(v string) error {
			if v == "" {
				if err := put(""); err != nil {
					return err
				}
				if onMagpie() {
					return nil
				}
				return edit.DelJSON(path, "provider."+magpieID, openCodeNativeKey)
			}
			v, err := openCodeRefAt(at, path, id, v)
			if err != nil {
				return err
			}
			return put(v)
		}
	}
	return &Agent{
		ID: id, Name: name, Icon: icon, Aliases: aliases, Spelled: openCodeSpelled(path, at.v1),
		UA:  ua,
		Bin: bin, Dir: dir, Path: path,
		Check: func() string {
			if !usesMagpie(get("model"), file().small()) {
				return ""
			}
			return openCodeWiringOff(name, path, at.v1(), openCodeKeyAt(id, at.gw()))
		},
		ListOnly: true,
		Sync: func() error {
			// a model of magpie's chosen, but its provider gone from the
			// file: put it back, or the agent has nothing to send it to
			return file().sync(onMagpie())
		},
		Fields: []Field{
			{Key: "model", Label: "model", Get: jsonGet(path, "model"), Set: set("model"), Options: opts("model")},
			{Key: "small", Label: "small", Get: func() string { return file().small() }, Set: set("small_model"), Options: opts("small")},
		},
	}
}

// openCodeKeyAt is the key an OpenCode-shaped agent (id) sends the gateway
// at gw: the gateway's own, which OpenCode and MiMo Code are told apart by
// their User-Agents with, or for DevEco Code, parts of whose code still
// send OpenCode's User-Agent, one that names it.
func openCodeKeyAt(id, gw string) string {
	if id == "deveco" {
		return agentKeyAt(id, gw)
	}
	return keyAt(gw)
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
	block := func() any { return magpieProviderJSONAt("opencode", id, at.gw()) }
	return v, ocFile{path: path, two: id == "opencode" && openCodeTwo(at), block: block}.sync(true)
}

// ownGatewayProvider is the provider in an OpenCode config, other than
// magpie's own, whose baseURL is magpie's gateway (v1, as the agent reaches
// it) and that lists the model ref, as a layout of one provider per family
// of magpie's models has; "" when there is none. A provider under
// providers is read as OpenCode 2 reads it, before the same name under
// provider (openCodeBaseURL).
func ownGatewayProvider(path, ref, v1 string) string {
	type own struct {
		base   string
		models map[string]json.RawMessage
	}
	ps := map[string]own{}
	if raw, ok := edit.GetJSON(path, "provider"); ok {
		var v1s map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
			Models map[string]json.RawMessage `json:"models"`
		}
		if json.Unmarshal([]byte(raw), &v1s) == nil {
			for name, p := range v1s {
				ps[name] = own{p.Options.BaseURL, p.Models}
			}
		}
	}
	if raw, ok := edit.GetJSON(path, "providers"); ok {
		var v2s map[string]struct {
			Settings struct {
				BaseURL string `json:"baseURL"`
			} `json:"settings"`
			Models map[string]json.RawMessage `json:"models"`
		}
		if json.Unmarshal([]byte(raw), &v2s) == nil {
			for name, p := range v2s {
				ps[name] = own{p.Settings.BaseURL, p.Models}
			}
		}
	}
	names := make([]string, 0, len(ps))
	for name := range ps {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := ps[name]
		if _, has := p.models[ref]; name != magpieID && has && sameGateway(p.base, v1) {
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
		at.join(".config", "opencode"), at.join(".local", "share", "opencode", "auth.json"),
		[]string{"opencode"}, "oc")
}

// mimocodeIn is MiMo Code in a WSL distro: ~/.config/mimocode, as
// MIMOCODE_HOME isn't read there.
func mimocodeIn(at place) *Agent {
	return openCodeLike(at, "mimocode", "MiMo Code", "mimocode", "mimo",
		at.join(".config", "mimocode"), at.join(".local", "share", "mimocode", "auth.json"),
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

// deveco is DevEco Code, Huawei's fork of OpenCode for HarmonyOS apps
// (openharmony-sig/deveco-code, #1885). It keeps OpenCode's config shape
// in its own folder, $XDG_CONFIG_HOME/deveco, merging config.json,
// deveco.json and deveco.jsonc there (packages/opencode/src/config,
// loadGlobal); DEVECO_CONFIG_DIR adds a folder read after it, as
// OPENCODE_CONFIG_DIR does OpenCode's, so what magpie writes there wins.
// Its auth is xdg-basedir's data folder's, ~/.local/share/deveco. 0.2.3's
// requests to a provider of the config's say "deveco/0.2.3 ai-sdk/…", but
// much of the OpenCode code it keeps still sends OpenCode's own User-Agent
// (opencode/<version>), so its key names it as well (openCodeKeyAt).
func deveco(home, cfg string) *Agent {
	return openCodeLike(here(home), "deveco", "DevEco Code", "deveco-color", "deveco",
		devecoDir(cfg), filepath.Join(home, ".local", "share", "deveco", "auth.json"), []string{"deveco"})
}

// devecoDir is the folder DevEco Code reads its user config from:
// $DEVECO_CONFIG_DIR when set to an absolute folder, else
// $XDG_CONFIG_HOME/deveco (cfg).
func devecoDir(cfg string) string {
	if d := strings.TrimSpace(appdir.Getenv("DEVECO_CONFIG_DIR")); filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(cfg, "deveco")
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
	return at.join(".pi", "agent")
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

// beforeMagpie is a copy of an agent's own files taken before magpie first
// writes them (file.before-magpie beside each), which Disconnect puts back
// byte for byte when what it left says the same as the copy: the same
// JSON, in the user's own order, spacing and comments-free layout. Where
// they differ (the user changed something while connected), what
// Disconnect left stands. A file that wasn't there (named in the stash
// under absent) goes again when nothing but empty objects is left in it.
type beforeMagpie struct {
	absent string
	files  []string
}

// take copies each file aside, or notes it absent; a file that can't be
// read is neither, and isn't put back.
func (b beforeMagpie) take() {
	var gone []string
	for _, f := range b.files {
		raw, err := os.ReadFile(f)
		switch {
		case err == nil:
			if edit.WriteAtomic(f+".before-magpie", raw) != nil {
				os.Remove(f + ".before-magpie")
			}
		case errors.Is(err, fs.ErrNotExist):
			os.Remove(f + ".before-magpie")
			gone = append(gone, filepath.Base(f))
		default:
			os.Remove(f + ".before-magpie")
		}
	}
	stash(map[string]string{b.absent: strings.Join(gone, "\n")})
}

// restore puts back what take kept, where Disconnect left the same.
func (b beforeMagpie) restore() error {
	gone := strings.Split(unstash(b.absent), "\n")
	for _, f := range b.files {
		cur, err := os.ReadFile(f)
		if err != nil {
			os.Remove(f + ".before-magpie")
			continue
		}
		var now any
		if json.Unmarshal(edit.TrimBOM(cur), &now) != nil { // a BOM is the user's own (#1534)
			continue
		}
		if was, err := os.ReadFile(f + ".before-magpie"); err == nil {
			var then any
			if json.Unmarshal(edit.TrimBOM(was), &then) == nil && reflect.DeepEqual(now, then) && !bytes.Equal(cur, was) {
				if err := edit.WriteAtomic(f, was); err != nil {
					return err
				}
			}
			os.Remove(f + ".before-magpie")
			continue
		}
		if slices.Contains(gone, filepath.Base(f)) && emptyJSON(now) {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
	}
	return nil
}

// emptyJSON says v is an object holding nothing but empty objects.
func emptyJSON(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, x := range m {
		if !emptyJSON(x) {
			return false
		}
	}
	return true
}

// piLike is Pi, or a fork of it that keeps Pi's settings.json and
// models.json in an agent folder of its own (OmO, omo.go): id is its id,
// icon and command, dir its agent folder.
func piLike(at place, id, name, dir string) *Agent {
	return piLikeKeyed(at, id, name, dir, at.gwKey)
}

// piLikeKeyed is piLike whose provider "magpie" carries key's key: one
// that names the agent (agentKeyAt) for an agent whose requests carry
// nothing else of it (Prime Agent, primeagent.go).
func piLikeKeyed(at place, id, name, dir string, key func() string) *Agent {
	path := filepath.Join(dir, "settings.json")
	modelsPath := filepath.Join(dir, "models.json")
	auth := filepath.Join(dir, "auth.json")
	get := func(k string) (string, bool) { return edit.GetJSON(path, k) }
	set := func(kvs ...edit.KV) error { return edit.SetJSON(path, kvs...) }
	pair := pairSet(set, "defaultProvider", "defaultModel")
	// the model a new session starts on, as the model field shows it
	startup := func() string { return piStartup(path, pairGet(get, "defaultProvider", "defaultModel")()) }
	block := piBlockKept(modelsPath, func() any {
		b := magpieProviderJSONAt("pi", id, at.gw()).(map[string]any)
		b["apiKey"] = key()
		return b
	})
	// Pi's own: magpie's decision and image models, for codemode
	// (pi_extension.go); a fork's extensions may not be Pi's
	extension := func() error { return nil }
	if id == "pi" {
		extension = func() error { return piExtensionSynced(dir, modelsPath, at.v1(), key()) }
	}
	writeMagpie := func() error {
		if err := edit.SetJSON(modelsPath, edit.KV{Path: "providers." + magpieID, Value: block()}); err != nil {
			return err
		}
		return extension()
	}
	// the user's two files as they were before magpie's first write, put
	// back byte for byte by Disconnect where it leaves them saying the same
	before := beforeMagpie{at.key(id + ".before.absent"), []string{path, modelsPath}}
	// the entries of enabledModels a sync took out (scope_gone.go)
	gone := at.key(id + ".scope.gone")
	return &Agent{
		ID: id, Name: name, Icon: id, Bin: id, Dir: dir, Path: path, Spelled: prefixed,
		Restore: before.restore,
		Check: func() string {
			if p, _ := get("defaultProvider"); p != magpieID {
				return ""
			}
			return wiringOff(name, modelsPath, func(k string) (string, bool) { return edit.GetJSON(modelsPath, "providers."+magpieID+"."+k) },
				"baseUrl", at.v1(), "apiKey", key())
		},
		ListOnly: true,
		Sync: func() error {
			if err := syncJSON(modelsPath, "providers."+magpieID, block); err != nil {
				return err
			}
			if err := extension(); err != nil {
				return err
			}
			// an entry on a model magpie no longer gives Pi names nothing
			// (#2055, scope_gone.go)
			return piScopeSynced(path, modelsPath, gone)
		},
		// a provider renamed takes its magpie/<id>/ entries in enabledModels
		// with it: an entry left on the old id names nothing, and Pi would
		// leave that model out of Ctrl+P (#1919, #1828)
		RenameRefs: func(from, to string) (bool, error) {
			scopeGoneRename(gone, from, to)
			next, ok := renameRefList(piScope(path), from, to)
			if !ok {
				return false, nil
			}
			return true, edit.SetJSON(path, edit.KV{Path: "enabledModels", Value: next})
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
						if err := extension(); err != nil {
							return err
						}
						forget(gone)
						return piScopeWithout(path)
					}
					if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
						if _, has := edit.GetJSON(modelsPath, "providers."+magpieID); !has {
							if p, _ := get("defaultProvider"); p != magpieID {
								before.take()
							}
						}
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
					return withVia(ownOptionsFrom(piRegistry(id), auth, cur["model"]), id, magpieID+"/")
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
	path := filepath.Join(at.open(filepath.Join(cfg, "goose")), "config.yaml")
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
		ID: "goose", Name: "Goose", Icon: "goose", Bin: "goose", Dir: filepath.Dir(path), Path: path, Spelled: prefixed, NoNames: true,
		UA: []string{"goose"},
		Check: func() string {
			if p, _ := gooseActive(path); p != gooseProviderID {
				return ""
			}
			return wiringOff("Goose", provider, func(k string) (string, bool) { return edit.GetJSON(provider, k) },
				"base_url", at.v1(), "headers.Authorization", "Bearer "+gooseKey(at))
		},
		ListOnly: true,
		Sync:     func() error { return syncGooseProvider(provider, at) },
		// goose loads custom_providers when it starts
		Notice: func() string {
			if p, _ := gooseActive(path); p == gooseProviderID && Running(`Goose\.app/`, `(^|/)goose( |$)`) {
				return noticeGoose.String()
			}
			return ""
		},
		// a goose on PATH may be pressly's database migration tool, a Go
		// program; Block's goose is Rust, so a Go goose is not the agent
		detect: func() bool {
			if agentDir(filepath.Dir(path)) {
				return true
			}
			bin, err := proc.Look("goose")
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
				return withVia(ownOptions("", cur["model"], gooseConfigured(path, keyEnv)...), "goose", gooseProviderID+"/")
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
		// Cursor CLI, not Cursor: the row is cursor-agent, and the Cursor
		// editor is not on it (@G_Z_Y on X looked for Cursor CLI on the
		// Agents page and took "Cursor" for the editor)
		ID: "cursor", Name: "Cursor CLI", Icon: "cursor", Aliases: []string{"cursor-agent"},
		UA:  []string{"cursor"},
		Bin: "cursor-agent", Dir: filepath.Dir(path), Path: path,
		// the editor reads ~/.cursor's mcp.json, skills and hooks, and its
		// chats are listed with the CLI's
		shared: "Cursor",
		// the Cursor editor makes ~/.cursor (argv.json, extensions, mcp.json,
		// projects) with no cursor-agent anywhere, so the folder says nothing
		// of the CLI: cli-config.json or the cursor-agent command does
		// (@G_Z_Y on X saw a Cursor row that wasn't the CLI)
		dirShared: true,
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
	// COPILOT_HOME moves the whole folder, settings.json and providers.json
	// with it (Copilot's `copilot help environment`)
	if d := appdir.Getenv("COPILOT_HOME"); d != "" {
		dir = d
	}
	path := filepath.Join(dir, "settings.json")
	// Copilot CLI's own multi-provider file (BYOK): $COPILOT_PROVIDERS_CONFIG,
	// else providers.json beside settings.json. It needs no GitHub sign-in.
	provs := filepath.Join(dir, "providers.json")
	if p := appdir.Getenv("COPILOT_PROVIDERS_CONFIG"); p != "" {
		provs = p
	}
	own := func() []Option {
		// "auto" is Copilot's own choice, not a model it lists; the rest
		// come from the list magpie last fetched from Copilot.
		out := []Option{{Value: "auto", Note: "let Copilot pick", Icon: "githubcopilot"}}
		if live, _, ok := catalog.Live("copilot"); ok {
			// magpie's list has Auto too, offered above
			live = slices.DeleteFunc(slices.Clone(live), func(m catalog.Model) bool { return m.ID == "auto" })
			out = append(out, options(live, "")...)
		}
		return out
	}
	return &Agent{
		ID: "copilot", Name: "Copilot CLI", Icon: "githubcopilot", Aliases: []string{"gh-copilot"},
		UA:  []string{"copilot", "github-copilot"},
		Bin: "copilot", Dir: dir, Path: path,
		// GitHub Copilot's JetBrains and VS Code plugins write ~/.copilot
		// too (agents, skills, hooks, instructions, prompts, ide), so the
		// folder says nothing of the CLI: its settings.json or the copilot
		// command does (#1495)
		dirShared: true,
		// VS Code 1.143 ships a copilot launcher of its own in
		// bin\copilot-shim in its install folder, which is on the user's
		// PATH, and running it only asks for the CLI to be installed
		// (#3269): a command of the editor's is not the CLI
		binNot: editorLauncher,
		// magpie/<model> is a model of the provider named magpie in providers.json
		Spelled:  prefixed,
		ListOnly: true,
		Check: func() string {
			if v, _ := edit.GetJSON(path, "model"); !prefixed(v) {
				return ""
			}
			p := copilotProvider(provs)
			return wiringOff("Copilot CLI", provs, func(k string) (string, bool) { v, ok := p[k].(string); return v, ok },
				"baseUrl", gateway.URL()+"/v1", "apiKey", agentKey("copilot"))
		},
		Sync: func() error { return copilotSync(provs, gateway.URL()+"/v1", agentKey("copilot")) },
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: jsonGet(path, "model"),
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if err := copilotWrite(provs, gateway.URL()+"/v1", agentKey("copilot")); err != nil {
						return err
					}
				}
				if v == "" {
					if err := edit.DelJSON(path, "model"); err != nil {
						return err
					}
					return copilotUnwrite(provs)
				}
				return edit.SetJSON(path, edit.KV{Path: "model", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				return withVia(own(), "copilot", magpieID+"/")
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

// editorLauncher reports whether bin is a launcher an editor ships beside
// itself, not the CLI it is named after (#3269): an editor's install is
// told by the product.json it keeps beside its program, as
// vscodeCodeBinary tells one. VS Code 1.143 puts its copilot launcher in
// <install>\bin\copilot-shim, with <install>\resources\app\product.json
// in a Windows or Linux install; a Mac's app keeps bin in
// Contents/Resources/app, beside product.json. So only bin's own folder
// and the two above it are looked in, never further up, where a folder of
// the user's (a home, a checkout of an editor's source) may hold a
// product.json of its own. A link on PATH is judged where it points.
// A copilot a user installed itself, in npm's global folder, Homebrew's,
// winget's Links or scoop's shims, is still the CLI.
//
// VS Code's versioned Windows layout (1.139's win32VersionedUpdate) moves
// that product.json one level under the install root: the app's payload
// lives in a folder named for the build's commit — the short hash, ten
// lowercase hex digits, 2c33b50b16 for 1.144.0 — with resources\app under
// it and bin beside it (microsoft/vscode#343045; Tony-ooo's 1.144.0, whose
// every product.json the six places above miss, #3269). A folder of the
// user's beside bin is not named as a commit's short hash is, so the cap
// keeps its meaning: nothing wider than the same three folders is read.
func editorLauncher(bin string) bool {
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		bin = real
	}
	dir, err := filepath.Abs(filepath.Dir(bin))
	if err != nil {
		return false
	}
	for range 3 {
		if isFile(filepath.Join(dir, "product.json")) ||
			isFile(filepath.Join(dir, "resources", "app", "product.json")) {
			return true
		}
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() && commitHashName(e.Name()) &&
					isFile(filepath.Join(dir, e.Name(), "resources", "app", "product.json")) {
					return true
				}
			}
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return false
}

// commitHashName is whether a folder is named as a build's short commit
// hash is: ten lowercase hexadecimal digits (16507d91be, 2c33b50b16).
func commitHashName(name string) bool {
	if len(name) != 10 {
		return false
	}
	for _, c := range []byte(name) {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
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
	return crushAt(at, at.join(".config", "crush", "crush.json"),
		at.join(".local", "share", "crush", "crush.json"))
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
				if json.Unmarshal(edit.TrimBOM(b), &c) == nil {
					for p := range c.Providers {
						if p != magpieID {
							extra = append(extra, p)
						}
					}
				}
			}
			return withVia(ownOptions("", cur[key], extra...), "crush", magpieID+"/")
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
		ListOnly: true,
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

// what goose says after a change (notice.go)
var (
	noticeGoose = newNotice("Goose loads its providers at start-up — quit and reopen Goose (and open goose sessions) to use magpie's models.")
)

// readOwnAuth is os.ReadFile for an agent's own sign-ins (OpenCode's
// auth.json and its kin), left unread with account discovery off (#1535).
func readOwnAuth(path string) ([]byte, error) {
	if settings.AccountDiscoveryOff() {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(path)
}
