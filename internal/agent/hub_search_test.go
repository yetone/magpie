package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// hubSearchHome is a sandbox home whose Claude Code has settings.json,
// managed settings and .claude.json as given ("" for none), the shell
// saying nothing, and nothing HubSearchOff read before.
func hubSearchHome(t *testing.T, settings, managed, global string) (home, path string) {
	t.Helper()
	home, path, _ = claudeCloudHome(t, settings)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for f, s := range map[string]string{claudeManaged(): managed, filepath.Join(home, ".claude.json"): global} {
		if s != "" {
			writeFile(t, f, s)
		}
	}
	if settings == "" {
		os.Remove(path)
	}
	hubSearchForget()
	t.Cleanup(hubSearchForget)
	return home, path
}

// jjaychen1e on X (#7349): a Claude Code whose user set
// ENABLE_TOOL_SEARCH=false in its settings sends every MCP tool whole on
// every request, so the router gives it find_tools and call_tool
// (gateway.HubSearchOff). Read as Claude Code reads it: managed settings
// over settings.json over .claude.json, off for 0, false, no, off and
// auto:N of 100 or more, and off too with experimental betas off unless managed
// settings force it. Unset, true, auto and force search.
func TestHubSearchOffReadsClaudeSettings(t *testing.T) {
	for _, c := range []struct {
		name, settings, managed, global string
		off                             bool
	}{
		{"none", "", "", "", false},
		{"no env", `{"model":"opus"}`, "", "", false},
		{"false", `{"env":{"ENABLE_TOOL_SEARCH":"false"}}`, "", "", true},
		{"json false", `{"env":{"ENABLE_TOOL_SEARCH":false}}`, "", "", true},
		{"0", `{"env":{"ENABLE_TOOL_SEARCH":"0"}}`, "", "", true},
		{"OFF", `{"env":{"ENABLE_TOOL_SEARCH":" OFF "}}`, "", "", true},
		{"no", `{"env":{"ENABLE_TOOL_SEARCH":"no"}}`, "", "", true},
		{"auto:100", `{"env":{"ENABLE_TOOL_SEARCH":"auto:100"}}`, "", "", true},
		{"magpie's true", `{"env":{"ENABLE_TOOL_SEARCH":"true"}}`, "", "", false},
		{"1", `{"env":{"ENABLE_TOOL_SEARCH":"1"}}`, "", "", false},
		{"auto", `{"env":{"ENABLE_TOOL_SEARCH":"auto"}}`, "", "", false},
		{"auto:10", `{"env":{"ENABLE_TOOL_SEARCH":"auto:10"}}`, "", "", false},
		{"auto:150", `{"env":{"ENABLE_TOOL_SEARCH":"auto:150"}}`, "", "", true},
		{"auto:x", `{"env":{"ENABLE_TOOL_SEARCH":"auto:x"}}`, "", "", false},
		{"empty", `{"env":{"ENABLE_TOOL_SEARCH":""}}`, "", "", false},
		{"global false", `{}`, "", `{"env":{"ENABLE_TOOL_SEARCH":"false"}}`, true},
		{"settings over global", `{"env":{"ENABLE_TOOL_SEARCH":"true"}}`, "", `{"env":{"ENABLE_TOOL_SEARCH":"false"}}`, false},
		{"managed false", `{"env":{"ENABLE_TOOL_SEARCH":"true"}}`, `{"env":{"ENABLE_TOOL_SEARCH":"false"}}`, "", true},
		{"managed over settings", `{"env":{"ENABLE_TOOL_SEARCH":"false"}}`, `{"env":{"ENABLE_TOOL_SEARCH":"true"}}`, "", false},
		{"betas off", `{"env":{"ENABLE_TOOL_SEARCH":"true","CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS":"1"}}`, "", "", true},
		{"betas on", `{"env":{"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS":"0"}}`, "", "", false},
		{"betas off, force in settings", `{"env":{"ENABLE_TOOL_SEARCH":"force","CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS":"true"}}`, "", "", true},
		{"betas off, managed force", `{"env":{"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS":"true"}}`, `{"env":{"ENABLE_TOOL_SEARCH":"force"}}`, "", false},
		{"unreadable", `{"env":{"ENABLE_TOOL_SEARCH":"false"`, "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			hubSearchHome(t, c.settings, c.managed, c.global)
			if got := HubSearchOff("claude"); got != c.off {
				t.Fatalf("HubSearchOff(claude) = %v, want %v", got, c.off)
			}
			// a project's address is cut by the gateway; codex isn't read
			if HubSearchOff("codex") {
				t.Fatal("codex's search off")
			}
		})
	}
}

// The folder CLAUDE_CONFIG_DIR names is the claude row's, and every other
// Claude Code config folder is claude#<name> (claudeDirAgents): each is
// read in its own folder, with its own .claude.json.
func TestHubSearchOffReadsEachConfigFolder(t *testing.T) {
	home, _ := hubSearchHome(t, `{"env":{"ENABLE_TOOL_SEARCH":"true"}}`, "", "")
	work := filepath.Join(home, ".claude-work")
	writeFile(t, filepath.Join(work, "settings.json"), `{"env":{"ENABLE_TOOL_SEARCH":"false"}}`)
	solo := filepath.Join(home, ".claude-solo")
	writeFile(t, filepath.Join(solo, "settings.json"), `{}`)
	writeFile(t, filepath.Join(solo, ".claude.json"), `{"env":{"ENABLE_TOOL_SEARCH":"0"}}`)
	for id, want := range map[string]bool{"claude": false, "claude#work": true, "claude#solo": true, "claude#gone": false} {
		if got := HubSearchOff(id); got != want {
			t.Errorf("HubSearchOff(%s) = %v, want %v", id, got, want)
		}
	}

	// CLAUDE_CONFIG_DIR on .claude-work: that folder is claude's, and the
	// default one is claude#default
	t.Setenv("CLAUDE_CONFIG_DIR", work)
	hubSearchForget()
	for id, want := range map[string]bool{"claude": true, "claude#default": false} {
		if got := HubSearchOff(id); got != want {
			t.Errorf("CLAUDE_CONFIG_DIR: HubSearchOff(%s) = %v, want %v", id, got, want)
		}
	}
}

// The router asks on every tools/list: what was read is kept for
// hubSearchFor, then read again.
func TestHubSearchOffKeptAWhile(t *testing.T) {
	_, path := hubSearchHome(t, `{"env":{"ENABLE_TOOL_SEARCH":"false"}}`, "", "")
	if !HubSearchOff("claude") {
		t.Fatal("not off")
	}
	writeFile(t, path, `{"env":{"ENABLE_TOOL_SEARCH":"true"}}`)
	if !HubSearchOff("claude") {
		t.Fatal("read again at once")
	}
	hubSearch.Lock()
	s := hubSearch.seen["claude"]
	s.at = s.at.Add(-hubSearchFor)
	hubSearch.seen["claude"] = s
	hubSearch.Unlock()
	if HubSearchOff("claude") {
		t.Fatal("not read again after hubSearchFor")
	}
}

// Connecting Claude Code to magpie writes ENABLE_TOOL_SEARCH=true, which
// keeps its own search and every tool whole; the user's own false is
// left as theirs, and with it the router's tools on demand.
func TestHubSearchOffAfterConnect(t *testing.T) {
	home, path := hubSearchHome(t, `{}`, "", "")
	a := claude(home)
	if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "env."+claudeSearchEnv); v != "true" || HubSearchOff("claude") {
		t.Fatalf("magpie's %q read as off:\n%s", v, readFile(path))
	}

	home, path = hubSearchHome(t, `{"env":{"ENABLE_TOOL_SEARCH":"false"}}`, "", "")
	a = claude(home)
	if err := a.Apply("model", "opencode-go/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "env."+claudeSearchEnv); v != "false" || !HubSearchOff("claude") {
		t.Fatalf("the user's false (now %q) not read as off:\n%s", v, readFile(path))
	}
}

// Claude Desktop starts its Code tab's Claude Code with experimental
// betas off, so with tool search off, while toolSearchEnabled isn't on in
// the profile it applies (desktopSeedToolSearch): magpie seeds it on once,
// and the user can turn it off in Desktop's window. Off, or taken away,
// Desktop is given its tools on demand; a Desktop not on magpie, or a
// profile that can't be read, keeps every tool.
func TestHubSearchOffReadsDesktopPolicy(t *testing.T) {
	home, p := desktopSandbox(t)
	hubSearchForget()
	t.Cleanup(hubSearchForget)
	off := func(when string, want bool) {
		t.Helper()
		hubSearchForget()
		if got := HubSearchOff("claude-desktop"); got != want {
			t.Fatalf("%s: HubSearchOff(claude-desktop) = %v, want %v", when, got, want)
		}
	}
	off("not on magpie", false)
	a := claudeDesktop(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	off("on magpie, seeded on", false)
	if err := edit.SetJSON(p.prof, edit.KV{Path: "toolSearchEnabled", Value: false}); err != nil {
		t.Fatal(err)
	}
	off("turned off in Desktop", true)
	if err := edit.DelJSON(p.prof, "toolSearchEnabled"); err != nil {
		t.Fatal(err)
	}
	off("taken away", true)
	b, _ := os.ReadFile(p.prof)
	writeFile(t, p.prof, string(b[:len(b)/2]))
	off("unreadable", false)
	writeFile(t, p.prof, string(b))
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	off("off magpie", false)
}
