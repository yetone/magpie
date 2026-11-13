package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/lazyre"
)

// A bundle is a Claude Code plugin kept in the library as one item
// (@FakeshadowzZ on X: manage a plugin's subagents, MCP servers, skills
// and hooks together). Its files are copied to <library>/plugins/<name>:
//
//	.claude-plugin/plugin.json   name, version, description, and paths for the parts below
//	skills/<name>/SKILL.md       skills
//	agents/<name>.md             subagents (front matter name, description; the prompt below)
//	hooks/hooks.json             hooks, {"hooks": {"PostToolUse": [{"matcher", "hooks": [{"type": "command", "command"}]}]}}
//	.mcp.json                    MCP servers, {"mcpServers": {...}}
//
// A plugin's parts become ordinary library skills, servers, hooks and
// subagents, each marked with its Bundle, so every agent gets them through
// the code that already writes each kind in that agent's own format, and
// the skills, MCP servers, Hooks and Subagents tabs go on working. The
// bundle records which items are its own and which agents it is given to:
// turning it off takes every part from every agent, turning it on gives
// them back, and removing it takes the parts out of the library. Parts no
// agent here can take (slash commands, LSP servers, hooks that aren't a
// command) are listed in Left, never silently dropped.
//
// ${CLAUDE_PLUGIN_ROOT} in a command, argument, header or value is the
// bundle's folder; ${CLAUDE_PLUGIN_DATA} a folder of its own that stays
// across updates.

// Plugin is a plugin the library keeps.
type Plugin struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	// Source is what the user gave to install it (a folder, a repository,
	// a marketplace.json's folder), and Pick the plugin's name in it when
	// that is a marketplace: an update fetches it again from there
	Source string `json:"source,omitempty"`
	Pick   string `json:"pick,omitempty"`
	// Agents are the agents it is given to when it is on
	Agents []string `json:"agents"`
	// Off is set while it is turned off: none of its parts is given to any
	// agent, and Agents are kept for turning it on again
	Off bool `json:"off,omitempty"`
	// the items it made, by name
	Skills    []string `json:"skills,omitempty"`
	MCP       []string `json:"mcp,omitempty"`
	Hooks     []string `json:"hooks,omitempty"`
	Subagents []string `json:"subagents,omitempty"`
	// Left are the parts of the plugin magpie doesn't carry, and why
	Left []PluginLeft `json:"left,omitempty"`
	// Installed is when it was installed or last updated
	Installed string `json:"installed,omitempty"`
}

// PluginLeft is a part of a plugin that wasn't carried.
type PluginLeft struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

func (l *Library) plugin(name string) *Plugin {
	for _, b := range l.Plugins {
		if b.Name == name {
			return b
		}
	}
	return nil
}

func pluginsDir() string { return filepath.Join(Dir(), "plugins") }

func pluginDir(name string) string { return filepath.Join(pluginsDir(), name) }

// pluginData is a folder a plugin keeps its own files in, which an update
// of the plugin leaves as it is.
func pluginData(name string) string { return filepath.Join(Dir(), "plugin-data", name) }

// ---- reading a plugin --------------------------------------------------------

// plSkill is a skill found in a plugin's folder.
type plSkill struct {
	name, dir string
}

// plLoaded is a plugin as read from its folder.
type plLoaded struct {
	name, description, version string
	skills                     []plSkill
	servers                    []*Server
	hooks                      []*Hook
	agents                     []*Subagent
	left                       []PluginLeft
}

func (p *plLoaded) parts() int {
	return len(p.skills) + len(p.servers) + len(p.hooks) + len(p.agents)
}

func readJSONFile(p string) (map[string]any, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(stripBOM(b), &m); err != nil {
		return nil, fmt.Errorf("%s doesn't parse: %w", filepath.Base(p), err)
	}
	return m, nil
}

func stripBOM(b []byte) []byte { return []byte(strings.TrimPrefix(string(b), "\ufeff")) }

func plStr(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// pathsOf is a manifest value naming one path or a list of them.
func pathsOf(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// inside resolves a path a manifest names against the plugin's folder: one
// that leaves it is refused.
func inside(dir, p string) (string, bool) {
	p = filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(p, "./")))
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

var pluginName = lazyre.New(`[^A-Za-z0-9_-]+`)

func pluginSlug(s string) string {
	s = strings.Trim(pluginName.ReplaceAllString(strings.TrimSpace(s), "-"), "-_")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-_")
	}
	return s
}

// isPlugin is whether dir looks like a plugin's folder.
func isPlugin(dir string) bool {
	for _, p := range []string{".claude-plugin/plugin.json", "skills", "agents", "commands", "hooks/hooks.json", ".mcp.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil {
			return true
		}
	}
	return false
}

// readPlugin reads the plugin in dir. over are the fields a marketplace's
// entry gives for it, which a plugin.json overrides; hint is the name the
// marketplace calls it by.
func readPlugin(dir, hint string, over map[string]any) (*plLoaded, error) {
	man := map[string]any{}
	if m, err := readJSONFile(filepath.Join(dir, ".claude-plugin", "plugin.json")); err == nil {
		man = m
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for k, v := range over {
		if _, ok := man[k]; !ok {
			man[k] = v
		}
	}
	if !isPlugin(dir) && len(man) == 0 {
		return nil, fmt.Errorf("no Claude Code plugin in %s: it has no .claude-plugin/plugin.json, skills, agents, hooks or .mcp.json", dir)
	}
	name := pluginSlug(plStr(man["name"]))
	if name == "" {
		name = pluginSlug(hint)
	}
	if name == "" {
		name = pluginSlug(filepath.Base(dir))
	}
	if name == "" {
		return nil, errors.New("the plugin has no name")
	}
	p := &plLoaded{name: name, description: strings.Join(strings.Fields(plStr(man["description"])), " "), version: plStr(man["version"])}
	root, data := pluginDir(name), pluginData(name)
	sub := func(s string) string {
		return strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", root, "$CLAUDE_PLUGIN_ROOT", root,
			"${CLAUDE_PLUGIN_DATA}", data, "$CLAUDE_PLUGIN_DATA", data).Replace(s)
	}
	leave := func(what, why string) { p.left = append(p.left, PluginLeft{What: what, Why: why}) }

	// skills
	seen := map[string]bool{}
	for _, rel := range append([]string{"skills"}, pathsOf(man["skills"])...) {
		d, ok := inside(dir, rel)
		if !ok {
			leave("skills "+rel, "the path leaves the plugin's folder")
			continue
		}
		var dirs []string
		if _, ok := readMeta(d); ok {
			dirs = []string{d}
		} else if es, err := os.ReadDir(d); err == nil {
			for _, e := range es {
				if c := filepath.Join(d, e.Name()); e.IsDir() || isDirLink(c) {
					if _, ok := readMeta(c); ok {
						dirs = append(dirs, c)
					}
				}
			}
		}
		for _, sd := range dirs {
			m, _ := readMeta(sd)
			n := nameFor(filepath.Base(sd), m)
			if n == "" || seen[sd] {
				continue
			}
			seen[sd] = true
			if err := checkName("skill", n); err != nil {
				leave("skill "+n, err.Error())
				continue
			}
			p.skills = append(p.skills, plSkill{name: n, dir: sd})
		}
	}
	sort.Slice(p.skills, func(i, j int) bool { return p.skills[i].name < p.skills[j].name })

	// subagents
	var agentFiles []string
	for _, rel := range append([]string{"agents"}, pathsOf(man["agents"])...) {
		d, ok := inside(dir, rel)
		if !ok {
			leave("agents "+rel, "the path leaves the plugin's folder")
			continue
		}
		if fi, err := os.Stat(d); err == nil && !fi.IsDir() {
			agentFiles = append(agentFiles, d)
		} else if es, err := os.ReadDir(d); err == nil {
			for _, e := range es {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
					agentFiles = append(agentFiles, filepath.Join(d, e.Name()))
				}
			}
		}
	}
	slices.Sort(agentFiles)
	agentFiles = slices.Compact(agentFiles)
	for _, f := range agentFiles {
		s, err := readSubagentFile(f, p)
		if err != nil {
			leave("agent "+strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)), err.Error())
			continue
		}
		p.agents = append(p.agents, s)
	}

	// MCP servers
	var mcpDocs []map[string]any
	if m, err := readJSONFile(filepath.Join(dir, ".mcp.json")); err == nil {
		mcpDocs = append(mcpDocs, m)
	} else if !errors.Is(err, fs.ErrNotExist) {
		leave(".mcp.json", err.Error())
	}
	for _, v := range manifestList(man["mcpServers"]) {
		switch x := v.(type) {
		case string:
			if d, ok := inside(dir, x); ok {
				if m, err := readJSONFile(d); err == nil {
					mcpDocs = append(mcpDocs, m)
				} else {
					leave(x, err.Error())
				}
			}
		case map[string]any:
			mcpDocs = append(mcpDocs, x)
		}
	}
	for _, doc := range mcpDocs {
		servers, left := serversOf(doc, sub)
		p.servers = append(p.servers, servers...)
		p.left = append(p.left, left...)
	}

	// hooks
	var hookDocs []map[string]any
	if m, err := readJSONFile(filepath.Join(dir, "hooks", "hooks.json")); err == nil {
		hookDocs = append(hookDocs, m)
	} else if !errors.Is(err, fs.ErrNotExist) {
		leave("hooks/hooks.json", err.Error())
	}
	for _, v := range manifestList(man["hooks"]) {
		switch x := v.(type) {
		case string:
			if d, ok := inside(dir, x); ok {
				if m, err := readJSONFile(d); err == nil {
					hookDocs = append(hookDocs, m)
				} else if !errors.Is(err, fs.ErrNotExist) {
					leave(x, err.Error())
				}
			}
		case map[string]any:
			hookDocs = append(hookDocs, x)
		}
	}
	n := 0
	for _, doc := range hookDocs {
		hooks, left := plHooksOf(doc, name, root, sub, &n)
		p.hooks = append(p.hooks, hooks...)
		p.left = append(p.left, left...)
	}

	// what no agent here takes
	if es, err := os.ReadDir(filepath.Join(dir, "commands")); err == nil {
		c := 0
		for _, e := range es {
			if strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				c++
			}
		}
		if c > 0 {
			leave(fmt.Sprintf("%d slash commands", c), "slash commands are Claude Code's own: magpie carries a plugin's skills, MCP servers, hooks and subagents")
		}
	}
	for _, k := range []string{"lspServers", "outputStyles", "themes", "monitors"} {
		if man[k] != nil {
			leave(k, "no agent magpie writes to reads it")
		}
	}
	return p, nil
}

// manifestList is a manifest value that is one thing or a list of them.
func manifestList(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	}
	return []any{v}
}

func isDirLink(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// subagentName is the name a subagent goes by in every agent: lowercase
// letters, digits and -.
func subagentName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-"), "-")
	if len(s) > 50 {
		s = strings.Trim(s[:50], "-")
	}
	return s
}

// splitFront is a Markdown file's YAML front matter and what follows it.
func splitFront(b []byte) (map[string]any, string) {
	s := strings.ReplaceAll(strings.TrimPrefix(string(b), "\ufeff"), "\r\n", "\n")
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return nil, s
	}
	front, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return nil, s
	}
	body = strings.TrimPrefix(body, "\n")
	var m map[string]any
	if yaml.Unmarshal([]byte(front), &m) != nil {
		return nil, s
	}
	return m, body
}

func readSubagentFile(f string, p *plLoaded) (*Subagent, error) {
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	front, body := splitFront(b)
	name := subagentName(plStr(front["name"]))
	if name == "" {
		name = subagentName(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)))
	}
	s := &Subagent{
		Name:        name,
		Description: strings.Join(strings.Fields(plStr(front["description"])), " "),
		Prompt:      strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \n\t"),
		Agents:      []string{},
	}
	// the skills it names that this plugin brought
	var named []string
	switch x := front["skills"].(type) {
	case string:
		named = strings.Split(x, ",")
	case []any:
		for _, e := range x {
			named = append(named, plStr(e))
		}
	}
	for _, n := range named {
		n = strings.TrimSpace(n)
		if i := strings.LastIndex(n, ":"); i >= 0 {
			n = n[i+1:]
		}
		if slices.ContainsFunc(p.skills, func(k plSkill) bool { return k.name == n }) {
			s.Skills = append(s.Skills, n)
		}
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	return s, nil
}

// serversOf reads a .mcp.json: {"mcpServers": {...}} or the map itself.
func serversOf(doc map[string]any, sub func(string) string) ([]*Server, []PluginLeft) {
	m, _ := doc["mcpServers"].(map[string]any)
	if m == nil {
		m = doc
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []*Server
	var left []PluginLeft
	for _, k := range names {
		e, ok := m[k].(map[string]any)
		if !ok {
			continue
		}
		s := &Server{Name: pluginSlug(k), Agents: []string{}}
		switch typ := plStr(e["type"]); {
		case typ == "sse":
			s.Transport = "sse"
		case typ == "http" || typ == "streamable-http" || (typ == "" && plStr(e["url"]) != ""):
			s.Transport = "http"
		case typ == "" || typ == "stdio":
			s.Transport = "stdio"
		default:
			left = append(left, PluginLeft{What: "MCP server " + k, Why: "a server of type " + typ + " isn't one agents take"})
			continue
		}
		s.Command, s.URL = sub(plStr(e["command"])), sub(plStr(e["url"]))
		if a, ok := e["args"].([]any); ok {
			for _, x := range a {
				s.Args = append(s.Args, sub(fmt.Sprint(x)))
			}
		}
		strMap := func(v any) map[string]string {
			mm, _ := v.(map[string]any)
			if len(mm) == 0 {
				return nil
			}
			out := map[string]string{}
			for k, x := range mm {
				out[k] = sub(fmt.Sprint(x))
			}
			return out
		}
		s.Env, s.Headers = strMap(e["env"]), strMap(e["headers"])
		if err := s.check(); err != nil {
			left = append(left, PluginLeft{What: "MCP server " + k, Why: err.Error()})
			continue
		}
		out = append(out, s)
	}
	return out, left
}

// claudeEvents are Claude Code's hook events that a portable event of
// the library's stands for.
var claudeEvents = map[string]string{"Stop": "turn-end", "PostToolUse": "after-tool", "PreToolUse": "before-tool",
	"UserPromptSubmit": "prompt", "SessionStart": "session-start"}

// toolKind is a Claude Code tool matcher as a portable kind of tool, or
// "" when it names others.
func toolKind(m string) (string, bool) {
	if m == "" || m == "*" {
		return "", true
	}
	set := map[string]bool{}
	for _, x := range strings.Split(m, "|") {
		set[strings.TrimSpace(x)] = true
	}
	switch {
	case len(set) == 1 && set["Bash"]:
		return "shell", true
	case (len(set) == 2 && set["Edit"] && set["Write"]) || (len(set) == 3 && set["Edit"] && set["Write"] && set["MultiEdit"]):
		return "edit", true
	}
	return "", false
}

// plHooksOf reads a hooks.json: {"hooks": {Event: [...]}} or the map itself.
// A hook whose event and matcher are another agent's the same moment too
// is the portable one; any other is given to Claude Code alone, as
// written.
func plHooksOf(doc map[string]any, bundle, root string, sub func(string) string, n *int) ([]*Hook, []PluginLeft) {
	m, _ := doc["hooks"].(map[string]any)
	if m == nil {
		m = doc
	}
	events := make([]string, 0, len(m))
	for k := range m {
		events = append(events, k)
	}
	sort.Strings(events)
	var out []*Hook
	var left []PluginLeft
	for _, ev := range events {
		groups, _ := m[ev].([]any)
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			if gm == nil {
				continue
			}
			matcher := plStr(gm["matcher"])
			hs, _ := gm["hooks"].([]any)
			for _, h := range hs {
				hm, _ := h.(map[string]any)
				if hm == nil {
					continue
				}
				if t := plStr(hm["type"]); t != "" && t != "command" {
					left = append(left, PluginLeft{What: ev + " hook", Why: "a hook of type " + t + " isn't a command: agents take commands only"})
					continue
				}
				cmd := sub(quoteRoot(plStr(hm["command"]), root))
				if cmd == "" {
					continue
				}
				*n++
				x := &Hook{Name: hookName(bundle, *n), Description: "From plugin " + bundle, Command: cmd, Agents: []string{}}
				if t, ok := hm["timeout"].(float64); ok && t > 0 && t <= 86400 {
					x.Timeout = int(t)
				}
				if portable, ok := claudeEvents[ev]; ok {
					k, tools := toolKind(matcher)
					if slices.Contains(toolEvents, portable) && tools {
						x.Event, x.Matcher = portable, k
					} else if !slices.Contains(toolEvents, portable) && matcher == "" {
						x.Event = portable
					}
				}
				if x.Event == "" {
					if !slices.Contains(claudeHookEvents, ev) {
						left = append(left, PluginLeft{What: ev + " hook", Why: "Claude Code has no event called " + ev})
						*n--
						continue
					}
					x.Event, x.Matcher = "claude:"+ev, matcher
				}
				if err := x.check(); err != nil {
					left = append(left, PluginLeft{What: ev + " hook", Why: err.Error()})
					*n--
					continue
				}
				out = append(out, x)
			}
		}
	}
	return out, left
}

// quoteRoot quotes the plugin's folder where a command names it bare and
// the folder has a space in it, as a plugin's own scripts assume it has
// none.
func quoteRoot(cmd, root string) string {
	if !strings.ContainsAny(root, " \t") {
		return cmd
	}
	re := regexp.MustCompile(`(["']?)(\$\{CLAUDE_PLUGIN_ROOT\}|\$CLAUDE_PLUGIN_ROOT)([^\s"']*)`)
	return re.ReplaceAllStringFunc(cmd, func(s string) string {
		if s[0] == '"' || s[0] == '\'' {
			return s
		}
		return `"` + s + `"`
	})
}

func hookName(bundle string, n int) string {
	b := bundle
	if len(b) > 40 {
		b = b[:40]
	}
	return fmt.Sprintf("%s-%d", b, n)
}

// ---- finding plugins ---------------------------------------------------------

// PluginCandidate is a plugin found at what the user gave.
type PluginCandidate struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Skills      int    `json:"skills"`
	MCP         int    `json:"mcp"`
	Hooks       int    `json:"hooks"`
	Subagents   int    `json:"subagents"`
	// Remote: it is fetched from another repository when installed, so
	// its parts aren't counted
	Remote bool `json:"remote,omitempty"`
	// Have: the library has it already
	Have bool         `json:"have,omitempty"`
	Left []PluginLeft `json:"left,omitempty"`
	// where it is, in the probe's folder
	dir  string
	over map[string]any
	src  *Source // another repository, for a marketplace's entry that names one
	path string  // its folder in that repository
}

// PluginProbe is what was found at an address.
type PluginProbe struct {
	Source      string            `json:"source"`
	Marketplace string            `json:"marketplace,omitempty"` // its name, when there are several
	Candidates  []PluginCandidate `json:"candidates"`
	root        string
}

// resolvePluginRoot is the folder a plugin or a marketplace is in, for
// what the user gave: a folder, a plugin.json or marketplace.json in one,
// or a git repository. tmp is a download that is the caller's to remove.
func resolvePluginRoot(input string) (root, tmp string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", errors.New("paste a plugin's folder, or a GitHub or git repository's link")
	}
	if strings.HasPrefix(input, "-") {
		return "", "", errors.New("an address can't start with -")
	}
	if dir := expand(input); filepath.IsAbs(dir) {
		fi, err := os.Stat(dir)
		if err != nil {
			return "", "", fmt.Errorf("%s isn't a folder", input)
		}
		if !fi.IsDir() {
			// .claude-plugin/marketplace.json or plugin.json
			dir = filepath.Dir(dir)
			if filepath.Base(dir) == ".claude-plugin" {
				dir = filepath.Dir(dir)
			}
		}
		return dir, "", nil
	}
	root, tmp, _, err = fetchSourceRoot(input)
	if err != nil {
		return "", "", err
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		os.RemoveAll(tmp)
		return "", "", fmt.Errorf("%s isn't a folder", input)
	}
	return root, tmp, nil
}

// fetchSourceRoot downloads the GitHub or git repository input names, whole,
// and gives back the folder or file in it the link points to, the download
// to remove after, and the repository with the path in it.
func fetchSourceRoot(input string) (root, tmp string, src Source, err error) {
	sweepStaleDownloads()
	src, ok := githubSource(input)
	if !ok {
		if src, ok, err = gitSource(input); err != nil {
			return "", "", Source{}, err
		} else if !ok {
			return "", "", Source{}, errors.New("that's neither a git repository's link (GitHub, GitLab or another host) nor a folder's full path")
		}
	}
	path := src.Path
	src.Path = ""
	tmp, err = fetch(src)
	if err != nil {
		return "", "", Source{}, err
	}
	src.Path = path
	if path == "" {
		return tmp, tmp, src, nil
	}
	sub, ok := inside(tmp, path)
	if _, err := os.Stat(sub); !ok || err != nil {
		os.RemoveAll(tmp)
		return "", "", Source{}, fmt.Errorf("%s has no folder %s", src.Repo, path)
	}
	return sub, tmp, src, nil
}

// findPlugins reads the plugins at root: a marketplace's entries, or the
// one plugin there.
func findPlugins(root string) (*PluginProbe, error) {
	p := &PluginProbe{root: root, Candidates: []PluginCandidate{}}
	mk, err := readJSONFile(filepath.Join(root, ".claude-plugin", "marketplace.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if mk == nil {
		pl, err := readPlugin(root, filepath.Base(root), nil)
		if err != nil {
			return nil, err
		}
		p.Candidates = append(p.Candidates, candidateOf(pl, root, nil))
		return p, nil
	}
	p.Marketplace = plStr(mk["name"])
	pluginRoot := ""
	if md, ok := mk["metadata"].(map[string]any); ok {
		pluginRoot = plStr(md["pluginRoot"])
	}
	entries, _ := mk["plugins"].([]any)
	for _, e := range entries {
		em, _ := e.(map[string]any)
		if em == nil || pluginSlug(plStr(em["name"])) == "" {
			continue
		}
		over := map[string]any{}
		for _, k := range []string{"description", "version", "skills", "agents", "hooks", "mcpServers"} {
			if v, ok := em[k]; ok {
				over[k] = v
			}
		}
		c := PluginCandidate{Name: pluginSlug(plStr(em["name"])), Description: strings.Join(strings.Fields(plStr(em["description"])), " "), Version: plStr(em["version"]), over: over}
		switch s := em["source"].(type) {
		case string:
			d, ok := inside(root, filepath.Join(pluginRoot, s))
			if !ok {
				c.Left = []PluginLeft{{What: c.Name, Why: "its source leaves the marketplace's folder"}}
				break
			}
			if pl, err := readPlugin(d, c.Name, over); err == nil {
				c2 := candidateOf(pl, d, over)
				c2.Name = c.Name
				c = c2
			} else {
				c.Left = []PluginLeft{{What: c.Name, Why: err.Error()}}
			}
			c.dir = d
		case map[string]any:
			src, why := marketSource(s)
			if why != "" {
				c.Left = []PluginLeft{{What: c.Name, Why: why}}
				break
			}
			c.Remote, c.src, c.path = true, &src, plStr(s["path"])
		default:
			c.Left = []PluginLeft{{What: c.Name, Why: "it has no source"}}
		}
		c.over = over
		p.Candidates = append(p.Candidates, c)
	}
	if len(p.Candidates) == 0 {
		return nil, errors.New("the marketplace lists no plugins")
	}
	return p, nil
}

// marketSource is where a marketplace entry's plugin is fetched from.
func marketSource(s map[string]any) (Source, string) {
	ref := plStr(s["ref"])
	switch plStr(s["source"]) {
	case "github":
		repo := plStr(s["repo"])
		if src, ok := githubSource(repo); ok {
			src.Ref = ref
			return src, ""
		}
		return Source{}, "its GitHub repository " + repo + " isn't owner/repo"
	case "url", "git", "git-subdir":
		u := plStr(s["url"])
		if src, ok := githubSource(u); ok {
			src.Ref = ref
			return src, ""
		}
		if src, ok, err := gitSource(u); ok && err == nil {
			src.Ref = ref
			return src, ""
		}
		return Source{}, "its address " + u + " isn't a git repository's"
	}
	return Source{}, "its source of kind " + plStr(s["source"]) + " isn't one magpie fetches"
}

func candidateOf(pl *plLoaded, dir string, over map[string]any) PluginCandidate {
	return PluginCandidate{Name: pl.name, Description: pl.description, Version: pl.version,
		Skills: len(pl.skills), MCP: len(pl.servers), Hooks: len(pl.hooks), Subagents: len(pl.agents),
		Left: pl.left, dir: dir, over: over}
}

var pluginProbes = struct {
	m map[string]*PluginProbe
}{m: map[string]*PluginProbe{}}

// ProbePlugins looks for plugins at what the user typed: a plugin's
// folder, a marketplace's (its .claude-plugin/marketplace.json), or a
// repository with either.
func ProbePlugins(input string) (*PluginProbe, error) {
	root, tmp, err := resolvePluginRoot(input)
	if err != nil {
		return nil, err
	}
	p, err := findPlugins(root)
	if err != nil {
		if tmp != "" {
			os.RemoveAll(tmp)
		}
		return nil, err
	}
	p.Source = strings.TrimSpace(input)
	if l, err := load(); err == nil {
		for i := range p.Candidates {
			p.Candidates[i].Have = l.plugin(p.Candidates[i].Name) != nil
		}
	}
	if tmp != "" {
		// the install that follows uses this download; it is removed
		// with the probe's life, as a skill's is
		keepProbe("plugin:"+p.Source, &Probe{Source: p.Source, root: root}, tmp)
	}
	return p, nil
}

// ---- installing ----------------------------------------------------------------

// InstallPlugins installs the plugins named (all of them, of one plugin)
// from what the user gave, and gives them to the agents. One the library
// has is replaced by this one, keeping the agents it was given to and
// whether it is on.
func InstallPlugins(input string, names, agents []string) (*Result, error) {
	input = strings.TrimSpace(input)
	p, cleanup, err := pluginProbeFor(input)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return installPlugins(p, input, names, agents)
}

// pluginProbeFor is the probe of input, from the one the page just made when it
// is still kept; cleanup removes a download made here.
func pluginProbeFor(input string) (*PluginProbe, func(), error) {
	if dir := expand(input); !filepath.IsAbs(dir) {
		probes.Lock()
		e := probes.m["plugin:"+input]
		probes.Unlock()
		if e != nil {
			if _, err := os.Stat(e.tmp); err == nil {
				if p, err := findPlugins(e.p.root); err == nil {
					p.Source = input
					return p, func() {}, nil
				}
			}
		}
	}
	root, tmp, err := resolvePluginRoot(input)
	if err != nil {
		return nil, nil, err
	}
	p, err := findPlugins(root)
	if err != nil {
		if tmp != "" {
			os.RemoveAll(tmp)
		}
		return nil, nil, err
	}
	p.Source = input
	return p, func() {
		if tmp != "" {
			os.RemoveAll(tmp)
		}
	}, nil
}

func installPlugins(p *PluginProbe, input string, names, agents []string) (*Result, error) {
	var picked []PluginCandidate
	switch {
	case len(names) == 0 && len(p.Candidates) == 1:
		picked = p.Candidates
	case len(names) == 0:
		return nil, fmt.Errorf("%s lists %d plugins: pick the ones to install", p.Marketplace, len(p.Candidates))
	default:
		for _, n := range names {
			i := slices.IndexFunc(p.Candidates, func(c PluginCandidate) bool { return c.Name == n })
			if i < 0 {
				return nil, fmt.Errorf("no plugin called %s there", n)
			}
			picked = append(picked, p.Candidates[i])
		}
	}
	// read each before anything changes: one that can't be read stops the
	// install with nothing written
	type one struct {
		c  PluginCandidate
		pl *plLoaded
		d  string
	}
	var ones []one
	var cleanups []func()
	defer func() {
		for _, f := range cleanups {
			f()
		}
	}()
	for _, c := range picked {
		d := c.dir
		if c.Remote {
			tmp, err := fetch(*c.src)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.Name, err)
			}
			cleanups = append(cleanups, func() { os.RemoveAll(tmp) })
			var ok bool
			if d, ok = inside(tmp, c.path); !ok {
				return nil, fmt.Errorf("%s: its folder %s leaves its repository", c.Name, c.path)
			}
		}
		pl, err := readPlugin(d, c.Name, c.over)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, err)
		}
		if len(p.Candidates) > 1 || c.Remote {
			pl.name = c.Name // a marketplace's name for it
		}
		if pl.parts() == 0 {
			return nil, fmt.Errorf("%s has no skills, MCP servers, hooks or subagents magpie can give an agent", pl.name)
		}
		ones = append(ones, one{c, pl, d})
	}
	var done []string
	var skipped []Problem
	res, err := change(func(l *Library) error {
		done, skipped = nil, nil
		for _, o := range ones {
			pick := ""
			if len(p.Candidates) > 1 {
				pick = o.pl.name
			}
			sk, err := installPlugin(l, o.pl, o.d, input, pick, agents)
			if err != nil {
				return fmt.Errorf("%s: %w", o.pl.name, err)
			}
			skipped = append(skipped, sk...)
			done = append(done, o.pl.name)
		}
		return nil
	})
	if res != nil {
		res.Installed, res.Skipped = done, skipped
	}
	return res, err
}

// installPlugin puts one plugin in l: its files in the library's folder,
// its parts among the library's items.
func installPlugin(l *Library, pl *plLoaded, dir, input, pick string, agents []string) ([]Problem, error) {
	old := l.plugin(pl.name)
	b := &Plugin{Name: pl.name, Description: pl.description, Version: pl.version, Source: input, Pick: pick,
		Agents: orNone(slices.Compact(slices.Sorted(slices.Values(agents)))), Installed: time.Now().Format(time.RFC3339)}
	if old != nil {
		b.Agents, b.Off = old.Agents, old.Off
		if len(agents) > 0 {
			b.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(agents))))
		}
	}
	// the files first, beside the old ones, and swapped in once copied
	if err := os.MkdirAll(pluginsDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(pluginData(pl.name), 0o755); err != nil {
		return nil, err
	}
	stage := pluginDir(pl.name) + ".new"
	os.RemoveAll(stage)
	if err := copyDir(dir, stage); err != nil {
		os.RemoveAll(stage)
		return nil, err
	}
	// what the old version made goes, to be made again
	if old != nil {
		removePluginParts(l, old)
	}
	os.RemoveAll(pluginDir(pl.name))
	if err := os.Rename(stage, pluginDir(pl.name)); err != nil {
		os.RemoveAll(stage)
		return nil, err
	}
	root := pluginDir(pl.name)
	// skills are read again from the copy
	rel := func(p string) string { r, _ := filepath.Rel(dir, p); return filepath.Join(root, r) }

	var skipped []Problem
	skip := func(what, why string) {
		b.Left = append(b.Left, PluginLeft{What: what, Why: why})
		skipped = append(skipped, Problem{What: "plugin:" + pl.name, Error: what + ": " + why})
	}
	b.Left = append(b.Left, pl.left...)
	for _, k := range pl.left {
		skipped = append(skipped, Problem{What: "plugin:" + pl.name, Error: k.What + ": " + k.Why})
	}
	// a name another item has is given this plugin's name before it
	free := func(name string, taken func(string) bool, ok func(string) bool) (string, bool) {
		for _, n := range []string{name, pluginSlug(pl.name) + "-" + name} {
			if ok(n) && !taken(n) {
				return n, true
			}
		}
		return "", false
	}
	skillNames := map[string]string{}
	for _, s := range pl.skills {
		n, ok := free(s.name, func(n string) bool {
			if l.skill(n) != nil {
				return true
			}
			_, err := os.Lstat(skillDir(n))
			return err == nil
		}, func(n string) bool { return checkName("skill", n) == nil })
		if !ok {
			skip("skill "+s.name, "the library already has one by that name")
			continue
		}
		if err := os.MkdirAll(skillsDir(), 0o755); err != nil {
			return nil, err
		}
		if err := copyDir(rel(s.dir), skillDir(n)); err != nil {
			os.RemoveAll(skillDir(n))
			skip("skill "+s.name, err.Error())
			continue
		}
		l.Skills = append(l.Skills, &Skill{Name: n, Agents: []string{}, Bundle: pl.name})
		b.Skills = append(b.Skills, n)
		skillNames[s.name] = n
	}
	for _, s := range pl.servers {
		n, ok := free(s.Name, func(n string) bool { return l.server(n) != nil }, func(n string) bool { return checkName("server", n) == nil })
		if !ok {
			skip("MCP server "+s.Name, "the library already has one by that name")
			continue
		}
		s.Name, s.Bundle = n, pl.name
		l.MCP = append(l.MCP, s)
		b.MCP = append(b.MCP, n)
	}
	for _, h := range pl.hooks {
		n, ok := free(h.Name, func(n string) bool { return l.hook(n) != nil }, func(n string) bool { return checkName("hook", n) == nil })
		if !ok {
			skip("hook "+h.Name, "the library already has one by that name")
			continue
		}
		h.Name, h.Bundle = n, pl.name
		l.Hooks = append(l.Hooks, h)
		b.Hooks = append(b.Hooks, n)
	}
	for _, s := range pl.agents {
		n, ok := free(s.Name, func(n string) bool { return l.subagent(n) != nil }, func(n string) bool { return subagentNameRe.MatchString(n) })
		if !ok {
			skip("subagent "+s.Name, "the library already has one by that name")
			continue
		}
		s.Name, s.Bundle = n, pl.name
		for i, k := range s.Skills {
			if m := skillNames[k]; m != "" {
				s.Skills[i] = m
			}
		}
		l.Subagents = append(l.Subagents, s)
		b.Subagents = append(b.Subagents, n)
	}
	if len(b.Skills)+len(b.MCP)+len(b.Hooks)+len(b.Subagents) == 0 {
		os.RemoveAll(pluginDir(pl.name))
		return nil, errors.New("every part of it clashes with the library's own: " + skipped[len(skipped)-1].Error)
	}
	if old != nil {
		l.Plugins = slices.DeleteFunc(l.Plugins, func(x *Plugin) bool { return x.Name == old.Name })
	}
	l.Plugins = append(l.Plugins, b)
	sort.Slice(l.Plugins, func(i, j int) bool { return l.Plugins[i].Name < l.Plugins[j].Name })
	l.givePlugin(b)
	return skipped, nil
}

// givePlugin sets each part's agents from the bundle's: its Agents when
// on, none when off. A part an agent can't take is left out for that
// agent, not given and then refused.
func (l *Library) givePlugin(b *Plugin) {
	agents := b.Agents
	if b.Off {
		agents = nil
	}
	for _, n := range b.Skills {
		if s := l.skill(n); s != nil {
			s.Agents = orNone(slices.Clone(agents))
		}
	}
	for _, n := range b.MCP {
		if s := l.server(n); s != nil {
			s.Agents = orNone(slices.Clone(agents))
		}
	}
	for _, n := range b.Hooks {
		if h := l.hook(n); h != nil {
			h.Agents = orNone(slices.DeleteFunc(slices.Clone(agents), func(a string) bool { return !hookTakes(h, a) }))
		}
	}
	for _, n := range b.Subagents {
		if s := l.subagent(n); s != nil {
			s.Agents = orNone(slices.DeleteFunc(slices.Clone(agents), func(a string) bool {
				base, _, _ := strings.Cut(a, "@wsl:")
				return !slices.Contains(SubagentAgents, base)
			}))
		}
	}
}

// removePluginParts takes out of l every item the bundle made.
func removePluginParts(l *Library, b *Plugin) {
	stamp := time.Now().Format("2006-01-02_15-04-05.000")
	for _, n := range b.Skills {
		if s := l.skill(n); s != nil && s.Bundle == b.Name {
			_ = removeSkill(l, n, stamp)
		}
	}
	for _, n := range b.MCP {
		if s := l.server(n); s != nil && s.Bundle == b.Name {
			_ = removeServer(l, n)
		}
	}
	l.Hooks = slices.DeleteFunc(l.Hooks, func(h *Hook) bool { return h.Bundle == b.Name && slices.Contains(b.Hooks, h.Name) })
	l.Subagents = slices.DeleteFunc(l.Subagents, func(s *Subagent) bool { return s.Bundle == b.Name && slices.Contains(b.Subagents, s.Name) })
}

// ---- changes -------------------------------------------------------------------

// PluginAgents sets which agents get a plugin's parts. Each part goes only
// to the agents that can take it.
func PluginAgents(name string, agents []string) (*Result, error) {
	for _, a := range agents {
		if err := checkAgent(a); err != nil {
			return nil, err
		}
	}
	return change(func(l *Library) error {
		b := l.plugin(name)
		if b == nil {
			return fmt.Errorf("no plugin called %s", name)
		}
		b.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(agents))))
		l.givePlugin(b)
		return nil
	})
}

// PluginOn turns a plugin on or off: off takes all its parts from every
// agent and keeps them in the library.
func PluginOn(name string, on bool) (*Result, error) {
	return change(func(l *Library) error {
		b := l.plugin(name)
		if b == nil {
			return fmt.Errorf("no plugin called %s", name)
		}
		b.Off = !on
		l.givePlugin(b)
		return nil
	})
}

// RemovePlugin takes a plugin out of the library, every part of it out of
// every agent and its folder out of the library's.
func RemovePlugin(name string) (*Result, error) {
	res, err := change(func(l *Library) error {
		b := l.plugin(name)
		if b == nil {
			return fmt.Errorf("no plugin called %s", name)
		}
		removePluginParts(l, b)
		l.Plugins = slices.DeleteFunc(l.Plugins, func(x *Plugin) bool { return x.Name == name })
		return nil
	})
	if err == nil {
		os.RemoveAll(pluginDir(name))
	}
	return res, err
}

// UpdatePlugin fetches a plugin again from where it came from and
// replaces its parts, keeping the agents it is given to and whether it is
// on.
func UpdatePlugin(name string) (*Result, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	b := l.plugin(name)
	if b == nil {
		return nil, fmt.Errorf("no plugin called %s", name)
	}
	if b.Source == "" {
		return nil, fmt.Errorf("%s has no source to fetch again", name)
	}
	var names []string
	if b.Pick != "" {
		names = []string{b.Pick}
	}
	p, cleanup, err := pluginProbeFor(b.Source)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return installPlugins(p, b.Source, names, nil)
}

// ---- the page --------------------------------------------------------------

// PluginView is a plugin as the page shows it.
type PluginView struct {
	*Plugin
	// Missing are parts the user took out one by one since
	Missing []string `json:"missing,omitempty"`
	Dir     string   `json:"dir"`
}

func (l *Library) pluginsView() []PluginView {
	out := []PluginView{}
	for _, b := range l.Plugins {
		v := PluginView{Plugin: b, Dir: pluginDir(b.Name)}
		// the parts the user took out one by one aren't listed as its
		c := *b
		c.Skills = slices.DeleteFunc(slices.Clone(b.Skills), func(n string) bool {
			s := l.skill(n)
			gone := s == nil || s.Bundle != b.Name
			if gone {
				v.Missing = append(v.Missing, "skill "+n)
			}
			return gone
		})
		c.MCP = slices.DeleteFunc(slices.Clone(b.MCP), func(n string) bool {
			s := l.server(n)
			gone := s == nil || s.Bundle != b.Name
			if gone {
				v.Missing = append(v.Missing, "MCP server "+n)
			}
			return gone
		})
		c.Hooks = slices.DeleteFunc(slices.Clone(b.Hooks), func(n string) bool {
			h := l.hook(n)
			gone := h == nil || h.Bundle != b.Name
			if gone {
				v.Missing = append(v.Missing, "hook "+n)
			}
			return gone
		})
		c.Subagents = slices.DeleteFunc(slices.Clone(b.Subagents), func(n string) bool {
			s := l.subagent(n)
			gone := s == nil || s.Bundle != b.Name
			if gone {
				v.Missing = append(v.Missing, "subagent "+n)
			}
			return gone
		})
		v.Plugin = &c
		out = append(out, v)
	}
	return out
}
