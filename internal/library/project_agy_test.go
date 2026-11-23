package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// PStack at 36b090e3aa896543a4855da461c4fb48becc0caa (#7507), byte for
// byte: Claude Code's hooks/hooks.json, Antigravity's own hooks.json at
// the plugin's root, its .antigravity-plugin manifest, and a subagent.
const (
	pstackClaudeHooks = `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume|clear|compact",
        "hooks": [
          {
            "type": "command",
            "command": "\"${CLAUDE_PLUGIN_ROOT}/hooks/session-start.sh\" claude",
            "statusMessage": "Loading pstack routing"
          }
        ]
      }
    ]
  }
}
`
	pstackAgyHooks = `{
  "pstack-routing": {
    "SessionStart": [
      {
        "type": "command",
        "command": "./hooks/session-start.sh agy"
      }
    ]
  }
}
`
	pstackAgyManifest = `{
  "name": "pstack",
  "version": "0.9.151",
  "description": "If you want to go fast, go deep first. pstack helps you write less, but higher quality code. rigorous agent workflows you can parallelize with confidence. Ported from cursor/plugins/pstack to Antigravity. Original pstack by Lauren Tan (poteto).",
  "author": "Michael Denyer",
  "logo": "assets/pstack-icon.png",
  "repository": "https://github.com/michael-denyer/pstack-claude"
}
`
	pstackAgent = "---\nname: poteto-agent\ndescription: Delegate subagent for code-writing and helper tasks spawned during a `/poteto-mode` run. Use for delegates, not as the runner for `/poteto-mode` itself.\n---\n\n# Poteto subagent\n\nYou are operating as poteto-mode's full agent style.\n"
)

// agyProject is a sandbox with PStack in the library, given to no agent
// (agents=none), and a project.
func agyProject(t *testing.T) (root, a string) {
	t.Helper()
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "pstack")
	write(t, filepath.Join(src, ".antigravity-plugin/plugin.json"), pstackAgyManifest)
	write(t, filepath.Join(src, "hooks/hooks.json"), pstackClaudeHooks)
	write(t, filepath.Join(src, "hooks.json"), pstackAgyHooks)
	write(t, filepath.Join(src, "hooks/session-start.sh"), "#!/bin/sh\necho '{}'\n")
	write(t, filepath.Join(src, "agents/poteto-agent.md"), pstackAgent)
	ok(t)(InstallPlugins(src, nil, []string{}))
	a = filepath.Join(t.TempDir(), "a")
	gitInit(t, a)
	ok(t)(AddProject(a))
	return filepath.Join(Dir(), "plugins", "pstack"), a
}

func libHook(t *testing.T, name string) *Hook {
	t.Helper()
	h := loadT(t).hook(name)
	if h == nil {
		t.Fatalf("no hook %s", name)
	}
	return h
}

// #7507: PStack's Antigravity hooks.json becomes a hook of Antigravity's
// own, run in the plugin's folder as agy runs a plugin's, and Claude
// Code's SessionStart, narrowed to its own starts, stays Claude Code's.
func TestAgyPluginHooksImported(t *testing.T) {
	root, _ := agyProject(t)
	claude, agy := libHook(t, "pstack-1"), libHook(t, "pstack-2")
	if claude.Event != "claude:SessionStart" {
		t.Errorf("pstack-1: %+v", claude)
	}
	if agy.Event != "agy:SessionStart" || agy.Matcher != "" {
		t.Errorf("pstack-2: %+v", agy)
	}
	want := "cd '" + root + "' && ./hooks/session-start.sh agy"
	if cmdOS == "windows" {
		want = `cd /d "` + root + `" && ./hooks/session-start.sh agy`
	}
	if agy.Command != want {
		t.Errorf("pstack-2's command:\n got %s\nwant %s", agy.Command, want)
	}
	// never given to any agent's own (user-wide) hooks
	if hookTakes(agy, "agy") || hookTakes(agy, "claude") {
		t.Error("Antigravity's own hook is given user-wide")
	}
}

// The reporter's steps: Claude Code's hook is refused for Antigravity with
// why, Antigravity's goes into .agents/hooks.json under magpie's own name
// beside the user's, once however often it is given and by whatever route
// (the hook, the plugin, the CLI and 2.0 both), and taking it out leaves
// the file as the user had it.
func TestAgyProjectHooksBesideTheUsers(t *testing.T) {
	_, a := agyProject(t)
	f := filepath.Join(a, ".agents/hooks.json")
	mine := `{
  "mine": {
    "SessionStart": [{"type": "command", "command": "echo mine"}]
  }
}
`
	write(t, f, mine)
	_, err := ProjectHooks(a, []string{"pstack-1"}, []string{"agy"})
	if err == nil || !strings.Contains(err.Error(), "pstack-1") {
		t.Errorf("Claude Code's SessionStart was given to Antigravity: %v", err)
	}
	for _, step := range []func() (*Result, error){
		func() (*Result, error) { return ProjectHooks(a, []string{"pstack-2"}, []string{"agy"}) },
		func() (*Result, error) { return ProjectHooks(a, []string{"pstack-2"}, []string{"agy"}) },
		func() (*Result, error) { return ProjectPlugin(a, "pstack", []string{"agy"}) },
		func() (*Result, error) { return ProjectHooks(a, []string{"pstack-2"}, []string{"agy", "antigravity"}) },
	} {
		res := ok(t)(step())
		if len(res.Problems) > 0 {
			t.Fatalf("problems: %+v", res.Problems)
		}
		got := read(t, f)
		if n := strings.Count(got, "session-start.sh agy"); n != 1 {
			t.Fatalf("%d entries:\n%s", n, got)
		}
		if gjson.Get(got, "mine.SessionStart.0.command").String() != "echo mine" {
			t.Fatalf("the user's hook is gone:\n%s", got)
		}
		e := gjson.Get(got, "magpie.SessionStart.0")
		if e.Get("type").String() != "command" || !strings.HasSuffix(e.Get("command").String(), "./hooks/session-start.sh agy") {
			t.Fatalf("magpie's entry:\n%s", got)
		}
	}
	if strings.Contains(read(t, f), "session-start.sh\\\" claude") {
		t.Errorf("Claude Code's hook was written for Antigravity:\n%s", read(t, f))
	}
	ok(t)(ProjectHooks(a, []string{"pstack-2"}, nil))
	ok(t)(ProjectPlugin(a, "pstack", nil))
	if got := read(t, f); got != mine {
		t.Errorf("after taking it out:\n%s", got)
	}
}

// One of magpie's entries the user edited is theirs: taking the hook out
// leaves it, and the file magpie made stays for it.
func TestAgyProjectHookEditedStays(t *testing.T) {
	_, a := agyProject(t)
	f := filepath.Join(a, ".agents/hooks.json")
	ok(t)(ProjectHooks(a, []string{"pstack-2"}, []string{"agy"}))
	got := read(t, f)
	write(t, f, strings.Replace(got, `agy"`, `agy --verbose"`, 1))
	ok(t)(ProjectHooks(a, []string{"pstack-2"}, nil))
	if got := read(t, f); !strings.Contains(got, "agy --verbose") {
		t.Errorf("the user's edit was taken out:\n%s", got)
	}
	// taken out with nothing of the user's: the file magpie made goes
	b := filepath.Join(t.TempDir(), "b")
	gitInit(t, b)
	ok(t)(AddProject(b))
	ok(t)(ProjectHooks(b, []string{"pstack-2"}, []string{"antigravity"}))
	there(t, filepath.Join(b, ".agents/hooks.json"))
	ok(t)(ProjectHooks(b, []string{"pstack-2"}, nil))
	gone(t, filepath.Join(b, ".agents/hooks.json"))
}

// A portable hook takes Antigravity's names, and is not written a second
// time when a named hook of the user's runs the same command at the same
// event (Antigravity runs every named hook's); one turned off doesn't
// count. Events and tools Antigravity has no name for are refused.
func TestAgyProjectPortableHooks(t *testing.T) {
	_, a := agyProject(t)
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "say bark", Timeout: 5, Agents: []string{}}))
	ok(t)(SaveHook("", Hook{Name: "lint", Event: "before-tool", Matcher: "shell", Command: "lint", Agents: []string{}}))
	ok(t)(SaveHook("", Hook{Name: "fmt", Event: "after-tool", Matcher: "edit", Command: "fmt", Agents: []string{}}))
	ok(t)(SaveHook("", Hook{Name: "ask", Event: "prompt", Command: "ask", Agents: []string{}}))
	ok(t)(SaveHook("", Hook{Name: "back", Event: "session-start", Matcher: "resume", Command: "back", Agents: []string{}}))
	for _, n := range []string{"fmt", "ask", "back"} {
		if _, err := ProjectHooks(a, []string{n}, []string{"agy"}); err == nil {
			t.Errorf("%s was taken", n)
		}
	}
	f := filepath.Join(a, ".agents/hooks.json")
	write(t, f, `{"mine": {"Stop": [{"type": "command", "command": "say bark", "timeout": 5}]}}`)
	ok(t)(ProjectHooks(a, []string{"bark", "lint"}, []string{"agy"}))
	got := read(t, f)
	if n := strings.Count(got, "say bark"); n != 1 {
		t.Errorf("bark twice:\n%s", got)
	}
	g := gjson.Get(got, "magpie.PreToolUse.0")
	if g.Get("matcher").String() != "run_command" || g.Get("hooks.0.command").String() != "lint" {
		t.Errorf("lint:\n%s", got)
	}
	write(t, f, `{"mine": {"enabled": false, "Stop": [{"type": "command", "command": "say bark", "timeout": 5}]}}`)
	ok(t)(ProjectHooks(a, []string{"bark", "lint"}, []string{"agy"}))
	got = read(t, f)
	if e := gjson.Get(got, "magpie.Stop.0"); e.Get("command").String() != "say bark" || e.Get("timeout").Int() != 5 {
		t.Errorf("bark beside a turned-off hook:\n%s", got)
	}
}

// A subagent goes to .agents/agents in the shape agy's own migration
// guide writes (#7507), once for the CLI and 2.0 both; a name of one of
// Antigravity's own agents is refused.
func TestAgyProjectSubagent(t *testing.T) {
	_, a := agyProject(t)
	res := ok(t)(ProjectSubagents(a, []string{"poteto-agent"}, []string{"agy", "antigravity"}))
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	got := read(t, filepath.Join(a, ".agents/agents/poteto-agent.md"))
	for _, want := range []string{"---\nname: \"poteto-agent\"\n", "\ndescription: \"Delegate subagent", "\nsubagent: true\n", "\nmainAgent: false\n",
		"\ntools: [read_file, edit_file, write_file, run_command, manage_task, search_web]\n", "\n# Poteto subagent\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if _, body, _ := strings.Cut(strings.TrimPrefix(got, "---\n"), "\n---\n"); strings.TrimSpace(body) != "# Poteto subagent\n\nYou are operating as poteto-mode's full agent style." {
		t.Errorf("the prompt: %q", body)
	}
	if strings.Contains(got, "model:") {
		t.Errorf("a model was written:\n%s", got)
	}
	ok(t)(ProjectSubagents(a, []string{"poteto-agent"}, nil))
	gone(t, filepath.Join(a, ".agents/agents/poteto-agent.md"))

	ok(t)(SaveSubagent("", Subagent{Name: "browser", Description: "Mine", Prompt: "x", Agents: []string{}}))
	if _, err := ProjectSubagents(a, []string{"browser"}, []string{"agy"}); err == nil {
		t.Error("a name of Antigravity's own agents was taken")
	}
	ok(t)(ProjectSubagents(a, []string{"browser"}, []string{"claude"}))
	if _, err := os.Stat(filepath.Join(a, ".agents/agents/browser.md")); err == nil {
		t.Error("written for Antigravity")
	}
}

// The user's own named hooks in .agents/hooks.json are listed as the
// project's own, with their events; what magpie wrote there is not.
func TestAgyProjectOwnHooks(t *testing.T) {
	_, a := agyProject(t)
	write(t, filepath.Join(a, ".agents/hooks.json"), `{
  "mine": {"SessionStart": [{"type": "command", "command": "echo mine"}]},
  "lint": {"PreToolUse": [{"matcher": "run_command", "hooks": [{"type": "command", "command": "lint"}]}]}
}`)
	ok(t)(ProjectHooks(a, []string{"pstack-2"}, []string{"agy"}))
	var got []string
	for _, r := range View0(t).Projects[0].Own["hooks"] {
		got = append(got, r.Agent+" "+r.Event+" "+r.Name+" "+r.Path)
	}
	want := []string{"agy PreToolUse lint .agents/hooks.json", "agy SessionStart echo mine .agents/hooks.json"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("own hooks:\n%s", strings.Join(got, "\n"))
	}
}
