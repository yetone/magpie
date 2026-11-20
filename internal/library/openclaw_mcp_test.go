package library

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// openClawSandbox is a sandbox with OpenClaw installed: its program and
// its ~/.openclaw/openclaw.json holding the user's own settings and server.
func openClawSandbox(t *testing.T) (h, p string) {
	h = hubSandbox(t)
	holdCLI(t, h, "openclaw")
	p = filepath.Join(h, ".openclaw", "openclaw.json")
	write(t, p, `{
  "gateway": {"mode": "local"},
  "mcp": {
    "sessionIdleTtlMs": 60000,
    "servers": {
      "mine": {
        "command": "uvx",
        "args": ["mine-mcp"],
        "requestTimeoutMs": 30000,
        "toolFilter": {"include": ["read"]}
      }
    }
  }
}`)
	return h, p
}

func openClawServers(t *testing.T, p string) map[string]map[string]any {
	t.Helper()
	var f struct {
		MCP struct {
			Servers map[string]map[string]any `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(read(t, p)), &f); err != nil {
		t.Fatalf("%v:\n%s", err, read(t, p))
	}
	return f.MCP.Servers
}

// OpenClaw had no Library target, so neither a server nor the MCP router's
// magpie entry could be given to it (JeffreyCalm on X). It keeps its
// servers under mcp.servers in openclaw.json, where a url with no transport
// is SSE: streamable HTTP has to be named (OpenClaw 2026.11.6
// docs/cli/mcp.md, McpServerSchema).
func TestOpenClawMCP(t *testing.T) {
	_, p := openClawSandbox(t)
	tg := targetByID("openclaw")
	if tg == nil || tg.MCP == nil {
		t.Fatalf("openclaw has no MCP target: %+v", tg)
	}
	if tg.MCP.Path != p || tg.MCP.Format != fmtOpenClaw {
		t.Errorf("mcp: %+v", tg.MCP)
	}
	if id, err := Takes("openclaw", "mcp"); id != "openclaw" || err != nil {
		t.Errorf("Takes(openclaw, mcp): %q %v", id, err)
	}

	agents := []string{"openclaw"}
	stdio := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}, Agents: agents}
	web := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"X-Team": "a"}, Agents: agents}
	old := Server{Name: "old", Transport: "sse", URL: "https://example.com/sse", Agents: agents}
	for _, s := range []Server{stdio, web, old} {
		ok(t)(SaveServer("", s))
	}

	got := openClawServers(t, p)
	if fs := got["fs"]; fs["command"] != "npx" || fs["transport"] != nil || fs["type"] != nil {
		t.Errorf("fs: %v", fs)
	} else if e, _ := fs["env"].(map[string]any); e["K"] != "V" {
		t.Errorf("fs env: %v", fs)
	}
	if w := got["web"]; w["url"] != "https://example.com/mcp" || w["transport"] != "streamable-http" {
		t.Errorf("web, streamable HTTP named so OpenClaw doesn't take it for SSE: %v", w)
	} else if hd, _ := w["headers"].(map[string]any); hd["X-Team"] != "a" {
		t.Errorf("web headers: %v", w)
	}
	if o := got["old"]; o["transport"] != "sse" {
		t.Errorf("old: %v", o)
	}
	if m := got["mine"]; m["command"] != "uvx" || m["requestTimeoutMs"] != float64(30000) || m["toolFilter"] == nil {
		t.Errorf("the user's server and its fields kept: %v", m)
	}
	for _, want := range []string{`"sessionIdleTtlMs": 60000`, `"mode": "local"`} {
		if !strings.Contains(read(t, p), want) {
			t.Errorf("lost the user's %s:\n%s", want, read(t, p))
		}
	}

	// read back, and left as they are by a sync that changes nothing
	back, err := tg.MCP.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Server{stdio, web, old} {
		if s := back[want.Name]; s == nil || !s.same(&want) {
			t.Errorf("%s read back as %+v", want.Name, s)
		}
	}
	before := read(t, p)
	ok(t)(Sync())
	if read(t, p) != before {
		t.Errorf("a sync rewrote servers that are as the library has them:\n%s", read(t, p))
	}

	// how OpenClaw reads a url: no transport is SSE, and `type: "http"`
	// is the alias its doctor rewrites to streamable-http
	for _, c := range []struct {
		m    map[string]any
		want string
	}{
		{map[string]any{"url": "https://e/x"}, "sse"},
		{map[string]any{"url": "https://e/x", "transport": "sse"}, "sse"},
		{map[string]any{"url": "https://e/x", "transport": "streamable-http"}, "http"},
		{map[string]any{"url": "https://e/x", "type": "http"}, "http"},
	} {
		if s, _ := tg.MCP.decode("x", c.m); s == nil || s.Transport != c.want {
			t.Errorf("%v read as %+v, want %s", c.m, s, c.want)
		}
	}

	// magpie's own slow servers get OpenClaw's own key for more than its
	// minute; a "timeout" would fail the whole file
	o := tg.MCP.encode(&Server{Name: searchServerName, Transport: "stdio", Command: "/x/magpie", Args: []string{"mcp", "search"}})
	keys := map[string]any{}
	for _, e := range o {
		keys[e.k] = e.v
	}
	if keys["requestTimeoutMs"] != 120000 || keys["timeout"] != nil {
		t.Errorf("magpie-web-search: %v", o)
	}

	// an unset ${NAME} anywhere in openclaw.json fails the whole file, so
	// a server referencing one isn't written there
	ref := Server{Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}}
	if err := tg.MCP.supports(&ref); !errors.Is(err, errNoEnvRef) {
		t.Errorf("a ${NAME} header: %v", err)
	}

	for _, s := range []string{"fs", "web", "old"} {
		ok(t)(RemoveServer(s))
	}
	after := openClawServers(t, p)
	if len(after) != 1 || after["mine"]["toolFilter"] == nil {
		t.Errorf("after: %v\n%s", after, read(t, p))
	}
}

// With the MCP router on, OpenClaw is given magpie's one entry, streamable
// HTTP at the hub, in place of the servers it was given, and its own
// server is left as it is (JeffreyCalm on X: the router couldn't be turned
// on for it).
func TestOpenClawMCPHub(t *testing.T) {
	_, p := openClawSandbox(t)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}, Agents: []string{"openclaw"}}))
	r, err := SetMCPHub(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range r.Problems {
		if pr.Agent == "openclaw" {
			t.Errorf("problem: %+v", pr)
		}
	}
	got := openClawServers(t, p)
	hub := got[HubEntry]
	if u, _ := hub["url"].(string); !strings.HasSuffix(u, gateway.HubMCPPath+"openclaw") || hub["transport"] != "streamable-http" {
		t.Errorf("openclaw's magpie entry: %v\n%s", hub, read(t, p))
	}
	if got["fs"] != nil || got["mine"] == nil {
		t.Errorf("with the hub on: %v", got)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if a.ID == "openclaw" && a.Hub != "on" {
			t.Errorf("openclaw's hub %q, want on", a.Hub)
		}
	}

	ok(t)(SetMCPHub(false))
	got = openClawServers(t, p)
	if got[HubEntry] != nil || got["fs"] == nil || got["mine"] == nil {
		t.Errorf("with the hub off again: %v\n%s", got, read(t, p))
	}
}
