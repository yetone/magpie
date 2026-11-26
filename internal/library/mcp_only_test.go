package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// zysicyj on X (Windows): ticking agents on a server in Library → MCP and
// pressing Save said "Saving…" for about a minute. A change to the servers
// gave every agent its skills again too, through every skill folder and
// every copy's files, and the page was read again whole, skills and all:
// seconds a Save with many skills and agents, more on Windows. A change
// to the servers alone writes the agents' MCP files, and its answer reads
// the servers' part of the page: no skill folder is looked at, in the
// agents or the library.
func TestServerChangeLooksAtNoSkill(t *testing.T) {
	h := sandbox(t)
	skillsGiven(t, h, "zysk-a", "zysk-b", "zysk-c", "zysk-d")
	var all []string
	for _, tg := range Targets() {
		all = append(all, tg.Agent.ID)
	}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"codex"}}))
	codex := filepath.Join(h, ".codex/config.toml")
	claude := filepath.Join(h, ".claude.json")

	looks := countLooks(t)
	skillLooks := func(step string) {
		t.Helper()
		var at []string
		for p := range looks() {
			if strings.Contains(p, "zysk-") {
				at = append(at, p)
			}
		}
		if len(at) > 0 {
			slices.Sort(at)
			t.Fatalf("%s looked at %d skill paths, %s first", step, len(at), at[0])
		}
	}
	answer := func(step string, res *Result) {
		t.Helper()
		if !res.MCPOnly() {
			t.Fatalf("%s wrote more than the MCP files", step)
		}
		v, err := ReadMCP(res.Standing(nil))
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Agents) != len(all) || len(v.Skills) != 0 || v.Instructions != nil {
			t.Fatalf("%s's answer: %d agents of %d, %d skills, instructions %v", step, len(v.Agents), len(all), len(v.Skills), v.Instructions != nil)
		}
		skillLooks(step)
	}

	res := ok(t)(ServerAgents("fs", all))
	answer("ticking every agent", res)
	if servers(t, claude, "mcpServers")["fs"] == nil || !strings.Contains(read(t, codex), "[mcp_servers.fs]") {
		t.Fatalf("not given to every agent:\n%s\n%s", read(t, claude), read(t, codex))
	}
	answer("saving it", ok(t)(SaveServer("fs", Server{Name: "fs", Transport: "stdio", Command: "uvx", Agents: all})))
	if s, _ := servers(t, claude, "mcpServers")["fs"].(map[string]any); s["command"] != "uvx" {
		t.Fatalf("claude's fs after the save: %v", s)
	}
	answer("ticking it off codex", ok(t)(ServerAgents("fs", slices.DeleteFunc(slices.Clone(all), func(id string) bool { return id == "codex" }))))
	if strings.Contains(read(t, codex), "[mcp_servers.fs]") {
		t.Fatalf("still in codex's file:\n%s", read(t, codex))
	}
	answer("all off", ok(t)(SomeServersAgents([]string{"fs"}, all, false)))
	answer("turning it off", ok(t)(ServerOn("fs", false)))
	answer("on again", ok(t)(ServerOn("fs", true)))
	answer("removing it", ok(t)(RemoveServer("fs")))
	if servers(t, claude, "mcpServers")["fs"] != nil {
		t.Fatalf("still in claude's file:\n%s", read(t, claude))
	}
	// the skills are as they were, given to every agent
	for _, n := range []string{"zysk-a", "zysk-d"} {
		if !exists(filepath.Join(h, ".claude/skills", n, "SKILL.md")) {
			t.Fatalf("claude lost %s", n)
		}
	}
	// a skill change still reads them
	ok(t)(SkillAgents("zysk-a", []string{"claude"}))
	if !slices.ContainsFunc(keysOf(looks()), func(p string) bool { return strings.Contains(p, "zysk-") }) {
		t.Fatal("the counter sees nothing: a skill change looked at no skill path")
	}
}

// A change through changeMCP that changes more than the servers, or a
// server a subagent's file names, is given to the agents by the whole sync.
func TestServerChangeBeyondMCPSyncsWhole(t *testing.T) {
	sandbox(t)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"claude"}}))
	res, err := changeMCP(func(l *Library) error {
		l.server("fs").Agents = []string{"claude", "codex"}
		l.CopySkills = !l.CopySkills
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.MCPOnly() {
		t.Fatal("a change to the skills' way was given as the servers' alone")
	}
	if res := ok(t)(ServerAgents("fs", []string{"claude"})); !res.MCPOnly() {
		t.Fatal("a server ticked was given by the whole sync")
	}
	sub := reviewer
	sub.MCP, sub.Skills, sub.Agents = []string{"fs"}, nil, []string{"claude"}
	ok(t)(SaveSubagent("", sub))
	if res := ok(t)(ServerOn("fs", false)); res.MCPOnly() {
		t.Fatal("a server a subagent names, turned off, was given as the servers' alone")
	}
}

// The problems a change to the servers alone leaves standing: its own,
// and the others' as the last whole change found them.
func TestServerChangeKeepsOtherProblems(t *testing.T) {
	was := []Problem{{Agent: "claude", What: "skill:a", Error: "x"}, {Agent: "claude", What: "mcp:fs", Error: "y"},
		{Agent: "codex", What: "mcp", Error: "z"}, {Agent: "wsl:x", What: "wsl", Error: "asleep"}, {Agent: "claude", What: "project:/p", Error: "w"},
		{Agent: "codex", What: "instructions", Error: "v"}}
	now := []Problem{{Agent: "codex", What: "mcp:gs", Error: "u"}}
	got := (&Result{Problems: now, mcpOnly: true}).Standing(was)
	want := []Problem{was[0], was[5], now[0]}
	if !slices.Equal(got, want) {
		t.Fatalf("standing after a servers' change: %v, want %v", got, want)
	}
	if got := (&Result{Problems: now}).Standing(was); !slices.Equal(got, now) {
		t.Fatalf("standing after a whole change: %v", got)
	}
}
