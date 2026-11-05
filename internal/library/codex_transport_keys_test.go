package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// codexLoads fails the test when Codex would refuse the entry: a key of
// the other transport beside a command or a URL (codex-rs/config/src/
// mcp_types.rs, TryFrom RawMcpServerConfig).
func codexLoads(t *testing.T, cfg, name string) map[string]any {
	t.Helper()
	e := codexEntry(t, cfg, name)
	if e == nil {
		t.Fatalf("no %s in:\n%s", name, read(t, cfg))
	}
	stdio := []string{"args", "env", "env_vars", "cwd"}
	remote := []string{"url", "bearer_token_env_var", "http_headers", "env_http_headers", "http_headers_helper", "oauth", "oauth_resource", "auth"}
	_, cmd := e["command"]
	for k := range e {
		if cmd && slices.Contains(remote, k) && k != "url" || !cmd && slices.Contains(stdio, k) {
			t.Errorf("Codex refuses %s here:\n%s", k, read(t, cfg))
		}
	}
	return e
}

// A server that was a command passing a variable on (env_vars), with a
// folder the user gave it (cwd), and is changed to a URL is written to
// Codex with neither: Codex won't load a config.toml with "env_vars is
// not supported for streamable_http", and the ChatGPT app won't start
// (kwanyu, Discord #help, 11-06). The user's keys both transports take
// stay.
func TestCodexServerMadeRemoteLeavesCommandKeys(t *testing.T) {
	h := sandbox(t)
	cfg := filepath.Join(h, ".codex/config.toml")
	srv := Server{Name: "docs", Transport: "stdio", Command: "npx", Args: []string{"-y", "docs-mcp"},
		Env: map[string]string{"DOCS_TOKEN": "${DOCS_TOKEN}", "MODE": "fast"}, Agents: []string{"codex"}}
	ok(t)(SaveServer("", srv))
	s := read(t, cfg)
	if !strings.Contains(s, "env_vars") {
		t.Fatalf("no env_vars for the referenced variable:\n%s", s)
	}
	// the user's own: a folder, and the tools it may call
	write(t, cfg, strings.Replace(s, "command = ", "cwd = \"/srv\"\nenabled_tools = [\"search\"]\ncommand = ", 1))

	srv.Transport, srv.Command, srv.Args, srv.Env = "http", "", nil, nil
	srv.URL = "https://docs.example/mcp"
	ok(t)(SaveServer("docs", srv))
	e := codexLoads(t, cfg, "docs")
	if e["url"] != srv.URL {
		t.Errorf("url = %v:\n%s", e["url"], read(t, cfg))
	}
	if got, _ := e["enabled_tools"].([]any); len(got) != 1 || got[0] != "search" {
		t.Errorf("the user's enabled_tools went: %v\n%s", e["enabled_tools"], read(t, cfg))
	}
}

// The converse: a URL with a bearer token from a variable, a header and
// the user's own OAuth settings, changed to a command, is written without
// any key Codex takes only for a URL.
func TestCodexServerMadeLocalLeavesURLKeys(t *testing.T) {
	h := sandbox(t)
	cfg := filepath.Join(h, ".codex/config.toml")
	srv := Server{Name: "web", Transport: "http", URL: "https://x.example/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${WEB_TOKEN}", "X-Key": "${WEB_KEY}", "X-Team": "a"}, Agents: []string{"codex"}}
	ok(t)(SaveServer("", srv))
	s := read(t, cfg)
	for _, w := range []string{"bearer_token_env_var", "env_http_headers", "http_headers"} {
		if !strings.Contains(s, w) {
			t.Fatalf("no %s:\n%s", w, s)
		}
	}
	write(t, cfg, strings.Replace(s, "url = ", "oauth_resource = \"https://x.example\"\nscopes = [\"read\"]\ntool_timeout_sec = 90\nurl = ", 1))

	srv.Transport, srv.URL, srv.Headers = "stdio", "", nil
	srv.Command, srv.Args = "web-mcp", []string{"--stdio"}
	ok(t)(SaveServer("web", srv))
	e := codexLoads(t, cfg, "web")
	if e["command"] != "web-mcp" || e["url"] != nil {
		t.Errorf("not a command:\n%s", read(t, cfg))
	}
	if !isNumber(e["tool_timeout_sec"], 90) {
		t.Errorf("the user's tool_timeout_sec went:\n%s", read(t, cfg))
	}
	if got, _ := e["scopes"].([]any); len(got) != 1 {
		t.Errorf("scopes, which Codex takes for both, went:\n%s", read(t, cfg))
	}
}

// An entry magpie wrote before with a command's keys beside its URL, the
// reporter's shape, is written again on the next sync, though the server
// itself reads as unchanged; one of a project's .codex/config.toml too.
// A stdio entry's own env_vars, which the user gave it, stays.
func TestCodexEntryWithOtherTransportKeysRepaired(t *testing.T) {
	h := sandbox(t)
	cfg := filepath.Join(h, ".codex/config.toml")
	ok(t)(SaveServer("", Server{Name: "remote", Transport: "http", URL: "https://r.example/mcp", Agents: []string{"codex"}}))
	ok(t)(SaveServer("", Server{Name: "local", Transport: "stdio", Command: "npx", Args: []string{"-y", "l"}, Agents: []string{"codex"}}))
	s := read(t, cfg)
	s = strings.Replace(s, "url = ", "env_vars = [\"HOME_TOKEN\", { name = \"T\", source = \"local\" }]\nargs = []\nenabled_tools = [\"a\"]\nurl = ", 1)
	s = strings.Replace(s, "command = ", "env_vars = [\"MINE\"]\nbearer_token_env_var = \"X\"\ncommand = ", 1)
	write(t, cfg, s)

	ok(t)(Sync())
	r := codexLoads(t, cfg, "remote")
	if got, _ := r["enabled_tools"].([]any); len(got) != 1 {
		t.Errorf("the user's enabled_tools went:\n%s", read(t, cfg))
	}
	l := codexLoads(t, cfg, "local")
	if got, _ := l["env_vars"].([]any); len(got) != 1 || got[0] != "MINE" {
		t.Errorf("the user's env_vars on a command went: %v\n%s", l["env_vars"], read(t, cfg))
	}
}

// A project's .codex/config.toml is held to the same: a server made remote
// there leaves its command's keys.
func TestCodexProjectEntryLeavesCommandKeys(t *testing.T) {
	f := &mcpFile{Format: fmtCodex, Path: filepath.Join(t.TempDir(), ".codex", "config.toml")}
	write(t, f.Path, "[mcp_servers.p]\ncommand = \"npx\"\nargs = []\ncwd = \"/w\"\nenv_vars = [\"P_TOKEN\"]\n")
	es, err := f.entries()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Name: "p", Transport: "http", URL: "https://p.example/mcp"}
	if !f.behind(s, es["p"]) {
		t.Error("an entry with a command's keys beside a URL reads as in step")
	}
	if err := f.put(s, es["p"]); err != nil {
		t.Fatal(err)
	}
	codexLoads(t, f.Path, "p")
}
