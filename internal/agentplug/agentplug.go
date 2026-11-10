// Package agentplug runs the agents plugins add: a module, named by a
// plugin's package.json magpie.agent, that says where an agent magpie has
// no wiring of its own keeps its model, and what else to write there for
// it to use magpie's gateway. internal/agent makes each one an Agent.
//
// A module exports
//
//	export const agent = {
//	  id: "aider", name: "Aider",
//	  config: "~/.aider.conf.yml", // the file magpie edits
//	  model: "model",              // the key path of the model in it
//	  prefix: "openai/",           // what the agent wants before the id
//	}
//	export function connect({ gateway, model, models }) {
//	  return { "openai-api-base": gateway.v1, "openai-api-key": gateway.key }
//	}
//
// and it runs in moejs in magpie's own process, as gateway middleware
// does: there is no Node here, so a package it imports is bundled into it.
//
// An agent whose model is picked in the agent itself (Raycast's own
// picker, among the models connect writes) leaves model out: it is connect
// only, a switch on the Agents page as an app whose one setting is magpie
// as its provider is, and connect is called without a model (#2374).
//
// An agent that keeps the model a new session starts on in a file apart
// from the one connect writes (PiG: the provider and its models in
// models.json, defaultProvider and defaultModel in settings.json) says so
// with startup, and model stays out: the row picks the model, written into
// startup's file with startup's keys beside it, and connect writes the
// rest into config. A magpie without startup reads the same module as
// connect only, so a plugin can say it to every magpie.
//
//	startup: {
//	  config: "~/.pig/agent/settings.json",
//	  model: "defaultModel",
//	  keys: { defaultProvider: "magpie" },
//	}
//
// The agent export may also say where its agent reads what magpie's
// Library gives (internal/library): an instructions file, an MCP file in
// one of MCPFormats, and a skills folder; each is optional.
//
//	library: {
//	  instructions: "~/.aider/AGENTS.md",
//	  mcp: "~/.aider/mcp.json", mcpFormat: "claude",
//	  skills: "~/.aider/skills",
//	}
//
// An agent that takes the gateway only from how it is started (agy reads
// GEMINI_API_KEY and GOOGLE_GEMINI_BASE_URL from its environment, and
// resolves a model only given on its command line) says so with launch,
// beside model and bin. While it is on one of magpie's models its row
// gives the command, and magpie puts a function named bin in the user's
// shells that runs `magpie <id> run`: the agent started with env, each
// {url}, {v1} and {key} the gateway's, and the model after launch.model's
// flag (none when the user gives that flag).
//
//	launch: {
//	  env: { GEMINI_API_KEY: "{key}", GOOGLE_GEMINI_BASE_URL: "{url}" },
//	  model: "--model",
//	}
//
// merge names keys whose object connect returns only magpie's members of:
// each member is written on its own beside the user's, and taken out
// again when magpie steps out (agy's customModelsConfig.customModels).
// effort: true gives its row a field for how hard magpie's models reason,
// which the gateway asks its requests for (an agent with no setting of
// its own for it). wsl: true, for an agent that takes a built-in's place
// (magpie's agentMovers), has it wired in WSL distros too where the
// built-in was; aliases are other names it is typed as.
//
// An agent that reads a settings file of magpie's own only when it is so
// started (agy's --settings) names it as config, the flag that hands it
// over in launch.args, and its own settings file as base: magpie writes
// only config, which it removes when magpie steps out, and the user's
// own model goes in base. connect's input and own's have base, parsed,
// so connect can carry the user's own entries over (agy takes
// customModels whole from the --settings file). launch.plain are the
// arguments that start it as it is (agy's remote-control, which needs
// its Google sign-in).
//
//	config: "~/.gemini/antigravity-cli/magpie-settings.json",
//	base: "~/.gemini/antigravity-cli/settings.json",
//	launch: { env: {…}, model: "--model", args: { "--settings": "{config}" }, plain: ["remote-control", "--remote-control"] },
//
// A module may also export own({ config, base, model }), given its config
// file (and base) as parsed JSON ({} for none) and the model it is on
// now, returning the agent's own models for its picker, each { value,
// model, direct }: the value written, the model's name for its picture,
// and the vendor it goes to without magpie.
package agentplug

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/yetone/magpie/internal/jsmod"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
)

// Formats are the config files an agent plugin's file may be, by the
// name its agent.format gives or its extension says.
var Formats = map[string]string{
	".json": "json", ".jsonc": "json",
	".yaml": "yaml", ".yml": "yaml",
	".env": "env",
}

// FormatOf is the format a config file's name says it is in, "" for one
// it doesn't say.
func FormatOf(config string) string {
	if strings.HasPrefix(strings.ToLower(filepath.Base(config)), ".env") {
		return "env"
	}
	return Formats[strings.ToLower(filepath.Ext(config))]
}

// Desc is what a module's agent export says.
type Desc struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Config string   `json:"config"`
	Format string   `json:"format"`
	Model  string   `json:"model"`
	Prefix string   `json:"prefix"`
	Bin    string   `json:"bin"`
	Dir    string   `json:"dir"`
	UA     []string `json:"ua"`
	Icon   string   `json:"icon"`
	// Notice is advice shown after a change, as a built-in agent's is
	// (an agent that reads its config at start-up needs a restart)
	Notice string `json:"notice"`
	// Library is where the agent reads the Library's instructions, MCP
	// servers and skills
	Library Places `json:"library"`
	// Startup is the file and key of the model a new session starts on,
	// for an agent that keeps it apart from config; nil for one that
	// keeps it in config (Model) or picks it itself
	Startup *Startup `json:"startup"`
	// Base is the agent's own settings file, for one whose config is a
	// file of magpie's alone (launch.args names it): the user's model is
	// there off magpie, and connect and own are given it as base; magpie
	// never writes it but for the user's own model picked in its row
	Base string `json:"base"`
	// Launch is how the agent is started on magpie, for one that takes the
	// gateway only from its environment and command line; nil for one its
	// config file connects
	Launch *Launch `json:"launch"`
	// Merge are key paths whose object connect returns magpie's members
	// of, each written beside the user's own (json only)
	Merge []string `json:"merge"`
	// Effort gives the agent's row magpie's effort field
	Effort bool `json:"effort"`
	// WSL has the agent in WSL distros too
	WSL bool `json:"wsl"`
	// Aliases are other names the agent is typed as
	Aliases []string `json:"aliases"`
}

// Launch is the environment an agent is started with, by variable, each
// value {key}, {url} or {v1} (the gateway's), or a path under the address,
// and Model the flag that names its model on the command line ("" for
// none).
type Launch struct {
	Env   map[string]string `json:"env"`
	Model string            `json:"model"`
	// Args are flags put before the user's arguments, each with its value
	// "{config}", the path of agent.config: agy's --settings, for a config
	// that is magpie's own file the agent reads only when so started
	Args map[string]string `json:"args"`
	// Plain are arguments (a subcommand or a flag, alone or with
	// "=value") with which the agent starts as it is, off magpie: agy's
	// remote-control, which needs its own sign-in
	Plain []string `json:"plain"`
}

// Fill is v with {url}, {v1} and {key} as given.
func Fill(v, url, v1, key string) string {
	return strings.NewReplacer("{url}", url, "{v1}", v1, "{key}", key).Replace(v)
}

// Own is one of an agent's own models, as own() gives it.
type Own struct {
	Value  string `json:"value"`
	Model  string `json:"model"`
	Direct string `json:"direct"`
}

// Startup is where an agent keeps the model it starts on, apart from the
// file connect writes: Config and Format as Desc's are, Model the key
// path of the model in it, and Keys what is written beside it, as it is
// (PiG's defaultProvider, the provider id its connect writes).
type Startup struct {
	Config string         `json:"config"`
	Format string         `json:"format"`
	Model  string         `json:"model"`
	Keys   map[string]any `json:"keys"`
}

// Places are where an agent reads what the Library gives it, each a path
// as Config is (~ and $VAR expanded); an empty one is a thing it isn't
// given.
type Places struct {
	Instructions string `json:"instructions"`
	MCP          string `json:"mcp"`
	// MCPFormat is how its MCP file lists servers: one of MCPFormats
	MCPFormat string `json:"mcpFormat"`
	Skills    string `json:"skills"`
}

// MCPFormats are the MCP files an agent plugin's agent may keep, each
// named for the agent whose file it is written as: "claude" is
// {"mcpServers": {name: {type, command, args, env, url, headers}}}.
var MCPFormats = []string{
	"alma", "antigravity", "claude", "cline", "codex", "commandcode", "copilot",
	"crush", "cursor", "devin", "gemini", "goose", "grok", "hermes", "kimi",
	"omp", "opencode", "zed",
}

// Agent is one plugin's agent, compiled.
type Agent struct {
	Spec string // the plugin's spec in plugins.json
	File string
	Desc Desc

	mu      sync.Mutex
	rt      *moejs.Runtime
	connect moejs.Hook
	has     bool
	own     moejs.Hook
	hasOwn  bool
}

// How long the module may take to load, and connect to answer.
var (
	loadLimit    = time.Second
	connectLimit = time.Second
)

var errTimeout = errors.New("took too long")

var (
	idOK   = lazyre.New(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	binOK  = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	envOK  = lazyre.New(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	flagOK = lazyre.New(`^--?[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
	// a plain argument: a subcommand or a flag
	plainOK = lazyre.New(`^-{0,2}[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
	// a launch variable's value is magpie's address or key, a path under
	// the address at most: it goes into the user's shell profiles, and a
	// variable such as LD_PRELOAD or NODE_OPTIONS gets nothing a plugin
	// chose
	envValueOK = lazyre.New(`^(\{key\}|\{(url|v1)\}(/[A-Za-z0-9._~-]+)*/?)$`)
)

type set struct {
	stamp string
	list  []*Agent
	errs  map[string]string // by spec: why it didn't load
}

var (
	loadMu    sync.Mutex
	cur       atomic.Pointer[set]
	nextCheck atomic.Int64
)

// List is the agents the plugins switched on add, in plugins.json's
// order, and why any didn't load, by plugin spec. It is loaded again when
// plugins.json or a module's file changed, which is looked at once a
// second.
func List() ([]*Agent, map[string]string) {
	s := current()
	return s.list, s.errs
}

// Reload drops the agents loaded, so the next List loads them again.
func Reload() {
	loadMu.Lock()
	cur.Store(nil)
	loadMu.Unlock()
}

func current() *set {
	s := cur.Load()
	now := time.Now().UnixNano()
	n := nextCheck.Load()
	if s != nil && (now < n || !nextCheck.CompareAndSwap(n, now+int64(time.Second))) {
		return s
	}
	st := stamp()
	if s != nil && s.stamp == st {
		return s
	}
	loadMu.Lock()
	defer loadMu.Unlock()
	if s := cur.Load(); s != nil && s.stamp == st {
		return s
	}
	s = load(st)
	cur.Store(s)
	return s
}

type found struct {
	e    plugin.Entry
	file string
}

func modules() []found {
	var out []found
	for _, e := range plugin.Load().Plugins {
		if e.Off {
			continue
		}
		if file, _ := plugin.Agent(plugin.Target(e.Spec)); file != "" {
			out = append(out, found{e, file})
		}
	}
	return out
}

func stamp() string {
	var b strings.Builder
	b.WriteString(plugin.ListStamp())
	for _, f := range modules() {
		b.WriteString("|" + f.file)
		if fi, err := os.Stat(f.file); err == nil {
			fmt.Fprint(&b, ":", fi.ModTime().UnixNano(), ":", fi.Size())
		}
	}
	return b.String()
}

func load(st string) *set {
	s := &set{stamp: st, errs: map[string]string{}}
	seen := map[string]string{}
	for _, f := range modules() {
		a, err := Compile(f.e.Spec, f.file)
		if err == nil && seen[a.Desc.ID] != "" {
			err = fmt.Errorf("agent id %q is %s's too", a.Desc.ID, plugin.Name(seen[a.Desc.ID]))
		}
		if err != nil {
			s.errs[f.e.Spec] = err.Error()
			log.Printf("agent plugin %s didn't load: %v", plugin.Name(f.e.Spec), err)
			continue
		}
		seen[a.Desc.ID] = f.e.Spec
		s.list = append(s.list, a)
	}
	return s
}

// Compile compiles the agent module in file, of the plugin spec, and
// checks what its agent export says.
func Compile(spec, file string) (*Agent, error) {
	root := filepath.Dir(file)
	if t := plugin.Target(spec); t != file {
		root = t
	}
	mod, err := jsmod.Compile(root, file, "an agent plugin")
	if err != nil {
		return nil, err
	}
	a := &Agent{Spec: spec, File: file, rt: moejs.NewRuntime(moejs.Options{})}
	if err := a.rt.SetGlobal("console", console(a)); err != nil {
		return nil, err
	}
	if err := a.limit(loadLimit, func() error { return a.rt.Load(mod) }); err != nil {
		return nil, err
	}
	v, ok := a.rt.Export("agent")
	if !ok || !v.IsObject() {
		if d, ok := a.rt.Export("default"); ok && d.IsObject() {
			v, _ = a.rt.Get(d, "agent")
		}
	}
	if !v.IsObject() {
		return nil, errors.New("exports no agent object")
	}
	if err := a.rt.Unmarshal(v, &a.Desc); err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if err := a.Desc.check(); err != nil {
		return nil, err
	}
	for _, h := range [][]string{{"connect"}, {"default", "connect"}} {
		if hk, err := mod.Hook(h[0], h[1:]...); err == nil {
			if ok, _ := a.rt.Has(hk); ok {
				a.connect, a.has = hk, true
				break
			}
		}
	}
	for _, h := range [][]string{{"own"}, {"default", "own"}} {
		if hk, err := mod.Hook(h[0], h[1:]...); err == nil {
			if ok, _ := a.rt.Has(hk); ok {
				a.own, a.hasOwn = hk, true
				break
			}
		}
	}
	// connect only: with no model key, connect's keys are all magpie
	// writes, so a module with neither would write nothing
	if a.Desc.Startup != nil && !a.has {
		return nil, errors.New("agent.startup names the model, but there is no connect to write the gateway into agent.config")
	}
	if a.Desc.Model == "" && !a.has {
		return nil, errors.New("agent.model names no key, and there is no connect to write the gateway: say one or both")
	}
	return a, nil
}

func (d *Desc) check() error {
	d.ID = strings.TrimSpace(d.ID)
	switch {
	case !idOK.MatchString(d.ID):
		return fmt.Errorf("agent.id %q: lower-case letters, digits, '.', '_' and '-' only", d.ID)
	case strings.TrimSpace(d.Config) == "":
		return errors.New("agent.config names no file")
	}
	d.Model = strings.TrimSpace(d.Model)
	if d.Name == "" {
		d.Name = d.ID
	}
	if d.Format == "" {
		d.Format = FormatOf(d.Config)
	}
	switch d.Format {
	case "json", "yaml", "env":
	case "":
		return fmt.Errorf("agent.config %q: say its agent.format (json, yaml or env)", d.Config)
	default:
		return fmt.Errorf("agent.format %q: json, yaml or env", d.Format)
	}
	if st := d.Startup; st != nil {
		st.Model = strings.TrimSpace(st.Model)
		switch {
		case d.Model != "":
			return errors.New("agent.model and agent.startup.model both name the model: say one")
		case strings.TrimSpace(st.Config) == "":
			return errors.New("agent.startup.config names no file")
		case st.Model == "":
			return errors.New("agent.startup.model names no key")
		case strings.TrimSpace(st.Config) == strings.TrimSpace(d.Config):
			return errors.New("agent.startup.config is agent.config: name the model in it with agent.model")
		}
		if _, ok := st.Keys[st.Model]; ok {
			return fmt.Errorf("agent.startup.keys has %q, the model's own key", st.Model)
		}
		if st.Format == "" {
			st.Format = FormatOf(st.Config)
		}
		switch st.Format {
		case "json", "yaml", "env":
		case "":
			return fmt.Errorf("agent.startup.config %q: say its agent.startup.format (json, yaml or env)", st.Config)
		default:
			return fmt.Errorf("agent.startup.format %q: json, yaml or env", st.Format)
		}
	}
	l := &d.Library
	l.Instructions, l.MCP, l.Skills = strings.TrimSpace(l.Instructions), strings.TrimSpace(l.MCP), strings.TrimSpace(l.Skills)
	l.MCPFormat = strings.ToLower(strings.TrimSpace(l.MCPFormat))
	switch {
	case l.MCP == "" && l.MCPFormat != "":
		return errors.New("agent.library.mcpFormat names a format, but agent.library.mcp no file")
	case l.MCP != "" && l.MCPFormat == "":
		return fmt.Errorf("agent.library.mcp %q: say its agent.library.mcpFormat, one of %s", l.MCP, strings.Join(MCPFormats, ", "))
	case l.MCP != "" && !slices.Contains(MCPFormats, l.MCPFormat):
		return fmt.Errorf("agent.library.mcpFormat %q: one of %s", l.MCPFormat, strings.Join(MCPFormats, ", "))
	}
	for i, u := range d.UA {
		d.UA[i] = strings.ToLower(u)
	}
	if l := d.Launch; l != nil {
		l.Model = strings.TrimSpace(l.Model)
		switch {
		case d.Model == "":
			return errors.New("agent.launch starts the agent on a model: name its key with agent.model")
		case !binOK.MatchString(d.Bin):
			return fmt.Errorf("agent.launch needs agent.bin, the command it starts, as a shell names a function (letters, digits, '_' and '-'), not %q", d.Bin)
		case len(l.Env) == 0 && l.Model == "":
			return errors.New("agent.launch gives neither env nor a model flag")
		case l.Model != "" && !flagOK.MatchString(l.Model):
			return fmt.Errorf("agent.launch.model %q: a flag, as --model", l.Model)
		}
		for f, v := range l.Args {
			if !flagOK.MatchString(f) || f == l.Model {
				return fmt.Errorf("agent.launch.args %q: a flag, as --settings, not the model's", f)
			}
			if v != "{config}" {
				return fmt.Errorf("agent.launch.args %s %q: {config}, agent.config's path", f, v)
			}
		}
		for i, a := range l.Plain {
			a = strings.TrimSpace(a)
			if !plainOK.MatchString(a) {
				return fmt.Errorf("agent.launch.plain %q: a subcommand or a flag", a)
			}
			l.Plain[i] = a
		}
		for k := range l.Env {
			if !envOK.MatchString(k) {
				return fmt.Errorf("agent.launch.env %q: a variable's name", k)
			}
			if !envValueOK.MatchString(l.Env[k]) {
				return fmt.Errorf("agent.launch.env %s %q: {key}, {url} or {v1}, a path after the address at most", k, l.Env[k])
			}
		}
	}
	if d.Base = strings.TrimSpace(d.Base); d.Base != "" {
		switch {
		case d.Launch == nil || !slices.Contains(slices.Collect(maps.Values(d.Launch.Args)), "{config}"):
			return errors.New("agent.base is for an agent.config that agent.launch.args hands the agent, as magpie's own file")
		case d.Base == strings.TrimSpace(d.Config):
			return errors.New("agent.base is agent.config")
		case FormatOf(d.Base) != "" && FormatOf(d.Base) != d.Format:
			return fmt.Errorf("agent.base %q: a file of agent.format, %s", d.Base, d.Format)
		}
	}
	for i, m := range d.Merge {
		m = strings.TrimSpace(m)
		switch {
		case d.Format != "json":
			return errors.New("agent.merge is for a json agent.config")
		case m == "":
			return errors.New("agent.merge names an empty key")
		case m == d.Model:
			return fmt.Errorf("agent.merge %q is the model's own key", m)
		}
		d.Merge[i] = m
	}
	for i, al := range d.Aliases {
		al = strings.TrimSpace(al)
		if !idOK.MatchString(al) || al == d.ID {
			return fmt.Errorf("agent.aliases %q: lower-case letters, digits, '.', '_' and '-', not its id", al)
		}
		d.Aliases[i] = al
	}
	return nil
}

// HasOwn says the module exports own.
func (a *Agent) HasOwn() bool { return a.hasOwn }

// OwnModels is what the module's own gives for config, the agent's file
// as parsed JSON, base, its own settings file so (agent.base; nil for
// none), and model, the one it is on now; none for a module without it.
func (a *Agent) OwnModels(config, base any, model string) ([]Own, error) {
	if !a.hasOwn {
		return nil, nil
	}
	in := map[string]any{"config": config, "model": model}
	if base != nil {
		in["base"] = base
	}
	b, err := a.call(a.own, "own", in)
	if err != nil || b == nil {
		return nil, err
	}
	var out []Own
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, errors.New("own returned no list of models")
	}
	return slices.DeleteFunc(out, func(o Own) bool { return o.Value == "" }), nil
}

// Connect is what the module's connect returns for in: the keys to write
// beside the model, by key path. A module without connect writes none.
func (a *Agent) Connect(in map[string]any) (map[string]any, error) {
	if !a.has {
		return nil, nil
	}
	b, err := a.call(a.connect, "connect", in)
	if err != nil || b == nil {
		return nil, err
	}
	return keys(b)
}

// call calls the module's hook named name with in, and gives what it
// returns as JSON text: nil for nothing, an error for what isn't an
// object or a list.
func (a *Agent) call(hook moejs.Hook, name string, in map[string]any) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.rt.ReleaseCallData()
	arg, err := a.rt.FromGo(in)
	if err != nil {
		return nil, err
	}
	var res moejs.Value
	err = a.limit(connectLimit, func() error {
		var err error
		res, err = a.rt.Call(hook, arg)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %s", name, errText(err))
	}
	if st, v, ok := moejs.PromiseResult(res); ok {
		switch st {
		case moejs.PromiseFulfilled:
			res = v
		case moejs.PromiseRejected:
			return nil, fmt.Errorf("%s rejected: %s", name, text(a.rt, v))
		default:
			return nil, fmt.Errorf("%s returned a promise that never settled", name)
		}
	}
	if res.IsUndefined() || res.IsNull() {
		return nil, nil
	}
	if !res.IsObject() {
		return nil, fmt.Errorf("%s returned neither an object nor nothing", name)
	}
	return a.rt.AppendJSON(nil, res)
}

// keys is connect's result, as JSON text, by key path. An object or array
// stays its JSON text (a json.RawMessage), so its members are written in
// the order the module gave them: a Go map would write them sorted, and an
// agent shows Kilo's effort variants low, medium, high as high, low, medium
// (#1890). A string, number, boolean or null is its Go value.
func keys(b []byte) (map[string]any, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil || top == nil {
		return nil, errors.New("connect returned no object of keys")
	}
	out := make(map[string]any, len(top))
	for k, raw := range top {
		switch raw[0] {
		case '{', '[':
			out[k] = raw
			continue
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			return nil, fmt.Errorf("connect's %q: %w", k, err)
		}
		if n, ok := v.(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				v = i
			} else {
				v, _ = n.Float64()
			}
		}
		out[k] = v
	}
	return out, nil
}

// afterFunc is time.AfterFunc; a test holds the func it runs.
var afterFunc = time.AfterFunc

// limit runs fn, stopping the module once d has passed. No interrupt
// outlives it: moejs keeps one that lands while nothing runs for the next
// call, whose own limit never fired, so every connect after would fail
// (#3727). Stop doesn't wait for a timer's func that has begun, so limit
// does, then clears what it left. A call that finished as its time ran
// out keeps what it returned.
func (a *Agent) limit(d time.Duration, fn func() error) error {
	interrupted := make(chan struct{})
	t := afterFunc(d, func() {
		defer close(interrupted) // after the interrupt: limit clears it once this is closed
		a.rt.Interrupt(errTimeout)
	})
	err := fn()
	if t.Stop() {
		return err
	}
	<-interrupted
	a.rt.ClearInterrupt()
	if err != nil {
		return errTimeout
	}
	return nil
}

func errText(err error) string {
	var exc *moejs.Exception
	if errors.As(err, &exc) {
		return exc.Name() + ": " + exc.Message()
	}
	return err.Error()
}

func text(rt *moejs.Runtime, v moejs.Value) string {
	if v.IsString() {
		if s, err := rt.Realm().ToString(v); err == nil {
			return s.GoString()
		}
	}
	if b, err := rt.AppendJSON(nil, v); err == nil && len(b) > 0 {
		return string(b)
	}
	return "?"
}

func console(a *Agent) map[string]any {
	say := func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		parts := make([]string, len(args))
		for i, v := range args {
			parts[i] = text(a.rt, v)
		}
		log.Printf("agent plugin %s: %s", plugin.Name(a.Spec), strings.Join(parts, " "))
		return moejs.Undefined(), nil
	}
	return map[string]any{"log": moejs.NativeFunc(say), "warn": moejs.NativeFunc(say), "error": moejs.NativeFunc(say), "info": moejs.NativeFunc(say)}
}
