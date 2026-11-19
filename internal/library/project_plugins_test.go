package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pluginProjects is a sandbox with the acme-kit plugin in the library,
// given to no agent user-wide, and two projects.
func pluginProjects(t *testing.T) (h, a, b string) {
	t.Helper()
	h = sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "acme")
	writePlugin(t, src)
	ok(t)(InstallPlugins(src, nil, []string{}))
	a, b = filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	for _, d := range []string{a, b} {
		gitInit(t, d)
		ok(t)(AddProject(d))
	}
	return
}

func there(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s: %v", path, err)
	}
}

// A plugin given to a project (jinpwng on X) puts each of its parts where
// each agent reads a project's own, in that agent's shape, and nowhere
// else; taken out, all of it comes out again, the files magpie made too.
func TestProjectPluginEachPartInTheProject(t *testing.T) {
	h, a, b := pluginProjects(t)
	res := ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude", "codex", "pi"}))
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	for _, rel := range []string{".claude/skills/tdd", ".agents/skills/tdd", ".claude/agents/reviewer.md", ".codex/agents/reviewer.toml"} {
		there(t, filepath.Join(a, rel))
	}
	for _, rel := range []string{".mcp.json", ".codex/config.toml", ".pi/mcp.json"} {
		if got := read(t, filepath.Join(a, rel)); !strings.Contains(got, "acme-db") {
			t.Errorf("%s lacks the plugin's server:\n%s", rel, got)
		}
	}
	claude := read(t, filepath.Join(a, ".claude/settings.local.json"))
	for _, want := range []string{"echo done", "lint.sh", "compacting", `"Bash"`} {
		if !strings.Contains(claude, want) {
			t.Errorf(".claude/settings.local.json lacks %q:\n%s", want, claude)
		}
	}
	codex := read(t, filepath.Join(a, ".codex/hooks.json"))
	if !strings.Contains(codex, "echo done") || !strings.Contains(codex, "lint.sh") {
		t.Errorf(".codex/hooks.json lacks the plugin's hooks:\n%s", codex)
	}
	if strings.Contains(codex, "compacting") {
		t.Errorf("codex was given Claude Code's own event:\n%s", codex)
	}
	// nothing user-wide, nothing in the other project
	if got := read(t, filepath.Join(h, ".claude/settings.json")); strings.Contains(got, "echo done") {
		t.Errorf("the user-wide settings got the plugin's hooks:\n%s", got)
	}
	for _, p := range []string{filepath.Join(h, ".claude/skills/tdd"), filepath.Join(h, ".codex/hooks.json"), filepath.Join(h, ".claude/agents/reviewer.md")} {
		gone(t, p)
	}
	for _, rel := range []string{".claude", ".codex", ".agents", ".pi", ".mcp.json"} {
		gone(t, filepath.Join(b, rel))
	}
	// kept out of git
	g, _ := gitOf(a)
	ex := read(t, filepath.Join(g.Common, "info", "exclude"))
	for _, rel := range []string{".codex/hooks.json", ".mcp.json", ".claude/skills/tdd"} {
		if !strings.Contains(ex, "/"+rel+"\n") {
			t.Errorf("%s isn't in info/exclude:\n%s", rel, ex)
		}
	}
	v := View0(t)
	if len(v.Projects) != 2 || len(v.Projects[0].Plugins["acme-kit"])+len(v.Projects[1].Plugins["acme-kit"]) != 3 {
		t.Errorf("the page's projects: %+v", v.Projects)
	}

	res = ok(t)(ProjectPlugin(a, "acme-kit", nil))
	if len(res.Problems) > 0 {
		t.Fatalf("problems taking it out: %+v", res.Problems)
	}
	for _, rel := range []string{".claude", ".codex", ".agents", ".pi", ".mcp.json"} {
		gone(t, filepath.Join(a, rel))
	}
}

// Taking a plugin out of a project takes out only what magpie wrote for
// it: the user's own entries and files in the same files stay, one of the
// plugin's the user edited since stays as they left it, and a skill the
// project has of its own stays though the plugin had it too.
func TestProjectPluginWithdrawLeavesTheUsers(t *testing.T) {
	_, a, _ := pluginProjects(t)
	write(t, filepath.Join(a, ".claude/settings.local.json"), `{
  // mine
  "permissions": {"allow": ["Bash(ls)"]},
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo mine"}]}]}
}
`)
	write(t, filepath.Join(a, ".mcp.json"), `{"mcpServers":{"mine":{"command":"mine"}}}`)
	ok(t)(ProjectSkill(a, "tdd", []string{"claude"}))
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude", "codex"}))
	settings := filepath.Join(a, ".claude/settings.local.json")
	got := read(t, settings)
	if !strings.Contains(got, "echo mine") || !strings.Contains(got, "echo done") {
		t.Fatalf("settings.local.json:\n%s", got)
	}
	// the user edits one of the plugin's hooks, and its subagent
	write(t, settings, strings.Replace(got, `"echo done"`, `"echo done && say hi"`, 1))
	rev := filepath.Join(a, ".claude/agents/reviewer.md")
	write(t, rev, read(t, rev)+"\nAlso check the tests.\n")

	res := ok(t)(ProjectPlugin(a, "acme-kit", []string{}))
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	got = read(t, settings)
	for _, want := range []string{"// mine", "Bash(ls)", "echo mine", "echo done && say hi"} {
		if !strings.Contains(got, want) {
			t.Errorf("settings.local.json lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "lint.sh") || strings.Contains(got, "compacting") {
		t.Errorf("the plugin's other hooks stayed:\n%s", got)
	}
	if !strings.Contains(read(t, rev), "Also check the tests.") {
		t.Error("the edited subagent was taken away")
	}
	there(t, filepath.Join(a, ".claude/skills/tdd"))
	gone(t, filepath.Join(a, ".agents/skills/tdd"))
	gone(t, filepath.Join(a, ".codex/agents/reviewer.toml"))
	gone(t, filepath.Join(a, ".codex/hooks.json"))
	mcp := read(t, filepath.Join(a, ".mcp.json"))
	if !strings.Contains(mcp, `"mine"`) || strings.Contains(mcp, "acme-db") {
		t.Errorf(".mcp.json:\n%s", mcp)
	}
	// nothing left of magpie's records of the hooks
	l := loadT(t)
	if p := l.project(a); p == nil || len(p.HookRecs) > 0 || len(p.Plugins) > 0 {
		t.Errorf("records left: %+v", p)
	}
}

// A plugin turned off gives a project nothing while it is off, and one
// removed from the library leaves every project it was in.
func TestProjectPluginFollowsTheLibrary(t *testing.T) {
	_, a, _ := pluginProjects(t)
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude"}))
	there(t, filepath.Join(a, ".claude/settings.local.json"))
	ok(t)(PluginOn("acme-kit", false))
	for _, rel := range []string{".claude", ".mcp.json"} {
		gone(t, filepath.Join(a, rel))
	}
	ok(t)(PluginOn("acme-kit", true))
	there(t, filepath.Join(a, ".claude/skills/tdd"))
	there(t, filepath.Join(a, ".claude/settings.local.json"))
	ok(t)(RemovePlugin("acme-kit"))
	for _, rel := range []string{".claude", ".mcp.json"} {
		gone(t, filepath.Join(a, rel))
	}
	if p := loadT(t).project(a); p == nil || len(p.Plugins) > 0 {
		t.Errorf("the project still has it: %+v", p)
	}
}

// Codex reads a project's hooks only from its own .codex/hooks.json, and
// trusts each by where it is: an entry of the plugin's taken out from
// before one of the user's moves the user's trust with it.
func TestProjectPluginCodexTrustFollows(t *testing.T) {
	h, a, _ := pluginProjects(t)
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"codex"}))
	hooks := filepath.Join(a, ".codex/hooks.json")
	got := read(t, hooks)
	// the user adds a Stop hook of their own after the plugin's, and
	// trusts it
	got = strings.Replace(got, `"command": "echo done"`, `"command": "echo done"}]},{"hooks":[{"type":"command","command":"echo mine"`, 1)
	if !strings.Contains(got, "echo mine") {
		t.Fatalf("couldn't add the user's hook:\n%s", read(t, hooks))
	}
	write(t, hooks, got)
	cfg := filepath.Join(h, ".codex/config.toml")
	write(t, cfg, "[hooks.state.\""+hooks+":stop:1:0\"]\ntrusted_hash = \"sha256:x\"\n")
	ok(t)(ProjectPlugin(a, "acme-kit", nil))
	if got := read(t, hooks); !strings.Contains(got, "echo mine") || strings.Contains(got, "echo done") {
		t.Errorf("hooks.json:\n%s", got)
	}
	if got := read(t, cfg); !strings.Contains(got, hooks+":stop:0:0") {
		t.Errorf("the user's trust didn't move with their hook:\n%s", got)
	}
}

func loadT(t *testing.T) *Library {
	t.Helper()
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// One hook given to a project on its own (#7253): written into each
// chosen agent's project hooks file, beside what the user has there, and
// taken out again with only magpie's entry; a hook leaving the library
// leaves its projects, and a worktree follows.
func TestProjectOneHookPerAgent(t *testing.T) {
	_, a, b := pluginProjects(t)
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "say bark", Agents: []string{}}))
	ok(t)(SaveHook("", Hook{Name: "tick", Event: "turn-end", Command: "say tick", Agents: []string{}}))
	f := filepath.Join(a, ".claude/settings.local.json")
	write(t, f, `{
  // mine
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo mine"}]}]}
}
`)
	if _, err := ProjectHooks(a, []string{"bark"}, []string{"gemini"}); err == nil {
		t.Error("an agent with no project hooks file was taken")
	}
	if _, err := ProjectHooks(a, []string{"nope"}, []string{"claude"}); err == nil {
		t.Error("a hook the library hasn't was taken")
	}
	res := ok(t)(ProjectHooks(a, []string{"bark"}, []string{"claude", "codex"}))
	ok(t)(ProjectHooks(a, []string{"tick"}, []string{"codex"}))
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	claude, codex := read(t, f), read(t, filepath.Join(a, ".codex/hooks.json"))
	if !strings.Contains(claude, "say bark") || !strings.Contains(claude, "echo mine") || !strings.Contains(claude, "// mine") || strings.Contains(claude, "say tick") {
		t.Errorf("claude:\n%s", claude)
	}
	if !strings.Contains(codex, "say bark") || !strings.Contains(codex, "say tick") {
		t.Errorf("codex:\n%s", codex)
	}
	gone(t, filepath.Join(b, ".codex"))
	if v := View0(t); len(v.Projects[0].Hooks["bark"]) != 2 {
		t.Errorf("view: %+v", v.Projects[0].Hooks)
	}
	ok(t)(ProjectHooks(a, []string{"bark"}, []string{"claude", "codex"})) // again: once
	if n := strings.Count(read(t, f), "say bark"); n != 1 {
		t.Errorf("%d entries:\n%s", n, read(t, f))
	}

	ok(t)(ProjectHooks(a, []string{"bark"}, nil))
	if got := read(t, f); strings.Contains(got, "say bark") || !strings.Contains(got, "echo mine") || !strings.Contains(got, "// mine") {
		t.Errorf("after withdrawing:\n%s", got)
	}
	// a hook leaving the library leaves the project
	ok(t)(RemoveHooks([]string{"tick"}))
	gone(t, filepath.Join(a, ".codex/hooks.json"))
	if v := View0(t); len(v.Projects[0].Hooks) != 0 {
		t.Errorf("view after removal: %+v", v.Projects[0].Hooks)
	}
}

// What the project's own files give agents is listed apart from what magpie
// placed, read-only (#7245, #7247): a hook, server, skill and subagent the
// folder had before, and none of magpie's.
func TestProjectOwnResources(t *testing.T) {
	_, a, _ := pluginProjects(t)
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "say bark", Agents: []string{}}))
	write(t, filepath.Join(a, ".claude/settings.json"), `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo shared"}]}]}}`)
	write(t, filepath.Join(a, ".claude/settings.local.json"), `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo mine"}]}]}}`)
	write(t, filepath.Join(a, ".mcp.json"), `{"mcpServers": {"theirs": {"url": "https://x.test/mcp?key=SECRET"}}}`)
	write(t, filepath.Join(a, ".claude/skills/own/SKILL.md"), "---\nname: own\ndescription: Mine.\n---\nbody\n")
	write(t, filepath.Join(a, ".claude/agents/rev.md"), "---\nname: rev\ndescription: Reviews.\n---\nbody\n")
	ok(t)(ProjectHooks(a, []string{"bark"}, []string{"claude"}))
	own := View0(t).Projects[0].Own
	var hooks []string
	for _, r := range own["hooks"] {
		hooks = append(hooks, r.Name)
	}
	if !slices.Equal(hooks, []string{"echo shared", "echo mine"}) {
		t.Errorf("own hooks %v (magpie's say bark must not be one)", hooks)
	}
	if len(own["servers"]) != 1 || own["servers"][0].Name != "theirs" || own["servers"][0].Transport != "http" {
		t.Errorf("own servers %+v", own["servers"])
	}
	if b, _ := json.Marshal(own); strings.Contains(string(b), "SECRET") {
		t.Errorf("a credential went in the view: %s", b)
	}
	if len(own["skills"]) != 1 || own["skills"][0].Name != "own" || own["skills"][0].Description != "Mine." {
		t.Errorf("own skills %+v", own["skills"])
	}
	if len(own["subagents"]) != 1 || own["subagents"][0].Name != "rev" {
		t.Errorf("own subagents %+v", own["subagents"])
	}
}
