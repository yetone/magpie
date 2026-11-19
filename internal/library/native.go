package library

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
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

// NativePart is one thing a native plugin or package declares.
type NativePart struct {
	// Kind is skill, mcp or hook
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Detail is a skill's description, a server's transport, a hook's
	// command
	Detail string `json:"detail,omitempty"`

	dir string // a skill's folder, what a Pi package's filters match
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
}

const nativePartsMax = 500

// ReadNative lists what the agents' own plugins and packages declare. It
// writes nothing and starts nothing.
func ReadNative() []NativePlugin {
	out := codexNative()
	out = append(out, piNativePlugins()...)
	if out == nil {
		out = []NativePlugin{}
	}
	return out
}

// ---- Codex ------------------------------------------------------------------

type codexConf struct {
	Plugins      map[string]struct{ Enabled *bool } `toml:"plugins"`
	Marketplaces map[string]struct{ Source string } `toml:"marketplaces"`
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
			if s := conf.Marketplaces[m.Name()].Source; s != "" {
				p.Source = m.Name() + " (" + s + ")"
			}
			p.Enabled = conf.Plugins[pl.Name()+"@"+m.Name()].Enabled
			p.Parts = codexParts(root, man)
			out = append(out, p)
		}
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

func codexParts(root string, man map[string]any) []NativePart {
	var parts []NativePart
	// skills
	roots := pathsOf(man["skills"])
	if len(roots) == 0 {
		roots = []string{"skills"}
	}
	for _, r := range roots {
		if d, ok := inside(root, r); ok && isDir(d) {
			parts = append(parts, nativeSkills(d, 0)...)
		}
	}
	// MCP servers: a file or the servers inline, else .mcp.json
	var docs []map[string]any
	switch v := man["mcpServers"].(type) {
	case string:
		docs = nativeJSONFiles(root, []string{v})
	case map[string]any:
		docs = []map[string]any{v}
	default:
		docs = nativeJSONFiles(root, []string{".mcp.json"})
	}
	for _, d := range docs {
		parts = append(parts, nativeServers(d)...)
	}
	// hooks: files or inline objects, else hooks/hooks.json
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
		docs = nativeJSONFiles(root, []string{"hooks/hooks.json"})
	}
	for _, d := range docs {
		parts = append(parts, nativeHooks(d)...)
	}
	return nativeUnique(parts)
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

func nativeServers(doc map[string]any) []NativePart {
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
		out = append(out, NativePart{Kind: "mcp", Name: k, Detail: tr})
	}
	return out
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
	seen := map[NativePart]bool{}
	out := []NativePart{}
	for _, p := range parts {
		if seen[p] || len(out) >= nativePartsMax {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	kinds := map[string]int{"skill": 0, "mcp": 1, "hook": 2}
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
