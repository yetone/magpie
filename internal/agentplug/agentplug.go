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
package agentplug

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/yetone/magpie/internal/jsmod"
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
}

// How long the module may take to load, and connect to answer.
var (
	loadLimit    = time.Second
	connectLimit = time.Second
)

var errTimeout = errors.New("took too long")

var idOK = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

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
	return nil
}

// Connect is what the module's connect returns for in: the keys to write
// beside the model, by key path. A module without connect writes none.
func (a *Agent) Connect(in map[string]any) (map[string]any, error) {
	if !a.has {
		return nil, nil
	}
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
		res, err = a.rt.Call(a.connect, arg)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("connect: %s", errText(err))
	}
	if st, v, ok := moejs.PromiseResult(res); ok {
		switch st {
		case moejs.PromiseFulfilled:
			res = v
		case moejs.PromiseRejected:
			return nil, fmt.Errorf("connect rejected: %s", text(a.rt, v))
		default:
			return nil, errors.New("connect returned a promise that never settled")
		}
	}
	if res.IsUndefined() || res.IsNull() {
		return nil, nil
	}
	if !res.IsObject() {
		return nil, errors.New("connect returned neither an object nor nothing")
	}
	b, err := a.rt.AppendJSON(nil, res)
	if err != nil {
		return nil, err
	}
	return keys(b)
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
