package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"
)

// What the agents' own plugins and packages declare (jinpwng, #7244). A
// plugin installed in Codex (its marketplaces) or a package installed in
// Pi (`pi install`) is the agent's to load. magpie only reads what it
// declares, to list the skills, MCP servers and hooks in the Library's own
// tabs once, with where it came from and its scope, and writes, links and
// registers nothing: the agent keeps loading it, so nothing is listed a
// second time as a copy of the library's. Whether a session loads it is
// not known from its files, so it is listed as unknown (Loaded) until one
// shows it.
//
// One MCP server of theirs can be copied into the library
// (CopyNativeServer, 0xRichardH on X), to give to other agents. A copy is
// the server as the plugin declares it, with the plugin's folder written
// in where the agent would put it, and no agent ticked. One that works
// only inside its agent is not copied, and says why (NoCopy): it runs from
// inside the agent's app install, or comes from the marketplace the
// agent's app writes out of its own bundle (Codex's openai-bundled), or
// reads values the agent fills in when it starts it (CLAUDE_PLUGIN_DATA,
// user_config). Codex's codex_app is the second kind: every tools/list
// goes through the pipe the Codex app hands it in
// CODEX_APP_TOOLS_PIPE_PATH, and fails without it (#2308). cua_repl is
// both: a node_repl inside ChatGPT.app that needs the app's browser access
// pipe and its signed replies for each Codex session.
//
// Codex (codex-rs/core-plugins loader.rs), under CODEX_HOME:
//
//	plugins/cache/<marketplace>/<plugin>/<version>/.codex-plugin/plugin.json
//	    (.claude-plugin/plugin.json and .cursor-plugin/plugin.json too)
//	config.toml   [plugins."<plugin>@<marketplace>"] enabled, [marketplaces.<m>] source
//
// skills are the plugin.json "skills" folders, else skills/; MCP servers
// its "mcpServers" (a file, or the servers inline), else .mcp.json; hooks
// its "hooks" (files, or inline), else hooks/hooks.json. Codex has no
// subagents in a plugin.
//
// Pi (settings-manager and package-manager.js, @earendil-works/pi-coding-agent
// 1.4): each entry of settings.json "packages" (the Pi's own, and a
// registered project's .pi/settings.json) whose folder is on disk. A
// package declares skills in package.json pi.skills, else skills/ when it
// has no "pi" object; Pi has no MCP servers, hooks or subagents of a
// package's. A project's packages load only once Pi trusts the project, so
// they are listed with Enabled unknown.
//
// Claude Code (#7266, jinpwng), under its config folder:
//
//	plugins/installed_plugins.json  {"version": 2, "plugins": {"<plugin>@<marketplace>":
//	    [{"scope": "user"|"project"|"local", "installPath", "version", "projectPath"}]}}
//	settings.json                   enabledPlugins (user scope)
//	<project>/.claude/settings.json, settings.local.json   enabledPlugins (project, local)
//
// One record for each scope an id is installed in. The parts are read from
// installPath as Claude Code reads a plugin: what plugin.json names is
// added to the default folders (skills/, agents/, hooks/hooks.json,
// .mcp.json), not read instead of them. Subagents are agents/*.md.

// NativePart is one thing a native plugin or package declares.
type NativePart struct {
	// Kind is skill, mcp, hook or subagent
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Detail is a skill's description, a server's transport, a hook's
	// command
	Detail string `json:"detail,omitempty"`
	// NoCopy is, for an MCP server, why it isn't copied into the library:
	// "app" when it works only inside the agent's app, "agent" when it
	// reads values the agent fills in when it starts it, "entry" when its
	// entry isn't one the library can hold. "" for one that is copied.
	NoCopy string `json:"noCopy,omitempty"`

	dir string // a skill's folder, what a Pi package's filters match
	// server is an MCP server as the library would hold it, for a copy
	server *Server
}

// nativeFrom is the plugin a part comes from, as its MCP servers need it:
// the agent, the plugin's folder, and whether the agent's app writes the
// plugin out of its own bundle.
type nativeFrom struct {
	agent, agentName string
	root             string
	bundled          bool
}

// NativePlugin is a plugin or package an agent installed itself.
type NativePlugin struct {
	Agent     string `json:"agent"`
	AgentName string `json:"agentName"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	// Source is the marketplace it came from (Codex), or the entry in
	// settings.json (Pi)
	Source string `json:"source,omitempty"`
	// Scope is user, or project for one a project's own settings name
	Scope   string `json:"scope"`
	Project string `json:"project,omitempty"`
	Dir     string `json:"dir"`
	// Enabled is what the agent's own settings say; nil when they don't
	Enabled *bool `json:"enabled,omitempty"`
	// Loaded is always "unknown": only a session shows what was loaded
	Loaded string       `json:"loaded"`
	Parts  []NativePart `json:"parts"`
	// InProjects is, for a Codex plugin, what each of the library's
	// projects makes of it (#7244)
	InProjects []NativeInProject `json:"inProjects,omitempty"`
}

// NativeInProject is a user-installed Codex plugin as one project has it:
// the project's .codex/config.toml may turn it on or off, which Codex
// applies once the project is trusted. Effective is what that comes to by
// the files (nil when neither says); whether a session loaded it stays
// unknown.
type NativeInProject struct {
	Dir string `json:"dir"`
	// Set is what the project's own config says; nil when it says nothing
	Set *bool `json:"set,omitempty"`
	// Trusted is the user config's trust_level for the project
	Trusted   bool  `json:"trusted"`
	Effective *bool `json:"effective,omitempty"`
}

const nativePartsMax = 500

// ReadNative lists what the agents' own plugins and packages declare. It
// writes nothing and starts nothing.
func ReadNative() []NativePlugin {
	out := codexNative()
	out = append(out, claudeNative()...)
	out = append(out, piNativePlugins()...)
	if out == nil {
		out = []NativePlugin{}
	}
	return out
}

// CopyNativeServer puts a copy of an MCP server an agent's plugin declares
// (the plugin in dir) in the library, given to no agent yet: the user
// gives it to the agents they want. The plugin stays the agent's.
func CopyNativeServer(agentID, dir, name string) (*Result, error) {
	// read before change: ReadNative takes the library's lock itself
	var part *NativePart
	var of string
	for _, p := range ReadNative() {
		if p.Agent != agentID || p.Dir != dir {
			continue
		}
		for i := range p.Parts {
			if x := p.Parts[i]; x.Kind == "mcp" && x.Name == name {
				part, of = &p.Parts[i], p.AgentName
				break
			}
		}
		if part != nil {
			break
		}
	}
	if part == nil {
		return nil, fmt.Errorf("no plugin of %s in %s has a server called %s", agentID, dir, name)
	}
	if part.NoCopy != "" || part.server == nil {
		return nil, errors.New(NoCopyWhy(part.NoCopy, of))
	}
	s := part.server
	return changeMCP(func(l *Library) error {
		if l.server(s.Name) != nil {
			return fmt.Errorf("the library already has a server called %s", s.Name)
		}
		l.MCP = append(l.MCP, s)
		return nil
	})
}

// NoCopyWhy is NativePart.NoCopy said in words, of the agent named agent.
func NoCopyWhy(why, agent string) string {
	switch why {
	case "app":
		return fmt.Sprintf("It works only inside %s's app: the app starts it with a connection of its own.", agent)
	case "agent":
		return fmt.Sprintf("It reads values %s fills in when it starts it.", agent)
	}
	return "The library can't hold its entry."
}

// ---- Codex ------------------------------------------------------------------

type codexConf struct {
	Plugins      map[string]struct{ Enabled *bool } `toml:"plugins"`
	Marketplaces map[string]struct{ Source string } `toml:"marketplaces"`
	Projects     map[string]struct {
		TrustLevel string `toml:"trust_level"`
	} `toml:"projects"`
}

var codexManifests = []string{".codex-plugin", ".claude-plugin", ".cursor-plugin"}

func codexNative() []NativePlugin {
	home := codexDir()
	var conf codexConf
	if raw, err := os.ReadFile(filepath.Join(home, "config.toml")); err == nil {
		// one that doesn't parse says nothing of enabled or sources
		if toml.Unmarshal(raw, &conf) != nil {
			conf = codexConf{}
		}
	}
	cache := filepath.Join(home, "plugins", "cache")
	var out []NativePlugin
	markets, _ := os.ReadDir(cache)
	var projects []codexProject
	if len(markets) > 0 {
		projects = codexProjects(conf.Projects)
	}
	for _, m := range markets {
		if !m.IsDir() || strings.HasPrefix(m.Name(), ".") {
			continue
		}
		plugins, _ := os.ReadDir(filepath.Join(cache, m.Name()))
		for _, pl := range plugins {
			if !pl.IsDir() || strings.HasPrefix(pl.Name(), ".") {
				continue
			}
			root, man := codexVersion(filepath.Join(cache, m.Name(), pl.Name()))
			if root == "" {
				continue
			}
			p := NativePlugin{Agent: "codex", AgentName: "Codex", Name: plStr(man["name"]), Version: plStr(man["version"]),
				Scope: "user", Dir: root, Loaded: "unknown", Source: m.Name()}
			if p.Name == "" {
				p.Name = pl.Name()
			}
			if p.Version == "" {
				p.Version = filepath.Base(root)
			}
			src := conf.Marketplaces[m.Name()].Source
			if src != "" {
				p.Source = m.Name() + " (" + src + ")"
			}
			id := pl.Name() + "@" + m.Name()
			p.Enabled = conf.Plugins[id].Enabled
			from := nativeFrom{agent: "codex", agentName: "Codex", root: root, bundled: codexBundled(m.Name(), src)}
			p.Parts = manifestParts(from, man, false)
			for _, pr := range projects {
				// a project that says nothing of it has the user's
				if set := pr.plugins[id]; set != nil {
					in := NativeInProject{Dir: pr.dir, Set: set, Trusted: pr.trusted, Effective: p.Enabled}
					if pr.trusted {
						in.Effective = set
					}
					p.InProjects = append(p.InProjects, in)
				}
			}
			out = append(out, p)
		}
	}
	return out
}

// codexProject is what a project's .codex/config.toml files say of
// plugins, and whether Codex trusts the project.
type codexProject struct {
	dir     string
	trusted bool
	plugins map[string]*bool
}

// codexProjects reads each of the library's projects as Codex does
// (codex-rs config loader, 0.171): the .codex/config.toml of each folder
// from the repository's top down to the project, a nearer one over a
// farther, used only when the project is trusted, which is the user
// config's projects."<dir>".trust_level: the folder's own, else its
// repository's, else the main checkout's for a linked worktree.
func codexProjects(trust map[string]struct {
	TrustLevel string `toml:"trust_level"`
}) []codexProject {
	var out []codexProject
	for _, dir := range nativeProjects() {
		pr := codexProject{dir: dir, plugins: map[string]*bool{}}
		chain := []string{dir}
		g, inGit := gitOf(dir)
		if inGit {
			for d := dir; d != g.Top; {
				up := filepath.Dir(d)
				if up == d {
					break
				}
				d = up
				chain = append(chain, d)
			}
		}
		for i := len(chain) - 1; i >= 0; i-- {
			raw, err := os.ReadFile(filepath.Join(chain[i], ".codex", "config.toml"))
			if err != nil {
				continue
			}
			var c codexConf
			if toml.Unmarshal(raw, &c) != nil {
				continue // one that doesn't parse says nothing
			}
			for id, v := range c.Plugins {
				if v.Enabled != nil {
					pr.plugins[id] = v.Enabled
				}
			}
		}
		keys := []string{dir}
		if inGit {
			keys = append(keys, g.Top)
			if m := g.mainTop(); m != "" {
				keys = append(keys, m)
			}
		}
	look:
		for _, k := range keys {
			for _, kk := range pathSpellings(k) {
				if lvl := codexTrust(trust, kk); lvl != "" {
					pr.trusted = lvl == "trusted"
					break look
				}
			}
		}
		out = append(out, pr)
	}
	return out
}

// codexFoldsKeys is whether Codex folds the case of a project's key: on
// Windows only (cfg!(windows) in the config loader).
var codexFoldsKeys = runtime.GOOS == "windows"

// codexTrust is the trust_level the user config gives the folder key, as
// Codex looks it up (codex-rs config loader 0.172,
// project_trust_for_lookup_key): the key itself; on Windows, where Codex
// lower-cases the key it looks up and writes (c:\users\…), the key
// in lower case (ASCII), else the least of those that differ from it in
// ASCII case only (#7786). "" when none does.
func codexTrust(trust map[string]struct {
	TrustLevel string `toml:"trust_level"`
}, key string) string {
	if !codexFoldsKeys {
		return trust[key].TrustLevel
	}
	key = asciiLower(key)
	if t := trust[key].TrustLevel; t != "" {
		return t
	}
	best, lvl := "", ""
	for k, t := range trust {
		if t.TrustLevel != "" && asciiLower(k) == key && (lvl == "" || k < best) {
			best, lvl = k, t.TrustLevel
		}
	}
	return lvl
}

// asciiLower is s with A to Z in lower case, as Rust's to_ascii_lowercase.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// pathSpellings are dir as written and with its links read (/var and
// /private/var on macOS), the ways a config may key it.
func pathSpellings(dir string) []string {
	out := []string{filepath.Clean(dir)}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r != out[0] {
		out = append(out, r)
	}
	return out
}

// codexVersion is the version folder of a cached plugin that is current:
// the newest one with a manifest (a "latest" beside the version it copies
// is the same plugin once), and that manifest.
func codexVersion(dir string) (string, map[string]any) {
	vs, _ := os.ReadDir(dir)
	var best string
	var bestMan map[string]any
	var bestTime int64
	for _, v := range vs {
		d := filepath.Join(dir, v.Name())
		if !isDir(d) {
			continue
		}
		var man map[string]any
		for _, mf := range codexManifests {
			if m, err := readJSONFile(filepath.Join(d, mf, "plugin.json")); err == nil {
				man = m
				break
			}
		}
		if man == nil {
			continue
		}
		fi, err := os.Stat(d)
		if err != nil {
			continue
		}
		// "latest" is a copy of a version beside it: that one stands for
		// the plugin, "latest" only when it is alone
		t := fi.ModTime().UnixNano()
		if v.Name() == "latest" {
			t = 0
		}
		if best == "" || t > bestTime {
			best, bestMan, bestTime = d, man, t
		}
	}
	return best, bestMan
}

// codexBundled is whether a Codex marketplace is the one the Codex app
// writes out of its own bundle (its config.toml source is
// CODEX_HOME/.tmp/bundled-marketplaces/<name>): its plugins are made for
// the app, which starts their servers with what only it has.
func codexBundled(market, source string) bool {
	if market == "openai-bundled" {
		return true
	}
	return strings.Contains(strings.ToLower(filepath.ToSlash(source)), "/.tmp/bundled-marketplaces/")
}

// manifestParts are the parts a plugin's manifest declares. Codex reads
// the default files only when the manifest names none; Claude Code reads
// them too (also).
func manifestParts(from nativeFrom, man map[string]any, also bool) []NativePart {
	root := from.root
	var parts []NativePart
	// skills
	roots := pathsOf(man["skills"])
	if len(roots) == 0 || also {
		roots = append([]string{"skills"}, roots...)
	}
	for _, r := range roots {
		if d, ok := inside(root, r); ok && isDir(d) {
			parts = append(parts, nativeSkills(d, 0)...)
		}
	}
	// MCP servers: a file or the servers inline, else .mcp.json
	var docs []map[string]any
	named := true
	switch v := man["mcpServers"].(type) {
	case string:
		docs = nativeJSONFiles(root, []string{v})
	case map[string]any:
		docs = []map[string]any{v}
	default:
		named = false
	}
	if !named || also {
		docs = append(docs, nativeJSONFiles(root, []string{".mcp.json"})...)
	}
	for _, d := range docs {
		parts = append(parts, nativeServers(d, from)...)
	}
	// hooks: files or inline objects, else hooks/hooks.json
	named = true
	switch v := man["hooks"].(type) {
	case string:
		docs = nativeJSONFiles(root, []string{v})
	case map[string]any:
		docs = []map[string]any{v}
	case []any:
		docs = nil
		var files []string
		for _, e := range v {
			switch x := e.(type) {
			case string:
				files = append(files, x)
			case map[string]any:
				docs = append(docs, x)
			}
		}
		docs = append(docs, nativeJSONFiles(root, files)...)
	default:
		docs, named = nil, false
	}
	if !named || also {
		docs = append(docs, nativeJSONFiles(root, []string{"hooks/hooks.json"})...)
	}
	for _, d := range docs {
		parts = append(parts, nativeHooks(d)...)
	}
	if also {
		parts = append(parts, nativeSubagents(root, man)...)
	}
	return nativeUnique(parts)
}

// nativeSubagents are a Claude Code plugin's agents/*.md and the files or
// folders its manifest's "agents" names: each Markdown file with a
// description in its front matter is one.
func nativeSubagents(root string, man map[string]any) []NativePart {
	var files []string
	for _, rel := range append([]string{"agents"}, pathsOf(man["agents"])...) {
		d, ok := inside(root, rel)
		if !ok {
			continue
		}
		if fi, err := os.Stat(d); err == nil && !fi.IsDir() {
			files = append(files, d)
		} else if es, err := os.ReadDir(d); err == nil {
			for _, e := range es {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
					files = append(files, filepath.Join(d, e.Name()))
				}
			}
		}
	}
	slices.Sort(files)
	var out []NativePart
	for _, f := range slices.Compact(files) {
		s, _ := readSubagentAt(f)
		if s == nil {
			continue
		}
		name := s.Name
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		}
		out = append(out, NativePart{Kind: "subagent", Name: name, Detail: s.Description})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---- Claude Code ------------------------------------------------------------

func claudeNative() []NativePlugin {
	raw, err := readClaudeJSON(claudeInstalled())
	if err != nil || raw == nil {
		return nil
	}
	var doc struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
			Version     string `json:"version"`
			ProjectPath string `json:"projectPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	settings := map[string][]byte{}
	enabled := func(file, id string) *bool {
		b, ok := settings[file]
		if !ok {
			b, _ = readClaudeJSON(file)
			settings[file] = b
		}
		v := gjson.GetBytes(b, "enabledPlugins."+gjson.Escape(id))
		if !v.IsBool() {
			return nil
		}
		on := v.Bool()
		return &on
	}
	var out []NativePlugin
	for _, id := range slices.Sorted(maps.Keys(doc.Plugins)) {
		name, market, ok := strings.Cut(id, "@")
		if !ok || name == "" {
			continue
		}
		for _, e := range doc.Plugins[id] {
			if e.InstallPath == "" || !isDir(e.InstallPath) {
				continue
			}
			man, _ := readJSONFile(filepath.Join(e.InstallPath, ".claude-plugin", "plugin.json"))
			p := NativePlugin{Agent: "claude", AgentName: "Claude Code", Name: id, Version: e.Version,
				Source: market, Scope: "user", Dir: e.InstallPath, Loaded: "unknown"}
			if p.Version == "" {
				p.Version = plStr(man["version"])
			}
			switch e.Scope {
			case "user", "":
				p.Enabled = enabled(claudeSettings(), id)
			case "project", "local":
				if e.ProjectPath == "" {
					continue
				}
				p.Scope, p.Project = "project", e.ProjectPath
				file := "settings.json"
				if e.Scope == "local" {
					file = "settings.local.json"
				}
				p.Enabled = enabled(filepath.Join(e.ProjectPath, ".claude", file), id)
			default:
				// managed, or a scope this magpie doesn't know: not listed
				// as one the user or a project chose
				continue
			}
			p.Parts = manifestParts(nativeFrom{agent: "claude", agentName: "Claude Code", root: e.InstallPath}, man, true)
			out = append(out, p)
		}
	}
	return out
}

// nativeJSONFiles reads the files a manifest names inside the plugin's
// folder: one that leaves it, or can't be read as an object, is skipped.
func nativeJSONFiles(root string, names []string) []map[string]any {
	var out []map[string]any
	for _, n := range names {
		p, ok := inside(root, n)
		if !ok {
			continue
		}
		if m, err := readJSONFile(p); err == nil {
			out = append(out, m)
		}
	}
	return out
}

func nativeServers(doc map[string]any, from nativeFrom) []NativePart {
	m, _ := doc["mcpServers"].(map[string]any)
	if m == nil {
		m = doc
	}
	var out []NativePart
	for _, k := range slices.Sorted(mapKeys(m)) {
		e, ok := m[k].(map[string]any)
		if !ok {
			continue
		}
		tr := "stdio"
		switch typ := plStr(e["type"]); {
		case typ == "sse":
			tr = "sse"
		case typ == "http" || typ == "streamable-http" || typ == "" && plStr(e["url"]) != "":
			tr = "http"
		}
		p := NativePart{Kind: "mcp", Name: k, Detail: tr}
		p.server, p.NoCopy = nativeServer(k, e, from)
		out = append(out, p)
	}
	return out
}

// nativeServer is a plugin's MCP server as the library would hold it, or
// why it can't be (NativePart.NoCopy). The entry is read as the agent's
// own are (decode): a variable Codex's env_vars passes on becomes a
// reference to it. Claude Code's ${CLAUDE_PLUGIN_ROOT} is the plugin's
// folder, and the server is given it in its env, as Claude Code gives
// it. A relative cwd is from the plugin's folder; the library has no cwd,
// so the command and the arguments that are paths are made paths from it.
func nativeServer(name string, e map[string]any, from nativeFrom) (*Server, string) {
	f := &mcpFile{Format: fmtClaude}
	if from.agent == "codex" {
		f.Format = fmtCodex
	}
	slug := pluginSlug(name)
	if slug == "" {
		return nil, "entry"
	}
	s, ok := f.decode(slug, e)
	if !ok {
		return nil, "entry"
	}
	s.Agents = []string{}
	cwd := plStr(e["cwd"])
	if from.agent == "claude" {
		sub := strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", from.root, "$CLAUDE_PLUGIN_ROOT", from.root).Replace
		s.Command, s.URL, cwd = sub(s.Command), sub(s.URL), sub(cwd)
		for i := range s.Args {
			s.Args[i] = sub(s.Args[i])
		}
		for _, m := range []map[string]string{s.Env, s.Headers} {
			for k, v := range m {
				m[k] = sub(v)
			}
		}
		if all := serverText(s, cwd); strings.Contains(all, "CLAUDE_PLUGIN_DATA") || strings.Contains(all, "user_config.") {
			return nil, "agent"
		}
	}
	if cwd != "" && !filepath.IsAbs(cwd) {
		cwd = filepath.Join(from.root, filepath.FromSlash(cwd))
	}
	if cwd != "" && !s.Remote() {
		s.Command = fromDir(cwd, s.Command, true)
		for i, a := range s.Args {
			s.Args[i] = fromDir(cwd, a, false)
		}
	}
	if from.bundled {
		return nil, "app"
	}
	// a program, a folder or a file of the agent's app, in any place the
	// server is started with
	if !s.Remote() {
		for _, p := range append(append([]string{s.Command, cwd}, s.Args...), slices.Collect(maps.Values(s.Env))...) {
			if inAgentApp(p, from.agent, from.agentName) {
				return nil, "app"
			}
		}
	}
	if from.agent == "codex" {
		s.StartupTimeout, s.CallTimeout = codexSeconds(e["startup_timeout_sec"]), codexSeconds(e["tool_timeout_sec"])
	}
	if from.agent == "claude" && !s.Remote() {
		if _, set := s.Env["CLAUDE_PLUGIN_ROOT"]; !set {
			if s.Env == nil {
				s.Env = map[string]string{}
			}
			s.Env["CLAUDE_PLUGIN_ROOT"] = from.root
		}
	}
	if s.check() != nil {
		return nil, "entry"
	}
	// a server of the plugin's own files is this computer's
	s.Local = from.root != "" && strings.Contains(serverText(s, cwd), from.root)
	return s, ""
}

// serverText is every value a server is started or reached with.
func serverText(s *Server, cwd string) string {
	v := []string{s.Command, s.URL, cwd}
	v = append(v, s.Args...)
	for _, m := range []map[string]string{s.Env, s.Headers} {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			v = append(v, m[k])
		}
	}
	return strings.Join(v, "\n")
}

// fromDir is p, a command or an argument of a server started in dir, as a
// path from anywhere: one written ./x or ../x, a command with a folder in
// it, or an argument naming a file in dir. Anything else (a program on the
// PATH, a flag, a word) is as written.
func fromDir(dir, p string, command bool) string {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "-") || strings.Contains(p, "$") {
		return p
	}
	slash := filepath.ToSlash(p)
	at := filepath.Join(dir, filepath.FromSlash(p))
	if strings.HasPrefix(slash, "./") || strings.HasPrefix(slash, "../") || command && strings.Contains(slash, "/") {
		return at
	}
	if fi, err := os.Stat(at); !command && err == nil && !fi.IsDir() {
		return at
	}
	return p
}

// codexSeconds is a Codex entry's timeout in whole seconds, rounded up; 0
// for none, or one past what the library holds.
func codexSeconds(v any) int {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int64:
		f = float64(x)
	}
	if f <= 0 || f > maxStartupTimeout {
		return 0
	}
	return int(math.Ceil(f))
}

func mapKeys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// nativeHooks lists a hooks file's handlers, one for each event and
// matcher: {"hooks": {"Stop": [{"matcher", "hooks": [{"type", "command"}]}]}}.
func nativeHooks(doc map[string]any) []NativePart {
	m, _ := doc["hooks"].(map[string]any)
	if m == nil {
		m = doc
	}
	var out []NativePart
	for _, ev := range slices.Sorted(mapKeys(m)) {
		groups, _ := m[ev].([]any)
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			hs, _ := gm["hooks"].([]any)
			for _, h := range hs {
				hm, _ := h.(map[string]any)
				if hm == nil {
					continue
				}
				name := ev
				if mt := plStr(gm["matcher"]); mt != "" {
					name += " · " + mt
				}
				detail := plStr(hm["command"])
				if detail == "" {
					detail = plStr(hm["type"])
					if s, t := plStr(hm["server"]), plStr(hm["tool"]); s != "" {
						detail += " " + s + "/" + t
					}
				}
				out = append(out, NativePart{Kind: "hook", Name: name, Detail: detail})
			}
		}
	}
	return out
}

// nativeSkills are the skills below dir: a folder with a SKILL.md is one,
// not looked into further.
func nativeSkills(dir string, depth int) []NativePart {
	if m, ok := readMeta(dir); ok {
		name := m.Name
		if name == "" {
			name = filepath.Base(dir)
		}
		return []NativePart{{Kind: "skill", Name: name, Detail: m.Description, dir: dir}}
	}
	if depth > 6 {
		return nil
	}
	var out []NativePart
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" {
			continue
		}
		d := filepath.Join(dir, e.Name())
		if e.IsDir() || e.Type()&fs.ModeSymlink != 0 && isDir(d) {
			out = append(out, nativeSkills(d, depth+1)...)
		}
		if len(out) >= nativePartsMax {
			break
		}
	}
	return out
}

// nativeUnique drops a part listed twice, and keeps the list bounded.
func nativeUnique(parts []NativePart) []NativePart {
	type key struct{ kind, name, detail, dir string }
	seen := map[key]bool{}
	out := []NativePart{}
	for _, p := range parts {
		k := key{p.Kind, p.Name, p.Detail, p.dir}
		if seen[k] || len(out) >= nativePartsMax {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	kinds := map[string]int{"skill": 0, "mcp": 1, "hook": 2, "subagent": 3}
	sort.SliceStable(out, func(i, j int) bool { return kinds[out[i].Kind] < kinds[out[j].Kind] })
	return out
}

// ---- Pi ---------------------------------------------------------------------

func piNativePlugins() []NativePlugin {
	var out []NativePlugin
	var local *piPlace
	for _, p := range piPlaces() {
		if p.asleep || p.err != nil {
			continue
		}
		if p.a.WSL == "" && local == nil {
			c := p
			local = &c
		}
		out = append(out, piNativeIn(p, p, "user", "")...)
	}
	if local != nil {
		for _, dir := range nativeProjects() {
			pp := *local
			pp.dir = filepath.Join(dir, ".pi")
			// a project's packages load once Pi trusts the project
			out = append(out, piNativeIn(*local, pp, "project", dir)...)
		}
	}
	return out
}

// nativeProjects are the library's projects, read without changing it.
func nativeProjects() []string {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range l.Projects {
		out = append(out, p.Dir)
	}
	return out
}

// piNativeIn lists the packages in the settings.json of where, for the Pi
// pi. One that isn't on disk yet is not listed: Pi installs it when it
// next starts, and nothing is known of what it declares.
func piNativeIn(pi, where piPlace, scope, project string) []NativePlugin {
	s, err := where.settings()
	if err != nil {
		return nil
	}
	var out []NativePlugin
	seen := map[string]bool{}
	for _, r := range s.pkgs {
		pk, ok := piPackageOf(r)
		if !ok {
			continue
		}
		id := where.piIdentity(pk.Source)
		if seen[id] {
			continue
		}
		seen[id] = true
		dir := where.packageDir(pk)
		if dir == "" {
			continue
		}
		var man struct {
			Name, Version string
		}
		if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			json.Unmarshal(stripBOM(raw), &man)
		}
		p := NativePlugin{Agent: pi.a.ID, AgentName: pi.a.Name, Name: pk.Name, Version: man.Version, Source: pk.Source,
			Scope: scope, Project: project, Dir: dir, Loaded: "unknown", Parts: piSkills(dir)}
		if man.Name != "" {
			p.Name = man.Name
		}
		if scope == "user" {
			on := pk.On
			p.Enabled = &on
		}
		out = append(out, p)
	}
	return out
}

// piSkills are the skills a package declares: package.json pi.skills when
// it has a "pi" object, else its skills folder.
func piSkills(root string) []NativePart {
	var pkg struct {
		Pi map[string]json.RawMessage `json:"pi"`
	}
	if raw, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		json.Unmarshal(stripBOM(raw), &pkg)
	}
	if pkg.Pi == nil {
		return nativeUnique(nativeSkills(filepath.Join(root, "skills"), 0))
	}
	var entries []string
	if json.Unmarshal(pkg.Pi["skills"], &entries) != nil {
		return []NativePart{}
	}
	var dirs, drops []string
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e, "!") || strings.HasPrefix(e, "-"):
			drops = append(drops, e[1:])
		case strings.HasPrefix(e, "+"):
		case piIsPattern(e):
			dirs = append(dirs, piExpand(root, e)...)
		default:
			if d, ok := inside(root, e); ok {
				dirs = append(dirs, d)
			}
		}
	}
	var parts []NativePart
	dropped := func(p string) bool {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return true
		}
		rel = filepath.ToSlash(rel)
		for _, x := range drops {
			x = strings.TrimPrefix(filepath.ToSlash(x), "./")
			re := piGlob(x)
			if rel == x || re.MatchString(rel) || re.MatchString(rel+"/SKILL.md") || re.MatchString(filepath.Base(p)) {
				return true
			}
		}
		return false
	}
	for _, d := range dirs {
		if dropped(d) {
			continue
		}
		if isDir(d) {
			for _, sk := range nativeSkills(d, 0) {
				if !dropped(sk.dir) {
					parts = append(parts, sk)
				}
			}
		} else if strings.HasSuffix(d, ".md") {
			if b, err := os.ReadFile(d); err == nil {
				m := parseMeta(b)
				name := m.Name
				if name == "" {
					name = strings.TrimSuffix(filepath.Base(d), ".md")
				}
				parts = append(parts, NativePart{Kind: "skill", Name: name, Detail: m.Description, dir: d})
			}
		}
	}
	return nativeUnique(parts)
}

// piExpand is the folders and files below root that a glob of a package's
// manifest matches.
func piExpand(root, glob string) []string {
	re := piGlob(strings.TrimPrefix(filepath.ToSlash(glob), "./"))
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return nil
		}
		if re.MatchString(filepath.ToSlash(rel)) {
			out = append(out, p)
		}
		if len(out) >= nativePartsMax {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}
