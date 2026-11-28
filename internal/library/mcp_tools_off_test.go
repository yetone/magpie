package library

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
)

// handEdit sets key in the server's entry of the agent's file as the user
// would by hand, beside everything else in the entry.
func handEdit(t *testing.T, tg *Target, name string, set map[string]any) {
	t.Helper()
	es, err := tg.MCP.entries()
	if err != nil || es[name] == nil {
		t.Fatalf("%s has no %s: %v", tg.Agent.ID, name, err)
	}
	e := maps.Clone(es[name])
	for k, v := range set {
		e[k] = v
	}
	var o ordered
	for _, k := range slices.Sorted(maps.Keys(e)) {
		o = append(o, kv{k, e[k]})
	}
	if err := tg.MCP.putEntry(&Server{Name: name}, o); err != nil {
		t.Fatal(err)
	}
}

// offIn is the list of tools the agent leaves out in the server's entry,
// as its file has it now.
func offIn(t *testing.T, tg *Target, name string) ([]string, map[string]any) {
	t.Helper()
	es, err := tg.MCP.entries()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := entryTools(es[name], tg.MCP.toolsKey())
	if !ok {
		t.Fatalf("%s: %v isn't a list", tg.Agent.ID, es[name])
	}
	return got, es[name]
}

// nf1qaz2w on Discord: each of a server's tools has a switch, and a tool
// turned off is written into each agent that has its own list of them in
// a server's entry — Codex's disabled_tools, Gemini CLI's and Qwen Code's
// excludeTools, Crush's disabled_tools, Hermes' tools.exclude — beside the names the
// user put there themselves, which stay when the tool is turned on again.
func TestToolsOffInTheEntry(t *testing.T) {
	h := sandbox(t)
	t.Setenv("HERMES_HOME", filepath.Join(h, "hermes-home"))
	write(t, filepath.Join(h, "hermes-home", "config.yaml"), "model:\n  default: x\n")
	holdCLI(t, h, "hermes") // the config alone is gone (#2173, #2177)
	write(t, filepath.Join(h, ".qwen", "settings.json"), `{"model": {"name": "x"}}`)
	agents := []string{"codex", "crush", "gemini", "hermes", "qwen"}
	keys := map[string]string{"codex": "disabled_tools", "crush": "disabled_tools", "gemini": "excludeTools", "hermes": "exclude", "qwen": "excludeTools"}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: agents}))
	for _, id := range agents {
		tg := targetByID(id)
		if tg == nil || toolsOffOf(tg) != toolsInEntry {
			t.Fatalf("%s: no list of tools left out", id)
		}
		if got, _ := offIn(t, tg, "fs"); len(got) != 0 {
			t.Errorf("%s: a list with no tool turned off: %v", id, got)
		}
		if strings.Contains(read(t, tg.MCP.Path), keys[id]) {
			t.Errorf("%s: %s written with none turned off:\n%s", id, keys[id], read(t, tg.MCP.Path))
		}
		// the user's own: one they turned off, one magpie will too, and
		// for Hermes a setting of its own beside exclude
		user := []any{"mine", "write_file"}
		if id == "hermes" {
			handEdit(t, tg, "fs", map[string]any{"tools": map[string]any{"exclude": user, "prompts": false}})
		} else {
			handEdit(t, tg, "fs", map[string]any{keys[id]: user})
		}
	}

	ok(t)(ServerTools("fs", []string{"write_file", " read_file ", "delete", "read_file"}))
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if s := l.server("fs"); !slices.Equal(s.DisabledTools, []string{"delete", "read_file", "write_file"}) {
		t.Errorf("kept: %v", s.DisabledTools)
	}
	for _, id := range agents {
		tg := targetByID(id)
		got, e := offIn(t, tg, "fs")
		if want := []string{"mine", "write_file", "delete", "read_file"}; !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", id, got, want)
		}
		if !strings.Contains(read(t, tg.MCP.Path), keys[id]) {
			t.Errorf("%s: no %s:\n%s", id, keys[id], read(t, tg.MCP.Path))
		}
		if e["command"] != "npx" {
			t.Errorf("%s: the entry lost its command: %v", id, e)
		}
		// magpie's are only those it added: the user's write_file isn't
		if mine := l.applied(id).ToolsOff["fs"]; !slices.Equal(mine, []string{"delete", "read_file"}) {
			t.Errorf("%s: magpie's: %v", id, mine)
		}
	}

	// turned on again: magpie's go, the user's stay
	ok(t)(ServerTools("fs", nil))
	for _, id := range agents {
		tg := targetByID(id)
		got, e := offIn(t, tg, "fs")
		if want := []string{"mine", "write_file"}; !slices.Equal(got, want) {
			t.Errorf("%s: on again: %v, want %v", id, got, want)
		}
		if id == "hermes" {
			if tools, _ := e["tools"].(map[string]any); tools["prompts"] != false {
				t.Errorf("hermes: lost the user's tools.prompts: %v", e)
			}
		}
	}

	// with the user's taken out too, the key goes, and a turned off tool
	// once more comes back alone
	for _, id := range agents {
		tg := targetByID(id)
		if id == "hermes" {
			handEdit(t, tg, "fs", map[string]any{"tools": map[string]any{"prompts": false}})
		} else {
			handEdit(t, tg, "fs", map[string]any{keys[id]: []any{}})
		}
	}
	ok(t)(ServerTools("fs", []string{"x"}))
	ok(t)(ServerTools("fs", nil))
	for _, id := range agents {
		tg := targetByID(id)
		_, e := offIn(t, tg, "fs")
		if id == "hermes" {
			if tools, _ := e["tools"].(map[string]any); tools["exclude"] != nil || tools["prompts"] != false {
				t.Errorf("hermes: %v", e)
			}
		} else if v, has := e[keys[id]]; has && len(v.([]any)) != 0 {
			t.Errorf("%s: %v", id, e)
		}
	}

	// taken from the agent: its entry goes, and what magpie had in it
	ok(t)(ServerTools("fs", []string{"x"}))
	ok(t)(ServerAgents("fs", nil))
	l, _ = load()
	for _, id := range agents {
		if a := l.applied(id); a.ToolsOff != nil || a.MCP != nil {
			t.Errorf("%s: still recorded: %+v", id, a)
		}
	}
}

// claudeDeny is Claude Code's permissions.deny in its settings.json, and
// the rest of permissions.
func claudeDeny(t *testing.T, p string) ([]string, map[string]any) {
	t.Helper()
	var doc struct {
		Permissions map[string]any `json:"permissions"`
	}
	raw := read(t, p)
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal(jsonc.ToJSON([]byte(raw)), &doc); err != nil {
			t.Fatalf("%v:\n%s", err, raw)
		}
	}
	var out []string
	if d, _ := doc.Permissions["deny"].([]any); d != nil {
		for _, x := range d {
			out = append(out, x.(string))
		}
	}
	return out, doc.Permissions
}

// Claude Code leaves a server's tool out by a deny rule naming it,
// mcp__<server>__<tool> with each character it doesn't keep an _, in its
// settings.json (code.claude.com/docs/en/permissions); the user's own rules,
// and their own one for a tool magpie turns off too, stay.
func TestToolsOffClaudeDeny(t *testing.T) {
	h := sandbox(t)
	settings := filepath.Join(h, ".claude", "settings.json")
	write(t, settings, `{
  // mine
  "model": "opus",
  "permissions": {"allow": ["Bash(ls:*)"], "deny": ["Bash(rm:*)", "mcp__web-1__fetch"]}
}`)
	tg := targetByID("claude")
	if tg == nil || tg.Settings != settings || toolsOffOf(tg) != toolsClaudeDeny {
		t.Fatalf("claude: %+v", tg)
	}
	ok(t)(SaveServer("", Server{Name: "web-1", Transport: "http", URL: "https://example.com/mcp", Agents: []string{"claude"}}))
	if got := read(t, settings); !strings.Contains(got, "// mine") {
		t.Errorf("settings written with no tool off:\n%s", got)
	}
	ok(t)(ServerTools("web-1", []string{"fetch", "read.page", "search"}))
	deny, perms := claudeDeny(t, settings)
	if want := []string{"Bash(rm:*)", "mcp__web-1__fetch", "mcp__web-1__read_page", "mcp__web-1__search"}; !slices.Equal(deny, want) {
		t.Errorf("deny: %v, want %v", deny, want)
	}
	if a, _ := perms["allow"].([]any); len(a) != 1 {
		t.Errorf("allow: %v", perms)
	}
	if got := read(t, settings); !strings.Contains(got, "// mine") || !strings.Contains(got, `"model": "opus"`) {
		t.Errorf("lost the user's settings:\n%s", got)
	}
	l, _ := load()
	if r := l.applied("claude").ToolRules; !slices.Equal(r, []string{"mcp__web-1__read_page", "mcp__web-1__search"}) {
		t.Errorf("magpie's: %v", r)
	}

	ok(t)(ServerTools("web-1", []string{"search"}))
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", "mcp__web-1__fetch", "mcp__web-1__search"}) {
		t.Errorf("one on again: %v", deny)
	}
	ok(t)(ServerTools("web-1", nil))
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"Bash(rm:*)", "mcp__web-1__fetch"}) {
		t.Errorf("on again: %v", deny)
	}

	// a settings.json with nothing of the user's in permissions: it goes
	// with magpie's last rule, and a server taken from Claude takes its
	// rules too
	write(t, settings, `{"model": "opus"}`)
	ok(t)(ServerTools("web-1", []string{"fetch"}))
	if deny, _ := claudeDeny(t, settings); !slices.Equal(deny, []string{"mcp__web-1__fetch"}) {
		t.Errorf("deny: %v", deny)
	}
	ok(t)(ServerAgents("web-1", nil))
	if got := read(t, settings); strings.Contains(got, "permissions") || !strings.Contains(got, "opus") {
		t.Errorf("after taken away:\n%s", got)
	}
	l, _ = load()
	if r := l.applied("claude").ToolRules; r != nil {
		t.Errorf("still recorded: %v", r)
	}

	// a deny that isn't a list is the user's, never taken as none
	write(t, settings, `{"permissions": {"deny": "oops"}}`)
	r, err := ServerAgents("web-1", []string{"claude"})
	if err != nil || len(r.Problems) == 0 {
		t.Errorf("no problem for a deny that isn't a list: %+v %v", r, err)
	}
	if got := read(t, settings); got != `{"permissions": {"deny": "oops"}}` {
		t.Errorf("written over:\n%s", got)
	}
}

// OpenCode leaves a tool out by its tools map, <server>_<tool>: false in
// opencode.json (opencode.ai/docs/mcp-servers, "Manage"); a key the user
// set, to either, is theirs.
func TestToolsOffOpenCode(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".config", "opencode", "opencode.json")
	write(t, p, `{"tools": {"write": false, "gh_search": true}}`)
	tg := targetByID("opencode")
	if tg == nil || tg.MCP.Path != p || toolsOffOf(tg) != toolsOpenCode {
		t.Fatalf("opencode: %+v", tg)
	}
	ok(t)(SaveServer("", Server{Name: "gh", Transport: "stdio", Command: "gh-mcp", Agents: []string{"opencode"}, DisabledTools: []string{"create.issue", "search"}}))
	tools := func() map[string]any {
		var doc struct {
			Tools map[string]any `json:"tools"`
		}
		if err := json.Unmarshal(jsonc.ToJSON([]byte(read(t, p))), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Tools
	}
	if got, want := tools(), map[string]any{"write": false, "gh_search": true, "gh_create_issue": false}; !maps.Equal(got, want) {
		t.Errorf("tools: %v, want %v", got, want)
	}
	if got := read(t, p); !strings.Contains(got, `"gh-mcp"`) {
		t.Errorf("no server:\n%s", got)
	}
	ok(t)(ServerTools("gh", nil))
	if got, want := tools(), map[string]any{"write": false, "gh_search": true}; !maps.Equal(got, want) {
		t.Errorf("on again: %v, want %v", got, want)
	}

	// a key magpie set that the user then turned on is theirs from then
	ok(t)(ServerTools("gh", []string{"a"}))
	if got := tools(); got["gh_a"] != false {
		t.Fatalf("not off: %v", got)
	}
	if err := edit.SetJSON(p, edit.KV{Path: "tools.gh_a", Value: true}); err != nil {
		t.Fatal(err)
	}
	ok(t)(ServerTools("gh", nil))
	if got := tools(); got["gh_a"] != true {
		t.Errorf("the user's true taken: %v", got)
	}

	// with nothing of the user's left, tools goes with magpie's last
	write(t, p, `{}`)
	ok(t)(ServerTools("gh", []string{"a"}))
	ok(t)(ServerTools("gh", nil))
	if got := read(t, p); strings.Contains(got, `"tools"`) {
		t.Errorf("tools left:\n%s", got)
	}
}

// An agent with no way to leave out a single tool gets the server whole,
// with nothing written for the tools turned off, and the page is told it
// can't.
func TestToolsOffNotPretended(t *testing.T) {
	h := sandbox(t)
	agents := []string{"codex", "cursor"}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: agents, DisabledTools: []string{"write_file"}}))
	cursor := read(t, filepath.Join(h, ".cursor", "mcp.json"))
	if !strings.Contains(cursor, `"npx"`) || strings.Contains(cursor, "write_file") {
		t.Errorf("cursor:\n%s", cursor)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	can := map[string]bool{}
	for _, a := range v.Agents {
		can[a.ID] = a.ToolsOff
	}
	if !can["codex"] || !can["claude"] || !can["gemini"] || !can["opencode"] || !can["crush"] {
		t.Errorf("not told these can: %v", can)
	}
	for _, id := range []string{"cursor", "copilot", "goose", "pi", "mimocode"} {
		if _, listed := can[id]; listed && can[id] {
			t.Errorf("%s said to leave out single tools", id)
		}
	}

	// the editor's save, which sends no tools, keeps those turned off
	ok(t)(SaveServer("fs", Server{Name: "fs", Transport: "stdio", Command: "uvx", Agents: agents}))
	l, _ := load()
	if s := l.server("fs"); !slices.Equal(s.DisabledTools, []string{"write_file"}) {
		t.Errorf("the save dropped them: %v", s.DisabledTools)
	}
	if _, err := ServerTools("fs", []string{"a\nb"}); err == nil {
		t.Error("a name with a line break kept")
	}
	if _, err := ServerTools("nope", []string{"a"}); err == nil {
		t.Error("no error for a server the library hasn't")
	}
}

func TestOffList(t *testing.T) {
	for _, c := range []struct {
		cur, prev, want, list, mine []string
	}{
		{nil, nil, []string{"a"}, []string{"a"}, []string{"a"}},
		{[]string{"u"}, nil, []string{"a"}, []string{"u", "a"}, []string{"a"}},
		{[]string{"u", "a"}, []string{"a"}, nil, []string{"u"}, nil},
		{[]string{"a"}, nil, []string{"a"}, []string{"a"}, nil},                          // the user's own
		{[]string{"a"}, nil, nil, []string{"a"}, nil},                                    // stays
		{[]string{"u"}, []string{"a"}, []string{"a"}, []string{"u", "a"}, []string{"a"}}, // taken out by hand: back
	} {
		list, mine := offList(c.cur, c.prev, c.want)
		if !slices.Equal(list, c.list) || !slices.Equal(mine, c.mine) {
			t.Errorf("offList(%v, %v, %v) = %v, %v; want %v, %v", c.cur, c.prev, c.want, list, mine, c.list, c.mine)
		}
	}
}
