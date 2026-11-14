package library

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// copyTree copies testdata's plugin into a folder of the test's own.
func copyTree(t *testing.T, from string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), filepath.Base(from))
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(from, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, r), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, r), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return to
}

// ponytail's hooks read CLAUDE_PLUGIN_ROOT from Node's process.env, which
// only Claude Code's plugin runtime sets: given as written, each start of
// the agent printed Node's TypeError (@AXmake110 on X). Its hooks.json is
// the published file's bytes.
func TestPluginHookReadsRootFromItsProgram(t *testing.T) {
	node, _ := exec.LookPath("node")
	sh, _ := exec.LookPath("sh")
	h := sandbox(t)
	pluginAgents(t, h)
	src := copyTree(t, filepath.Join("testdata", "ponytail"))

	res := ok(t)(InstallPlugins(src, nil, []string{"claude", "codex"}))
	if len(res.Installed) != 1 || res.Installed[0] != "ponytail" {
		t.Fatalf("installed: %+v", res)
	}
	root := filepath.Join(Dir(), "plugins", "ponytail")
	lit := "'" + filepath.ToSlash(root) + "'"

	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(h, ".claude/settings.json"))), &settings); err != nil {
		t.Fatal(err)
	}
	var start string
	for _, ev := range []string{"SessionStart", "SubagentStart", "UserPromptSubmit"} {
		gs := settings.Hooks[ev]
		if len(gs) != 1 || len(gs[0].Hooks) != 1 {
			t.Fatalf("claude %s: %+v", ev, gs)
		}
		c := gs[0].Hooks[0].Command
		if strings.Contains(c, "CLAUDE_PLUGIN_ROOT") || !strings.Contains(c, "require('path').join("+lit+".split(") {
			t.Errorf("claude %s still reads the variable: %s", ev, c)
		}
		if ev == "SessionStart" {
			start = c
			if gs[0].Matcher != "startup|resume|clear|compact" {
				t.Errorf("matcher: %q", gs[0].Matcher)
			}
		}
	}
	// the prompt hook is portable: Codex runs the same command
	if c := read(t, filepath.Join(h, ".codex/hooks.json")); strings.Contains(c, "CLAUDE_PLUGIN_ROOT") || !strings.Contains(c, "ponytail-mode-tracker.js") {
		t.Errorf("codex hooks:\n%s", c)
	}

	// and it runs, with no CLAUDE_PLUGIN_ROOT in its environment
	if node == "" || sh == "" || runtime.GOOS == "windows" {
		t.Log("no node or sh here: the command isn't run")
		return
	}
	write(t, filepath.Join(root, "hooks", "ponytail-activate.js"), "console.log('ponytail on')\n")
	cmd := exec.Command(sh, "-c", start)
	cmd.Env = []string{"PATH=" + filepath.Dir(node), "HOME=" + h}
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ponytail on" {
		t.Fatalf("the hook as written: %v\n%s", err, out)
	}
}

// What a magpie before this wrote, process.env.CLAUDE_PLUGIN_ROOT as it was,
// is resolved at the next sync, with nothing installed again.
func TestPluginHookWrittenBeforeIsResolved(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := copyTree(t, filepath.Join("testdata", "ponytail"))
	ok(t)(InstallPlugins(src, nil, []string{"claude"}))
	root := filepath.Join(Dir(), "plugins", "ponytail")
	lit := "'" + filepath.ToSlash(root) + "'"

	// as the old magpie left library.json and Claude Code's settings
	for _, f := range []string{path(), filepath.Join(h, ".claude/settings.json")} {
		s := read(t, f)
		if !strings.Contains(s, lit) {
			t.Fatalf("%s lacks %s:\n%s", f, lit, s)
		}
		write(t, f, strings.ReplaceAll(s, lit, "process.env.CLAUDE_PLUGIN_ROOT"))
	}
	ok(t)(Sync())
	s := read(t, filepath.Join(h, ".claude/settings.json"))
	if strings.Contains(s, "CLAUDE_PLUGIN_ROOT") || strings.Count(s, "ponytail-activate.js") != 1 || !strings.Contains(s, lit) {
		t.Errorf("settings after the sync:\n%s", s)
	}
}

func TestPluginEnvRefsEachSpelling(t *testing.T) {
	sandbox(t)
	root, data := filepath.ToSlash(pluginDir("p")), filepath.ToSlash(pluginData("p"))
	nr, nd := pluginDir("p"), pluginData("p")
	for _, c := range []struct{ in, want string }{
		{`node -e "require(process.env.CLAUDE_PLUGIN_ROOT + '/a.js')"`, `node -e "require('` + root + `' + '/a.js')"`},
		{`node -e 'require(process.env.CLAUDE_PLUGIN_ROOT + "/a.js")'`, `node -e 'require("` + root + `" + "/a.js")'`},
		{`node -e "x(process.env['CLAUDE_PLUGIN_DATA'])"`, `node -e "x('` + data + `')"`},
		{`bun -e "x(Bun.env.CLAUDE_PLUGIN_ROOT)"`, `bun -e "x('` + root + `')"`},
		{`deno eval "x(Deno.env.get('CLAUDE_PLUGIN_ROOT'))"`, `deno eval "x('` + root + `')"`},
		{`python3 -c "import os; x(os.environ['CLAUDE_PLUGIN_ROOT'])"`, `python3 -c "import os; x('` + root + `')"`},
		{`python3 -c "import os; x(os.environ.get('CLAUDE_PLUGIN_DATA', '/tmp'))"`, `python3 -c "import os; x('` + data + `')"`},
		{`python3 -c 'import os; x(os.getenv("CLAUDE_PLUGIN_ROOT"))'`, `python3 -c 'import os; x("` + root + `")'`},
		{`ruby -e "x(ENV['CLAUDE_PLUGIN_ROOT'])"`, `ruby -e "x('` + root + `')"`},
		{`& "$env:CLAUDE_PLUGIN_ROOT\a.ps1"`, `& "` + nr + `\a.ps1"`},
		{`& "${env:CLAUDE_PLUGIN_DATA}\a.ps1"`, `& "` + nd + `\a.ps1"`},
		{`"%CLAUDE_PLUGIN_ROOT%\a.cmd"`, `"` + nr + `\a.cmd"`},
		// sh's own spellings are pluginSub's, and a longer name isn't one
		{`"${CLAUDE_PLUGIN_ROOT}"/a.sh $CLAUDE_PLUGIN_DATA`, `"${CLAUDE_PLUGIN_ROOT}"/a.sh $CLAUDE_PLUGIN_DATA`},
		{`node -e "x(process.env.CLAUDE_PLUGIN_ROOTS)"`, `node -e "x(process.env.CLAUDE_PLUGIN_ROOTS)"`},
	} {
		if got := pluginEnvRefs(c.in, "p"); got != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// A command server is given both variables, as Claude Code gives them; a
// subagent's prompt and a skill's SKILL.md have ${CLAUDE_PLUGIN_ROOT}
// resolved, as Claude Code resolves it in their Markdown.
func TestPluginPartsGetItsFolders(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "kit")
	write(t, filepath.Join(src, ".claude-plugin/plugin.json"), `{"name":"kit"}`)
	write(t, filepath.Join(src, ".mcp.json"), `{"mcpServers":{"kit-db":{"command":"node","args":["-e","require(process.env.CLAUDE_PLUGIN_ROOT+'/db.js')"]},"kit-web":{"url":"https://example.com/mcp"}}}`)
	write(t, filepath.Join(src, "agents/helper.md"), "---\nname: helper\ndescription: Helps\n---\nRun ${CLAUDE_PLUGIN_ROOT}/bin/help.\n")
	write(t, filepath.Join(src, "skills/use/SKILL.md"), "---\nname: use\ndescription: Use it\n---\nRun `${CLAUDE_PLUGIN_ROOT}/bin/use`.\n")
	ok(t)(InstallPlugins(src, nil, []string{"claude"}))
	root, data := pluginDir("kit"), pluginData("kit")

	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	db, web := l.server("kit-db"), l.server("kit-web")
	if db == nil || db.Env["CLAUDE_PLUGIN_ROOT"] != root || db.Env["CLAUDE_PLUGIN_DATA"] != data {
		t.Errorf("command server: %+v", db)
	}
	if web == nil || len(web.Env) != 0 {
		t.Errorf("a URL server has no process: %+v", web)
	}
	if s := l.subagent("helper"); s == nil || s.Prompt != "Run "+root+"/bin/help." {
		t.Errorf("subagent: %+v", s)
	}
	if s := read(t, filepath.Join(h, ".claude/agents/helper.md")); !strings.Contains(s, "Run "+root+"/bin/help.") {
		t.Errorf("claude's subagent:\n%s", s)
	}
	if s := read(t, filepath.Join(skillDir("use"), "SKILL.md")); !strings.Contains(s, "Run `"+root+"/bin/use`.") {
		t.Errorf("skill:\n%s", s)
	}
	inJSON := func(s string) string { b, _ := json.Marshal(s); return string(b[1 : len(b)-1]) }
	if cj := read(t, filepath.Join(h, ".claude.json")); !strings.Contains(cj, `"CLAUDE_PLUGIN_ROOT": "`+inJSON(root)+`"`) && !strings.Contains(cj, `"CLAUDE_PLUGIN_ROOT":"`+inJSON(root)+`"`) {
		t.Errorf("claude's server env:\n%s", cj)
	}
}
