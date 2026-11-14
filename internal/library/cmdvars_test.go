package library

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// math_milian: a command's $HOME/bin/x, ${NAME} and ~ go to an agent that
// reads a reference in a command as that agent's reference, $NAME as
// ${NAME}; every other agent is given the values, HOME its own home, and
// a variable nothing has as written.
func TestCmdVarsPerAgent(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("MAGPIE_T_SET", "v1")
	os.Unsetenv("MAGPIE_T_UNSET")
	lv := loginVars
	loginVars = func([]string) map[string]string { return nil }
	// the home's variable as on a Mac or Linux (Windows':
	// TestCmdVarsUserProfile)
	was := cmdOS
	cmdOS = "linux"
	t.Cleanup(func() { loginVars, cmdOS = lv, was })

	s := &Server{Name: "x", Transport: "stdio", Command: "$HOME/bin/x",
		Args: []string{"--notes", "~/notes", "${MAGPIE_T_SET}", "$MAGPIE_T_UNSET", "$1", "a~/b", "~"}}
	keep := func(ref func(string) string) []string {
		return []string{ref("HOME") + "/bin/x", "--notes", h + "/notes", ref("MAGPIE_T_SET"), ref("MAGPIE_T_UNSET"), "$1", "a~/b", h}
	}
	dollar := func(n string) string { return "${" + n + "}" }
	put := []string{h + "/bin/x", "--notes", h + "/notes", "v1", "$MAGPIE_T_UNSET", "$1", "a~/b", h}
	for _, c := range []struct {
		name string
		f    mcpFile
		want []string
	}{
		{"claude, codebuddy, workbuddy", mcpFile{Format: fmtClaude}, keep(dollar)},
		{"droid", mcpFile{Format: fmtClaude, NoCmdRefs: true}, put},
		{"grok", mcpFile{Format: fmtGrok}, keep(dollar)},
		{"gemini", mcpFile{Format: fmtGemini}, keep(dollar)},
		{"omp, qoder, atomcode", mcpFile{Format: fmtOmp}, keep(dollar)},
		{"opencode", mcpFile{Format: fmtOpenCode}, keep(func(n string) string { return "{env:" + n + "}" })},
		{"cursor", mcpFile{Format: fmtCursor}, keep(func(n string) string { return "${env:" + n + "}" })},
		{"codex", mcpFile{Format: fmtCodex}, put},
		{"kiro", mcpFile{Format: fmtKiro}, put},
		{"copilot", mcpFile{Format: fmtCopilot}, put},
		{"claude desktop", mcpFile{Format: fmtDesktop}, put},
		{"pi-mcp-extension", mcpFile{Format: fmtPi, Literal: true}, put},
		// in WSL: the distro's home, and no variable of magpie's
		{"codex in WSL", mcpFile{Format: fmtCodex, WSL: true, Home: "/home/me"},
			[]string{"/home/me/bin/x", "--notes", "/home/me/notes", "${MAGPIE_T_SET}", "$MAGPIE_T_UNSET", "$1", "a~/b", "/home/me"}},
		{"claude in WSL", mcpFile{Format: fmtClaude, WSL: true, Home: "/home/me"},
			[]string{"${HOME}/bin/x", "--notes", "/home/me/notes", "${MAGPIE_T_SET}", "${MAGPIE_T_UNSET}", "$1", "a~/b", "/home/me"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := c.f.withCmdVars(s)
			if have := append([]string{got.Command}, got.Args...); !slices.Equal(have, c.want) {
				t.Errorf("got  %q\nwant %q", have, c.want)
			}
		})
	}
	if s.Command != "$HOME/bin/x" || s.Args[1] != "~/notes" {
		t.Errorf("the library's server was changed: %+v", s)
	}
	plain := &Server{Name: "y", Transport: "stdio", Command: "npx", Args: []string{"-y", "pkg"}}
	if (&mcpFile{Format: fmtCodex}).withCmdVars(plain) != plain {
		t.Error("a command with nothing to put in is copied")
	}
}

// The library keeps $HOME/bin/x as written; Claude Code's file has
// ${HOME}/bin/x (${USERPROFILE}/bin/x on Windows), Codex's this home's
// path, and a second sync rewrites neither.
func TestCmdVarsSync(t *testing.T) {
	h := sandbox(t)
	s := Server{Name: "mine", Transport: "stdio", Command: "$HOME/bin/x", Args: []string{"~/notes"},
		Agents: []string{"claude", "codex"}}
	if r, err := SaveServer("", s); err != nil || len(r.Problems) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if got := l.server("mine"); got == nil || got.Command != "$HOME/bin/x" || !slices.Equal(got.Args, []string{"~/notes"}) {
		t.Errorf("the library has %+v", got)
	}
	claude := read(t, filepath.Join(h, ".claude.json"))
	hv := `"${HOME}/bin/x"`
	if runtime.GOOS == "windows" {
		hv = `"${USERPROFILE}/bin/x"`
	}
	if !strings.Contains(claude, hv) || !strings.Contains(claude, filepath.ToSlash(h)+`/notes"`) && !strings.Contains(claude, strings.ReplaceAll(h, `\`, `\\`)+`/notes"`) {
		t.Errorf("claude:\n%s", claude)
	}
	codex := read(t, filepath.Join(h, ".codex/config.toml"))
	if strings.Contains(codex, "$HOME") || strings.Contains(codex, "~/") || !strings.Contains(codex, "/bin/x") {
		t.Errorf("codex:\n%s", codex)
	}
	if r, err := Sync(); err != nil || len(r.Changed) != 0 {
		t.Errorf("sync: %+v %v", r, err)
	}
	if after := read(t, filepath.Join(h, ".claude.json")) + read(t, filepath.Join(h, ".codex/config.toml")); after != claude+codex {
		t.Errorf("a sync rewrote them:\n%s", after)
	}
}

// magpie starts a command with its ~ and variables put in: the check (and
// so the MCP router, which starts it the same way) and Codex on magpie.
func TestCmdVarsLaunch(t *testing.T) {
	fake := fakeStdio(t, "ok", nil)
	dir, base := filepath.Dir(fake.Command), filepath.Base(fake.Command)
	t.Setenv("MAGPIE_T_DIR", dir)
	byVar := *fake
	byVar.Command = "$MAGPIE_T_DIR/" + base
	if h := CheckServer(context.Background(), &byVar); h.State != "ok" || h.Tools != 3 {
		t.Errorf("$MAGPIE_T_DIR: %+v", h)
	}
	// a home of the test's own (the fake refuses one outside it), with the
	// server in it
	h := t.TempDir()
	b, err := os.ReadFile(fake.Command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, base), b, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	byHome := *fake
	byHome.Command = "~/" + base
	if h := CheckServer(context.Background(), &byHome); h.State != "ok" || h.Tools != 3 {
		t.Errorf("~: %+v", h)
	}
	spec, ok := specOf(&Server{Transport: "stdio", Command: "${HOME}/bin/x", Args: []string{"~/a", "$MAGPIE_T_DIR"}})
	if want := []string{h + "/a", dir}; !ok || spec.Command != h+"/bin/x" || !slices.Equal(spec.Args, want) {
		t.Errorf("spec: %+v", spec)
	}
}

// 0xBCD18E on X: %USERPROFILE%\AppData\…\pinky.exe in the library, on
// computers whose users have different names. HOME and USERPROFILE are the
// home on every system, in each syntax; an agent that reads references is
// given the home's own variable where it runs; any other %NAME% is a
// variable on Windows only; a command's environment gets its home put in.
func TestCmdVarsUserProfile(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("MAGPIE_T_AD", `C:\AD`)
	t.Setenv("Y", "set")
	lv := loginVars
	loginVars = func([]string) map[string]string { return nil }
	t.Setenv("TOKEN", "tk")
	was := cmdOS
	t.Cleanup(func() { loginVars, cmdOS = lv, was })

	s := &Server{Name: "pinky", Transport: "stdio", Command: `%USERPROFILE%\AppData\Local\Programs\Pinky\pinky.exe`,
		Args: []string{`~\x`, "$USERPROFILE/y", `%MAGPIE_T_AD%\z`, "%userprofile%", "%Y%", "100%", "%20"},
		Env:  map[string]string{"CFG": `%USERPROFILE%\c`, "D": "~/d", "T": "${TOKEN}", "P": "50%"}}
	env := map[string]string{"CFG": h + `\c`, "D": h + "/d", "T": "${TOKEN}", "P": "50%"}
	for _, c := range []struct {
		os, name string
		f        mcpFile
		want     []string
	}{
		{"windows", "codex", mcpFile{Format: fmtCodex},
			[]string{h + `\AppData\Local\Programs\Pinky\pinky.exe`, h + `\x`, h + "/y", `C:\AD\z`, h, "set", "100%", "%20"}},
		{"windows", "claude", mcpFile{Format: fmtClaude},
			[]string{`${USERPROFILE}\AppData\Local\Programs\Pinky\pinky.exe`, h + `\x`, "${USERPROFILE}/y", `${MAGPIE_T_AD}\z`, "${USERPROFILE}", "${Y}", "100%", "%20"}},
		{"windows", "cursor", mcpFile{Format: fmtCursor},
			[]string{`${env:USERPROFILE}\AppData\Local\Programs\Pinky\pinky.exe`, h + `\x`, "${env:USERPROFILE}/y", `${env:MAGPIE_T_AD}\z`, "${env:USERPROFILE}", "${env:Y}", "100%", "%20"}},
		{"darwin", "codex", mcpFile{Format: fmtCodex},
			[]string{h + `\AppData\Local\Programs\Pinky\pinky.exe`, h + `\x`, h + "/y", `%MAGPIE_T_AD%\z`, h, "%Y%", "100%", "%20"}},
		{"darwin", "claude", mcpFile{Format: fmtClaude},
			[]string{`${HOME}\AppData\Local\Programs\Pinky\pinky.exe`, h + `\x`, "${HOME}/y", `%MAGPIE_T_AD%\z`, "${HOME}", "%Y%", "100%", "%20"}},
		{"linux", "opencode", mcpFile{Format: fmtOpenCode},
			[]string{`{env:HOME}\AppData\Local\Programs\Pinky\pinky.exe`, h + `\x`, "{env:HOME}/y", `%MAGPIE_T_AD%\z`, "{env:HOME}", "%Y%", "100%", "%20"}},
		{"linux", "codex", mcpFile{Format: fmtCodex},
			[]string{h + `\AppData\Local\Programs\Pinky\pinky.exe`, h + `\x`, h + "/y", `%MAGPIE_T_AD%\z`, h, "%Y%", "100%", "%20"}},
		// an agent in WSL, from Windows: the distro's HOME, no variable of
		// Windows'
		{"windows", "claude in WSL", mcpFile{Format: fmtClaude, WSL: true, Home: "/home/me"},
			[]string{`${HOME}\AppData\Local\Programs\Pinky\pinky.exe`, `/home/me\x`, "${HOME}/y", `%MAGPIE_T_AD%\z`, "${HOME}", "%Y%", "100%", "%20"}},
	} {
		t.Run(c.os+" "+c.name, func(t *testing.T) {
			cmdOS = c.os
			got := c.f.withCmdVars(s)
			if have := append([]string{got.Command}, got.Args...); !slices.Equal(have, c.want) {
				t.Errorf("got  %q\nwant %q", have, c.want)
			}
			want := env
			if c.f.WSL {
				want = map[string]string{"CFG": `/home/me\c`, "D": "/home/me/d", "T": "${TOKEN}", "P": "50%"}
			}
			if !maps.Equal(got.Env, want) {
				t.Errorf("env %q, want %q", got.Env, want)
			}
		})
	}
	if s.Command != `%USERPROFILE%\AppData\Local\Programs\Pinky\pinky.exe` || s.Args[0] != `~\x` || s.Env["CFG"] != `%USERPROFILE%\c` {
		t.Errorf("the library's server was changed: %+v", s)
	}
	cmdOS = was
	// magpie starting it: the home's variables are this home on every
	// system
	spec, ok := specOf(s)
	if !ok || !strings.HasPrefix(spec.Command, h+`\AppData`) || spec.Args[1] != h+"/y" || spec.Args[3] != h || spec.Env["CFG"] != h+`\c` || spec.Env["D"] != h+"/d" {
		t.Errorf("spec: %+v", spec)
	}
}

// The reporter's server: %USERPROFILE% then the program, started by the
// check (and so the router) on this system.
func TestCmdVarsUserProfileLaunch(t *testing.T) {
	fake := fakeStdio(t, "ok", nil)
	base := filepath.Base(fake.Command)
	h := t.TempDir()
	b, err := os.ReadFile(fake.Command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, base), b, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	for _, c := range []string{"%USERPROFILE%" + string(filepath.Separator) + base, "%HOME%/" + base, "$USERPROFILE/" + base} {
		s := *fake
		s.Command = c
		if got := CheckServer(context.Background(), &s); got.State != "ok" || got.Tools != 3 {
			t.Errorf("%s: %+v", c, got)
		}
	}
}
