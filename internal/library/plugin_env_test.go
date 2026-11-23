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
		c := withoutPluginEnv(gs[0].Hooks[0].Command)
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
	// the variables Claude Code's entry sets in front aside
	if strings.Contains(s, "process.env.CLAUDE_PLUGIN_ROOT") || strings.Count(s, "ponytail-activate.js") != 1 || !strings.Contains(s, lit) {
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

// withoutPluginEnv is a Claude Code hook's command without the variables
// set in front of it (pluginHookEnv).
func withoutPluginEnv(c string) string {
	if strings.HasPrefix(c, "export CLAUDE_PLUGIN_ROOT=") || strings.HasPrefix(c, "$env:CLAUDE_PLUGIN_ROOT=") {
		if i := strings.Index(c, "; "); i >= 0 {
			if j := strings.Index(c[i+2:], "; "); j >= 0 && strings.HasPrefix(c, "$env:") {
				return c[i+2+j+2:]
			}
			return c[i+2:]
		}
	}
	return c
}

// claudeHookCommands are the hook commands and shells in Claude Code's
// settings at path, event by event.
func claudeHookCommands(t *testing.T, path string) map[string][][2]string {
	t.Helper()
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
				Shell   string `json:"shell"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &settings); err != nil {
		t.Fatal(err)
	}
	out := map[string][][2]string{}
	for ev, gs := range settings.Hooks {
		for _, g := range gs {
			for _, h := range g.Hooks {
				out[ev] = append(out[ev], [2]string{h.Command, h.Shell})
			}
		}
	}
	return out
}

// A split plugin's hook script reads CLAUDE_PLUGIN_ROOT from its own
// environment, where no command text shows it (jinpwng on X): Claude Code
// gives a native plugin's hook both variables, and its entry from the
// library now sets them before the command. Other agents' entries are as
// the command is.
func TestPluginHookScriptReadsRootFromEnv(t *testing.T) {
	// found before the sandbox's PATH holds only its fake agents
	node, _ := exec.LookPath("node")
	sh, _ := exec.LookPath("sh")
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "envkit")
	write(t, filepath.Join(src, ".claude-plugin/plugin.json"), `{"name":"envkit","version":"1.0.0"}`)
	write(t, filepath.Join(src, "hooks/hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"node \"${CLAUDE_PLUGIN_ROOT}/hooks/stop.js\""}]}]}}`)
	write(t, filepath.Join(src, "hooks/stop.js"), "console.log(process.env.CLAUDE_PLUGIN_ROOT + '|' + process.env.CLAUDE_PLUGIN_DATA)\n")
	ok(t)(InstallPlugins(src, nil, []string{"claude", "codex"}))
	root, data := pluginDir("envkit"), pluginData("envkit")

	cs := claudeHookCommands(t, filepath.Join(h, ".claude/settings.json"))["Stop"]
	if len(cs) != 1 {
		t.Fatalf("claude Stop: %+v", cs)
	}
	stop := cs[0][0]
	if !strings.Contains(stop, "CLAUDE_PLUGIN_ROOT="+"'"+root+"'") || !strings.Contains(stop, "CLAUDE_PLUGIN_DATA="+"'"+data+"'") {
		t.Errorf("claude's entry doesn't set the variables: %s", stop)
	}
	if c := read(t, filepath.Join(h, ".codex/hooks.json")); strings.Contains(c, "CLAUDE_PLUGIN_ROOT") {
		t.Errorf("codex's entry was given Claude Code's variables:\n%s", c)
	}

	if node == "" || sh == "" || runtime.GOOS == "windows" {
		t.Log("no node or sh here: the command isn't run")
		return
	}
	cmd := exec.Command(sh, "-c", stop)
	cmd.Env = []string{"PATH=" + filepath.Dir(node), "HOME=" + h}
	out, err := cmd.CombinedOutput()
	if got := strings.TrimSpace(string(out)); err != nil || got != root+"|"+data {
		t.Fatalf("the hook's script read %q (%v), want %q", got, err, root+"|"+data)
	}
}

// The variables go in in the shell Claude Code runs the hook in: $SHELL's
// on a Mac and Linux and Git Bash's on Windows (export, with a ' in the
// folder closed and escaped), PowerShell's on Windows with no Git Bash
// (pinned, ' doubled).
func TestPluginHookEnvPerShell(t *testing.T) {
	// found before the sandbox's PATH holds only its fake agents
	node, _ := exec.LookPath("node")
	shells := map[string]string{}
	for _, n := range []string{"sh", "bash", "zsh", "powershell", "pwsh"} {
		shells[n], _ = exec.LookPath(n)
	}
	sandbox(t)
	defer func(o string, g func() string) { cmdOS, gitBash = o, g }(cmdOS, gitBash)
	const name = "it's"
	root, data := pluginDir(name), pluginData(name)
	sq := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	pq := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

	for _, c := range []struct {
		os, bash, want, shell string
	}{
		{"darwin", "", "export CLAUDE_PLUGIN_ROOT=" + sq(root) + " CLAUDE_PLUGIN_DATA=" + sq(data) + "; run", ""},
		{"linux", "", "export CLAUDE_PLUGIN_ROOT=" + sq(root) + " CLAUDE_PLUGIN_DATA=" + sq(data) + "; run", ""},
		{"windows", `C:\Program Files\Git\bin\bash.exe`, "export CLAUDE_PLUGIN_ROOT=" + sq(root) + " CLAUDE_PLUGIN_DATA=" + sq(data) + "; run", ""},
		{"windows", "", "$env:CLAUDE_PLUGIN_ROOT=" + pq(root) + "; $env:CLAUDE_PLUGIN_DATA=" + pq(data) + "; run", "powershell"},
	} {
		cmdOS, gitBash = c.os, func() string { return c.bash }
		got, shell := pluginHookEnv("run", name)
		if got != c.want || shell != c.shell {
			t.Errorf("%s (git bash %q):\n got %s [%s]\nwant %s [%s]", c.os, c.bash, got, shell, c.want, c.shell)
		}
		e := string(claudeHookSpec(filepath.Join(t.TempDir(), "s.json")).entry(&Hook{Name: "x", Bundle: name, Event: "turn-end", Command: "run"}, "Stop"))
		if c.shell != "" && !strings.Contains(e, `"shell":"`+c.shell+`"`) || c.shell == "" && strings.Contains(e, `"shell"`) {
			t.Errorf("%s entry's shell: %s", c.os, e)
		}
		// a hook of the user's own is as they wrote it
		if e := string(claudeHookSpec(filepath.Join(t.TempDir(), "s.json")).entry(&Hook{Name: "x", Event: "turn-end", Command: "run"}, "Stop")); strings.Contains(e, "CLAUDE_PLUGIN") || strings.Contains(e, `"shell"`) {
			t.Errorf("%s: the user's own hook: %s", c.os, e)
		}
	}

	// and each shell reads the folder back as it is, ' and all
	cmdOS, gitBash = "linux", func() string { return "" }
	probe := `node -e "console.log(process.env.CLAUDE_PLUGIN_ROOT)"`
	if node == "" {
		t.Log("no node here: the commands aren't run")
		return
	}
	run := func(name string, arg ...string) {
		shell, ok := shells[name]
		if !ok {
			shell = name // Git Bash, by its path
		}
		if shell == "" {
			t.Logf("no %s here", name)
			return
		}
		cmd := exec.Command(shell, arg...)
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if got := strings.TrimSpace(string(out)); err != nil || got != root {
			t.Errorf("%s read %q (%v), want %q", shell, got, err, root)
		}
	}
	if runtime.GOOS != "windows" {
		c, _ := pluginHookEnv(probe, name)
		run("sh", "-c", c)
		run("bash", "-c", c)
		run("zsh", "-c", c)
		return
	}
	cmdOS = "windows"
	if b := gitBash(); b != "" {
		c, _ := pluginHookEnv(probe, name)
		run(b, "-c", c)
	}
	gitBash = func() string { return "" }
	c, _ := pluginHookEnv(probe, name)
	run("powershell", "-NoProfile", "-Command", c)
	run("pwsh", "-NoProfile", "-Command", c)
}
