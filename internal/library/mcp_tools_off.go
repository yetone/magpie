package library

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
)

// A server's tools can be turned off one by one (nf1qaz2w on Discord): the
// library keeps the names turned off in the server's DisabledTools, and
// writes them into each agent given the server in that agent's own way, for
// the agents that have one. Each writes only what magpie adds, recorded in
// Applied, and takes back only that: a name the user turned off themselves
// stays, and stays theirs.

// toolsOffHow is how an agent is told to leave out single tools of a
// server, or toolsNone for one with no such setting.
type toolsOffHow int

const (
	toolsNone toolsOffHow = iota
	// toolsInEntry: a list in the server's own entry (toolsKey)
	toolsInEntry
	// toolsClaudeDeny: Claude Code's permissions.deny in its settings.json,
	// mcp__<server>__<tool>, a bare name there taking the tool out of
	// Claude's context (code.claude.com/docs/en/permissions, "Tool name
	// wildcards" and MCP)
	toolsClaudeDeny
	// toolsOpenCode: OpenCode's tools map in opencode.json, <server>_<tool>
	// set to false (opencode.ai/docs/mcp-servers "Manage"; the key is
	// packages/core/src/tool/mcp.ts name(), and a tools entry becomes a
	// deny rule in packages/core/src/config/normalize.ts migrateTools,
	// sst/opencode 0d61729b)
	toolsOpenCode
)

// toolsOffOf is how the target's agent leaves out single tools. Only the
// agents themselves, by their id: a plugin's agent or another app that
// shares a format (Droid's mcp.json is Claude Code's shape, MiMo Code's
// OpenCode's) isn't known to read the same setting.
func toolsOffOf(t *Target) toolsOffHow {
	if t == nil || t.MCP == nil || t.Agent == nil || t.Agent.Plugin != "" {
		return toolsNone
	}
	id, _, _ := strings.Cut(t.Agent.ID, "@")
	// another Hermes profile (hermes#coder) reads tools.exclude as the first
	id, _, _ = strings.Cut(id, "#")
	switch {
	case id == "claude" && t.MCP.Format == fmtClaude && t.Settings != "":
		return toolsClaudeDeny
	case id == "opencode" && t.MCP.Format == fmtOpenCode:
		return toolsOpenCode
	case id == "codex" && t.MCP.Format == fmtCodex,
		id == "gemini" && t.MCP.Format == fmtGemini,
		id == "crush" && t.MCP.Format == fmtCrush,
		id == "hermes" && t.MCP.Format == fmtHermes:
		return toolsInEntry
	}
	return toolsNone
}

// toolsKey is the key, in a server's entry, of the tools the agent leaves
// out, each named as the server names it:
//   - Codex: disabled_tools (codex-rs/config/src/mcp_types.rs, applied by
//     ToolFilter in codex-rs/codex-mcp/src/tools.rs, openai/codex cc7ba336)
//   - Gemini CLI: excludeTools (docs/tools/mcp-server.md; mcp-client.ts
//     drops a tool whose name it lists, google-gemini/gemini-cli 9b6e0265)
//   - Crush: disabled_tools (internal/config/config.go MCPConfig,
//     filterTools in internal/agent/tools/mcp/tools.go, charmbracelet/crush
//     df5b024a)
//   - Hermes Agent: tools.exclude, the original names
//     (tools/mcp_tool_registration.py, website/docs/reference/
//     mcp-config-reference.md, NousResearch/hermes-agent c5733167)
func (f *mcpFile) toolsKey() []string {
	switch f.Format {
	case fmtCodex, fmtCrush:
		return []string{"disabled_tools"}
	case fmtGemini:
		return []string{"excludeTools"}
	case fmtHermes:
		return []string{"tools", "exclude"}
	}
	return nil
}

// entryTools is the list under key in an entry, and whether it is one
// magpie can read: a value that isn't a list of names is the user's to
// keep, never taken as none.
func entryTools(old map[string]any, key []string) ([]string, bool) {
	var v any = old
	for _, k := range key {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, v == nil
		}
		v = m[k]
	}
	if v == nil {
		return nil, true
	}
	a, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// offList is the list an agent is given: what it has, less what magpie
// put there last that it no longer wants, then what it wants that isn't
// there. mine are the names magpie added: one the user had put there
// themselves stays theirs, so turning the tool on again leaves it.
func offList(cur, prev, want []string) (list, mine []string) {
	list = []string{}
	for _, x := range cur {
		if !slices.Contains(prev, x) || slices.Contains(want, x) {
			list = append(list, x)
		}
	}
	for _, x := range want {
		if !slices.Contains(list, x) {
			list = append(list, x)
		}
	}
	for _, x := range want {
		if !slices.Contains(cur, x) || slices.Contains(prev, x) {
			mine = append(mine, x)
		}
	}
	return list, mine
}

// toolsEdit is the list of tools left out to write into an entry; an
// empty one takes the key away (magpie's were the last in it).
type toolsEdit struct {
	key  []string
	list []string
}

// apply puts the list into the entry being written, beside the user's
// other keys.
func (e *toolsEdit) apply(o ordered) ordered {
	if e == nil {
		return o
	}
	i := slices.IndexFunc(o, func(x kv) bool { return x.k == e.key[0] })
	if len(e.key) == 1 {
		switch {
		case len(e.list) == 0 && i >= 0:
			return slices.Delete(o, i, i+1)
		case len(e.list) == 0:
			return o
		case i >= 0:
			o[i].v = e.list
			return o
		}
		return append(o, kv{e.key[0], e.list})
	}
	// Hermes' tools.exclude: the rest of tools (include, prompts,
	// resources) is the user's
	m := map[string]any{}
	if i >= 0 {
		old, _ := o[i].v.(map[string]any)
		for k, v := range old {
			m[k] = v
		}
	}
	if len(e.list) == 0 {
		delete(m, e.key[1])
	} else {
		m[e.key[1]] = e.list
	}
	switch {
	case len(m) == 0 && i >= 0:
		return slices.Delete(o, i, i+1)
	case len(m) == 0:
		return o
	case i >= 0:
		o[i].v = m
		return o
	}
	return append(o, kv{e.key[0], m})
}

// entryToolsEdit is what to write into a server's entry for the tools
// the agent leaves out (nil when the entry has them already), and the
// names magpie's own in it then.
func entryToolsEdit(f *mcpFile, old map[string]any, prev, want []string) (*toolsEdit, []string, error) {
	key := f.toolsKey()
	cur, ok := entryTools(old, key)
	if !ok {
		if len(want) == 0 && len(prev) == 0 {
			return nil, nil, nil
		}
		return nil, prev, fmt.Errorf("%s in its entry isn't a list of names, so magpie leaves it as it is", strings.Join(key, "."))
	}
	list, mine := offList(cur, prev, want)
	if slices.Equal(cur, list) {
		return nil, mine, nil
	}
	return &toolsEdit{key: key, list: list}, mine, nil
}

// mcpToolName is a name as Claude Code and OpenCode spell it in a tool's
// id: each character but a letter, a digit, _ and - is an _ (Claude Code
// 2.1.296's normalizeNameForMCP; OpenCode's tool/mcp.ts namespace()).
var mcpNameBad = lazyre.New(`[^a-zA-Z0-9_-]`)

func mcpToolName(s string) string { return mcpNameBad.ReplaceAllString(s, "_") }

// toolRule is the rule that leaves out one tool of a server, in the
// agent's own form.
func toolRule(how toolsOffHow, server, tool string) string {
	if how == toolsClaudeDeny {
		return "mcp__" + mcpToolName(server) + "__" + mcpToolName(tool)
	}
	return mcpToolName(server) + "_" + mcpToolName(tool)
}

// syncToolRules writes the rules, beside the servers, that leave out the
// tools turned off of the servers the agent has (given): Claude Code's
// permissions.deny, OpenCode's tools map.
func (l *Library) syncToolRules(t *Target, how toolsOffHow, given []string, b *backups, res *Result) {
	if res.unreached(t) {
		return
	}
	id := t.Agent.ID
	a := l.applied(id)
	var want []string
	for _, s := range l.MCP {
		if !slices.Contains(given, s.Name) {
			continue
		}
		for _, tool := range s.DisabledTools {
			if r := toolRule(how, s.Name, tool); !slices.Contains(want, r) {
				want = append(want, r)
			}
		}
	}
	if len(want) == 0 && len(a.ToolRules) == 0 {
		return
	}
	path := t.Settings
	if how == toolsOpenCode {
		path = t.MCP.Path
	}
	fail := func(err error) {
		res.fail(id, "mcp", fmt.Errorf("the tools turned off: %w", err))
	}
	raw, err := edit.Read(path)
	if err != nil {
		fail(err)
		return
	}
	doc := map[string]any{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(jsonc.ToJSON(raw), &doc); err != nil {
			fail(fmt.Errorf("%s: %w", path, err))
			return
		}
	}
	write := func(f func() error) bool {
		if err := b.keep(id, path); err != nil {
			fail(err)
			return false
		}
		if err := f(); err != nil {
			fail(err)
			return false
		}
		res.changed(id)
		return true
	}
	if how == toolsClaudeDeny {
		perms, _ := doc["permissions"].(map[string]any)
		cur, ok := entryTools(doc, []string{"permissions", "deny"})
		if !ok {
			fail(fmt.Errorf("%s: permissions.deny isn't a list of rules, so magpie leaves it as it is", path))
			return
		}
		list, mine := offList(cur, a.ToolRules, want)
		if slices.Equal(cur, list) {
			a.ToolRules = mine
			return
		}
		ok = write(func() error {
			if len(list) > 0 {
				return edit.SetJSON(path, edit.KV{Path: "permissions.deny", Value: list})
			}
			// magpie's were the last: the key goes, and permissions too
			// when nothing else is in it
			keys := []string{"permissions.deny"}
			if len(perms) == 1 {
				keys = []string{"permissions"}
			}
			return edit.DelJSON(path, keys...)
		})
		if ok {
			a.ToolRules = mine
		}
		return
	}
	// OpenCode: a key the user set, to anything, is theirs; magpie's are
	// those it set to false, taken away while they still say false
	tools, isMap := doc["tools"].(map[string]any)
	if doc["tools"] != nil && !isMap {
		fail(fmt.Errorf("%s: tools isn't an object, so magpie leaves it as it is", path))
		return
	}
	var set []edit.KV
	var del, mine []string
	for _, k := range a.ToolRules {
		if v, has := tools[k]; has && v == false && !slices.Contains(want, k) {
			del = append(del, "tools."+k)
		}
	}
	for _, k := range want {
		v, has := tools[k]
		switch {
		case !has:
			set = append(set, edit.KV{Path: "tools." + k, Value: false})
			mine = append(mine, k)
		case slices.Contains(a.ToolRules, k) && v == false:
			mine = append(mine, k)
		}
	}
	if len(set) == 0 && len(del) == 0 {
		a.ToolRules = mine
		return
	}
	if len(set) == 0 && len(del) == len(tools) {
		del = []string{"tools"}
	}
	if write(func() error {
		return edit.Atomically(func() error {
			if len(del) > 0 {
				if err := edit.DelJSON(path, del...); err != nil {
					return err
				}
			}
			if len(set) > 0 {
				return edit.SetJSON(path, set...)
			}
			return nil
		}, path)
	}) {
		a.ToolRules = mine
	}
}

// cleanTools is a list of tool names as kept: trimmed, each once, in
// order, none empty, none with a line break, none too long to be a name.
func cleanTools(names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || slices.Contains(out, n) {
			continue
		}
		if strings.ContainsAny(n, "\r\n\x00") || utf8.RuneCountInString(n) > maxToolName {
			return nil, fmt.Errorf("%q isn't a tool's name", n)
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out, nil
}

// ServerTools sets the tools of a server its agents leave out (nf1qaz2w on
// Discord); none turns every tool on again.
func ServerTools(name string, off []string) (*Result, error) {
	off, err := cleanTools(off)
	if err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		s.DisabledTools = off
		return nil
	})
}
