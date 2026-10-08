package library

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// #1250: a server's ${NAME} goes to each agent in that agent's own syntax,
// comes back from it as ${NAME}, and is never rewritten by a sync; a literal
// header or variable beside it is written as it is.
func TestEnvRefsPerAgent(t *testing.T) {
	web := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${MY_TOKEN}", "X-Static": "literal"}}
	fs := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"},
		Env: map[string]string{"MY_TOKEN": "${MY_TOKEN}", "PLAIN": "x"}}
	for _, c := range []struct {
		name   string
		f      mcpFile
		file   string
		web    []string // in the entry magpie writes, as JSON
		fs     []string
		absent []string // in neither
	}{
		{name: "claude", f: mcpFile{Format: fmtClaude}, file: "c.json",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`, `"X-Static":"literal"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`, `"PLAIN":"x"`}},
		{name: "codex", f: mcpFile{Format: fmtCodex}, file: "c.toml",
			web: []string{`"bearer_token_env_var":"MY_TOKEN"`, `"http_headers":{"X-Static":"literal"}`}, fs: []string{`"env_vars":["MY_TOKEN"]`, `"env":{"PLAIN":"x"}`},
			absent: []string{"${"}},
		{name: "gemini", f: mcpFile{Format: fmtGemini}, file: "g.json",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
		{name: "opencode", f: mcpFile{Format: fmtOpenCode}, file: "o.json",
			web: []string{`"Authorization":"Bearer {env:MY_TOKEN}"`, `"X-Static":"literal"`}, fs: []string{`"MY_TOKEN":"{env:MY_TOKEN}"`, `"PLAIN":"x"`},
			absent: []string{"${"}},
		{name: "cursor", f: mcpFile{Format: fmtCursor}, file: "cu.json",
			web: []string{`"Authorization":"Bearer ${env:MY_TOKEN}"`, `"X-Static":"literal"`}, fs: []string{`"MY_TOKEN":"${env:MY_TOKEN}"`},
			absent: []string{"${MY_TOKEN}"}},
		{name: "copilot", f: mcpFile{Format: fmtCopilot}, file: "cp.json",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
		{name: "crush", f: mcpFile{Format: fmtCrush}, file: "cr.json",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
		{name: "goose", f: mcpFile{Format: fmtGoose}, file: "g.yaml",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`, `"env_keys":["MY_TOKEN"]`}, fs: []string{`"envs":{"PLAIN":"x"}`, `"env_keys":["MY_TOKEN"]`}},
		{name: "pi-mcp-adapter", f: mcpFile{Format: fmtPi}, file: "pa.json",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
		{name: "pi, omo", f: mcpFile{Format: fmtPiNative}, file: "pn.json",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
		{name: "omp, qoder, atomcode", f: mcpFile{Format: fmtOmp}, file: "om.json",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
		{name: "hermes", f: mcpFile{Format: fmtHermes}, file: "h.yaml",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
		{name: "grok", f: mcpFile{Format: fmtGrok}, file: "gr.toml",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
		{name: "commandcode", f: mcpFile{Format: fmtCommandCode}, file: "cc.json",
			web: []string{`"Authorization":"Bearer ${MY_TOKEN}"`}, fs: []string{`"MY_TOKEN":"${MY_TOKEN}"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := c.f
			f.Path = filepath.Join(t.TempDir(), c.file)
			for _, s := range []Server{web, fs} {
				if err := f.supports(&s); err != nil {
					t.Fatalf("%s: %v", s.Name, err)
				}
				if err := f.put(&s, nil); err != nil {
					t.Fatal(err)
				}
			}
			es, err := f.entries()
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string][]string{"web": c.web, "fs": c.fs} {
				j, _ := json.Marshal(es[name])
				for _, w := range want {
					if !strings.Contains(string(j), w) {
						t.Errorf("%s: no %s in %s", name, w, j)
					}
				}
				for _, a := range c.absent {
					if strings.Contains(string(j), a) {
						t.Errorf("%s: %s in %s", name, a, j)
					}
				}
			}
			back, err := f.read()
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range []Server{web, fs} {
				if got := back[s.Name]; got == nil || !got.same(&s) {
					t.Errorf("%s read back as %+v", s.Name, got)
				}
				// a sync finds it as the library has it, and leaves it
				if cur, ok := f.current(s.Name, es[s.Name], &s); !ok || !cur.same(&s) || !f.has(&s) {
					t.Errorf("%s isn't seen as written: %+v", s.Name, cur)
				}
			}
		})
	}
}

// An agent that reads no reference isn't given a server with one, and says
// why; one without a reference it is given as before.
func TestEnvRefsLeftOut(t *testing.T) {
	ref := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${MY_TOKEN}"}}
	cmd := Server{Name: "fs", Transport: "stdio", Command: "npx", Env: map[string]string{"MY_TOKEN": "${MY_TOKEN}"}}
	plain := Server{Name: "lit", Transport: "stdio", Command: "npx", Env: map[string]string{"K": "V"}}
	for _, f := range []mcpFile{{Format: fmtAntigravity}, {Format: fmtKimi}, {Format: fmtCline}, {Format: fmtZCode},
		{Format: fmtDevin}, {Format: fmtDesktop}, {Format: fmtDsh}, {Format: fmtPi, Literal: true}} {
		if err := f.supports(&cmd); !errors.Is(err, errNoEnvRef) {
			t.Errorf("%d %v: command: %v", f.Format, f.Literal, err)
		}
		if f.Format != fmtDesktop {
			if err := f.supports(&ref); !errors.Is(err, errNoEnvRef) {
				t.Errorf("%d: remote: %v", f.Format, err)
			}
		}
		if err := f.supports(&plain); err != nil {
			t.Errorf("%d: a server with no reference: %v", f.Format, err)
		}
	}
	// a remote server's environment, a command's headers: neither is sent
	if err := (&mcpFile{Format: fmtKimi}).supports(&Server{Name: "x", Transport: "stdio", Command: "x", Headers: map[string]string{"A": "${B}"}}); err != nil {
		t.Errorf("a command's headers aren't sent: %v", err)
	}
	// what Codex and Goose can't name
	for _, c := range []struct {
		f mcpFile
		s Server
	}{
		{mcpFile{Format: fmtCodex}, Server{Transport: "http", URL: "u", Headers: map[string]string{"X-Team": "team-${TEAM}"}}},
		{mcpFile{Format: fmtCodex}, Server{Transport: "stdio", Command: "c", Env: map[string]string{"TOKEN": "${OTHER}"}}},
		{mcpFile{Format: fmtGoose}, Server{Transport: "stdio", Command: "c", Env: map[string]string{"TOKEN": "x${TOKEN}"}}},
		{mcpFile{Format: fmtGoose}, Server{Transport: "http", URL: "u", Headers: map[string]string{"A": "${lower}"}}},
	} {
		if err := c.f.supports(&c.s); err == nil {
			t.Errorf("%d took %+v", c.f.Format, c.s)
		}
	}
	// Codex takes a header whole from a variable
	if err := (&mcpFile{Format: fmtCodex}).supports(&Server{Transport: "http", URL: "u", Headers: map[string]string{"X-Key": "${KEY}"}}); err != nil {
		t.Error(err)
	}
}

// Through SaveServer: the library keeps ${NAME}, Claude Code and Codex get
// it in their syntax, Antigravity is left out with the reason, the page is
// told which agent reads none, and a sync rewrites nothing.
func TestEnvRefsLibrary(t *testing.T) {
	h, _ := antigravity(t)
	s := Server{Name: "docs", Transport: "http", URL: "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${DOCS_TOKEN}", "X-Static": "literal"},
		Agents:  []string{"claude", "codex", "agy"}}
	r, err := SaveServer("", s)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 1 || r.Problems[0].Agent != "agy" || r.Problems[0].Error != errNoEnvRef.Error() {
		t.Errorf("problems: %+v", r.Problems)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if got := l.server("docs"); got == nil || got.Headers["Authorization"] != "Bearer ${DOCS_TOKEN}" || got.Headers["X-Static"] != "literal" {
		t.Errorf("the library has %+v", got)
	}
	claude := read(t, filepath.Join(h, ".claude.json"))
	if !strings.Contains(claude, `"Bearer ${DOCS_TOKEN}"`) {
		t.Errorf("claude:\n%s", claude)
	}
	codex := read(t, filepath.Join(h, ".codex/config.toml"))
	if !strings.Contains(codex, `bearer_token_env_var = "DOCS_TOKEN"`) || strings.Contains(codex, "${") || !strings.Contains(codex, "literal") {
		t.Errorf("codex:\n%s", codex)
	}
	agy := targetByID("agy").MCP.Path
	if b, err := os.ReadFile(agy); err == nil && strings.Contains(string(b), "docs") {
		t.Errorf("agy was given it:\n%s", b)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if want := a.ID == "agy"; (a.ID == "agy" || a.ID == "claude" || a.ID == "codex") && a.NoEnvRefs != want {
			t.Errorf("%s: noEnvRefs %v", a.ID, a.NoEnvRefs)
		}
	}
	before := claude + codex
	if r, err := Sync(); err != nil || len(r.Problems) != 1 || len(r.Changed) != 0 {
		t.Errorf("sync: %+v %v", r, err)
	}
	if after := read(t, filepath.Join(h, ".claude.json")) + read(t, filepath.Join(h, ".codex/config.toml")); after != before {
		t.Errorf("a sync rewrote them:\n%s", after)
	}
	// brought back in from Codex, it is the library's ${NAME} again
	back, _ := targetByID("codex").MCP.read()
	if got := back["docs"]; got == nil || got.Headers["Authorization"] != "Bearer ${DOCS_TOKEN}" {
		t.Errorf("codex read back as %+v", got)
	}
}

// The user's own values: a Codex entry's own Authorization beside a
// bearer_token_env_var reads as the one written; a variable the user
// passes on (env_vars, Goose's env_keys) of a server whose library entry
// has none stays, and isn't rewritten; the user's env_vars tables naming
// the variables magpie would are kept as they wrote them.
func TestEnvRefsKeepTheUsers(t *testing.T) {
	codex := &mcpFile{Format: fmtCodex, Path: filepath.Join(t.TempDir(), "config.toml")}
	if s, _ := codex.decode("x", map[string]any{"url": "u", "http_headers": map[string]any{"Authorization": "Bearer mine"}, "bearer_token_env_var": "T"}); s.Headers["Authorization"] != "Bearer mine" {
		t.Errorf("decode: %+v", s.Headers)
	}

	write(t, codex.Path, "[mcp_servers.fs]\ncommand = \"npx\"\nargs = []\nenv_vars = [\"HOME_TOKEN\"]\nstartup_timeout_sec = 20\n\n[mcp_servers.ref]\ncommand = \"npx\"\nargs = []\nenv_vars = [{ name = \"MY_TOKEN\", source = \"local\" }]\n")
	plain := Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"codex"}}
	ref := Server{Name: "ref", Transport: "stdio", Command: "npx", Env: map[string]string{"MY_TOKEN": "${MY_TOKEN}"}}
	es, _ := codex.entries()
	if cur, ok := codex.current("fs", es["fs"], &plain); !ok || !cur.same(&plain) {
		t.Errorf("the user's env_vars count as the library's: %+v", cur)
	}
	if cur, ok := codex.current("ref", es["ref"], &ref); !ok || !cur.same(&ref) {
		t.Errorf("env_vars as a table: %+v", cur)
	}
	if err := codex.put(&plain, es["fs"]); err != nil {
		t.Fatal(err)
	}
	if err := codex.put(&ref, es["ref"]); err != nil {
		t.Fatal(err)
	}
	got := read(t, codex.Path)
	for _, w := range []string{`HOME_TOKEN`, `startup_timeout_sec = 20`, `source = "local"`} {
		if !strings.Contains(got, w) {
			t.Errorf("lost %s:\n%s", w, got)
		}
	}

	goose := &mcpFile{Format: fmtGoose, Path: filepath.Join(t.TempDir(), "config.yaml")}
	write(t, goose.Path, "extensions:\n  fs:\n    enabled: true\n    name: fs\n    type: stdio\n    cmd: npx\n    args: []\n    env_keys: [GH_TOKEN]\n    timeout: 300\n")
	gs := Server{Name: "fs", Transport: "stdio", Command: "npx"}
	es, _ = goose.entries()
	if cur, ok := goose.current("fs", es["fs"], &gs); !ok || !cur.same(&gs) {
		t.Errorf("goose: the user's env_keys count as the library's: %+v", cur)
	}
	if err := goose.put(&gs, es["fs"]); err != nil {
		t.Fatal(err)
	}
	if got := read(t, goose.Path); !strings.Contains(got, "GH_TOKEN") {
		t.Errorf("goose lost the user's env_keys:\n%s", got)
	}
	// brought in, it's the variable the user's Goose reads
	if s, _ := goose.decode("fs", es["fs"]); s.Env["GH_TOKEN"] != "${GH_TOKEN}" {
		t.Errorf("goose decode: %+v", s.Env)
	}
}

// The health check reaches a server with magpie's own value of the
// variable, and says which is unset rather than sending ${NAME}.
func TestEnvRefsExpand(t *testing.T) {
	t.Setenv("MAGPIE_T_SET", "v1")
	t.Setenv("MAGPIE_T_UNSET", "")
	os.Unsetenv("MAGPIE_T_UNSET")
	got, unset := expandAll(map[string]string{"A": "Bearer ${MAGPIE_T_SET}", "B": "${MAGPIE_T_UNSET}-${MAGPIE_T_SET}", "C": "lit $X"})
	if got["A"] != "Bearer v1" || got["C"] != "lit $X" || !slices.Equal(unset, []string{"MAGPIE_T_UNSET"}) {
		t.Errorf("%v %v", got, unset)
	}
	if h := checkStdio(t.Context(), &Server{Transport: "stdio", Command: "true", Env: map[string]string{"T": "${MAGPIE_T_UNSET}"}}); h.Why != "novar" || h.Detail != "MAGPIE_T_UNSET" {
		t.Errorf("stdio: %+v", h)
	}
	if _, h := remoteHeaders(t.Context(), &Server{Name: "x", Transport: "http", URL: "http://127.0.0.1:9/", Headers: map[string]string{"Authorization": "Bearer ${MAGPIE_T_UNSET}"}}); h == nil || h.Why != "novar" {
		t.Errorf("remote: %+v", h)
	}
}
