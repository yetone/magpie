package library

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// entryOf is the entry agent id has for name, as its own file holds it.
func entryOf(t *testing.T, id, name string) map[string]any {
	t.Helper()
	es, err := targetByID(id).MCP.entries()
	if err != nil {
		t.Fatal(err)
	}
	return es[name]
}

// A server's call timeout (steinxia on X) reaches each agent that reads one
// in a server's entry, under the agent's own key and in its own unit; an
// agent that reads none is given nothing, and a server with none gives
// nobody a key it didn't have.
func TestCallTimeoutKeys(t *testing.T) {
	s := &Server{Name: "render", Transport: "stdio", Command: "npx", Args: []string{"render-mcp"}, CallTimeout: 900}
	for _, c := range []struct {
		f   *mcpFile
		key string
		v   int
	}{
		{&mcpFile{Format: fmtClaude}, "timeout", 900000},
		{&mcpFile{Format: fmtCodex}, "tool_timeout_sec", 900},
		{&mcpFile{Format: fmtGemini}, "timeout", 900000},
		{&mcpFile{Format: fmtOpenCode}, "timeout", 900000},
		{&mcpFile{Format: fmtCopilot}, "timeout", 900000},
		{&mcpFile{Format: fmtGoose}, "timeout", 900},
		{&mcpFile{Format: fmtHermes}, "timeout", 900},
		{&mcpFile{Format: fmtCline}, "timeout", 900},
		{&mcpFile{Format: fmtKimi}, "toolTimeoutMs", 900000},
		{&mcpFile{Format: fmtKiro}, "timeout", 900000},
		{&mcpFile{Format: fmtGrok}, "tool_timeout_sec", 900},
		{&mcpFile{Format: fmtOpenClaw}, "requestTimeoutMs", 900000},
		{&mcpFile{Format: fmtPiNative}, "timeout", 900},
		{&mcpFile{Format: fmtPiNative, Call: &omoCallKey}, "requestTimeoutMs", 900000},
		{&mcpFile{Format: fmtPi}, "requestTimeoutMs", 900000},
		{&mcpFile{Format: fmtOmp}, "timeout", 900000},
		{&mcpFile{Format: fmtOmp, Call: &atomCallKey}, "timeout_ms", 900000},
		{&mcpFile{Format: fmtDsh}, "toolCallTimeoutMs", 900000},
		// Zed holds a context server to ten minutes at most
		{&mcpFile{Format: fmtZed}, "timeout", 600},
	} {
		if v, ok := valueOf(c.f.encode(s), c.key); !ok || v != c.v {
			t.Errorf("format %d: %s = %v (%v), want %d", c.f.Format, c.key, v, ok, c.v)
		}
		plain := *s
		plain.CallTimeout = 0
		if v, ok := valueOf(c.f.encode(&plain), c.key); ok && c.f.Format != fmtGoose {
			t.Errorf("format %d: no call timeout, yet %s = %v", c.f.Format, c.key, v)
		}
	}
	for _, f := range []*mcpFile{{Format: fmtCrush}, {Format: fmtCommandCode}, {Format: fmtDevin}, {Format: fmtZCode}, {Format: fmtTrae}, {Format: fmtCursor}, {Format: fmtAlma}, {Format: fmtDesktop}, {Format: fmtLiveAgent}, {Format: fmtClaude, NoCallKey: true}} {
		for _, e := range f.encode(s) {
			if strings.Contains(strings.ToLower(e.k), "timeout") {
				t.Errorf("format %d (no tool timeout of its own) was given %s", f.Format, e.k)
			}
		}
	}
	// magpie's own servers keep the longer timeouts they had, where they
	// had them, and no more
	search := &Server{Name: searchServerName, Transport: "stdio", Command: "/opt/magpie"}
	if v, _ := valueOf((&mcpFile{Format: fmtCodex}).encode(search), "tool_timeout_sec"); v != 120 {
		t.Errorf("Codex's search entry: %v", v)
	}
	if _, ok := valueOf((&mcpFile{Format: fmtGrok}).encode(search), "tool_timeout_sec"); ok {
		t.Error("Grok, whose own wait is 6000s, was given search's 120")
	}
}

// The timeout follows the Library: set, changed and taken off again, in
// the user-wide file of each agent given the server.
func TestCallTimeoutFollowsLibrary(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".qwen", "settings.json"), "{}")
	srv := Server{Name: "render", Transport: "stdio", Command: "npx", Args: []string{"-y", "render-mcp"}, Agents: []string{"claude", "codex", "gemini", "qwen"}}
	ok(t)(SaveServer("", srv))
	keys := map[string]string{"claude": "timeout", "codex": "tool_timeout_sec", "gemini": "timeout", "qwen": "timeout"}
	per := map[string]float64{"claude": 1000, "codex": 1, "gemini": 1000, "qwen": 1000}
	for id, k := range keys {
		if e := entryOf(t, id, "render"); e == nil || e[k] != nil {
			t.Fatalf("%s: no call timeout set, yet %v", id, e)
		}
	}
	for _, n := range []int{240, 75} {
		srv.CallTimeout = n
		ok(t)(SaveServer("render", srv))
		for id, k := range keys {
			if e := entryOf(t, id, "render"); !isNumber(e[k], float64(n)*per[id]) {
				t.Fatalf("%s: %s = %v, want %v", id, k, e[k], float64(n)*per[id])
			}
		}
	}
	srv.CallTimeout = 0
	ok(t)(SaveServer("render", srv))
	for id, k := range keys {
		if e := entryOf(t, id, "render"); e == nil || e[k] != nil {
			t.Errorf("%s: the timeout taken off stays: %v", id, e)
		}
	}
	if s := read(t, filepath.Join(h, ".claude.json")); strings.Contains(s, "callTimeout") {
		t.Errorf("magpie's own field reached Claude Code:\n%s", s)
	}

	for _, n := range []int{-1, maxStartupTimeout + 1} {
		srv.CallTimeout = n
		if _, err := SaveServer("render", srv); err == nil {
			t.Errorf("a call timeout of %d was saved", n)
		}
	}
}

// A tool timeout the user wrote in an agent's entry is never written over:
// one there before magpie had any, and one of magpie's changed by hand.
// Once it is taken out, magpie's goes in again.
func TestCallTimeoutKeepsUsers(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude.json")
	srv := Server{Name: "render", Transport: "stdio", Command: "npx", Agents: []string{"claude"}}
	ok(t)(SaveServer("", srv))
	set := func(v any) {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal([]byte(read(t, claude)), &doc); err != nil {
			t.Fatal(err)
		}
		e := doc["mcpServers"].(map[string]any)["render"].(map[string]any)
		if v == nil {
			delete(e, "timeout")
		} else {
			e["timeout"] = v
		}
		b, _ := json.Marshal(doc)
		write(t, claude, string(b))
	}
	want := func(n float64) {
		t.Helper()
		if e := entryOf(t, "claude", "render"); !isNumber(e["timeout"], n) {
			t.Fatalf("timeout = %v, want %v:\n%s", e["timeout"], n, read(t, claude))
		}
	}

	// the user's, before magpie had one
	set(400000)
	srv.CallTimeout = 120
	ok(t)(SaveServer("render", srv))
	want(400000)

	// taken out by hand: magpie's goes in, and follows the Library
	set(nil)
	ok(t)(Sync())
	want(120000)
	srv.CallTimeout = 180
	ok(t)(SaveServer("render", srv))
	want(180000)

	// magpie's changed by hand is the user's from then on
	set(700000)
	srv.CallTimeout = 30
	ok(t)(SaveServer("render", srv))
	want(700000)
	srv.CallTimeout = 0
	ok(t)(SaveServer("render", srv))
	want(700000)
}

// The hub's entry waits longer than the longest call timeout among the
// servers behind it, so the hub's own error, which names the server, comes
// first; with none longer than the hub's own wait it has no timeout, and
// a value the user wrote there stays.
func TestHubEntryCallTimeout(t *testing.T) {
	h := hubSandbox(t)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}, Agents: []string{"claude", "codex"}}))
	render := Server{Name: "render", Transport: "stdio", Command: "npx", Args: []string{"-y", "render-mcp"}, Agents: []string{"claude"}}
	ok(t)(SaveServer("", render))
	ok(t)(SetMCPHub(true))
	if e := entryOf(t, "claude", HubEntry); e == nil || e["timeout"] != nil {
		t.Fatalf("the hub's entry, no call timeout set: %v", e)
	}

	// shorter than the hub's own wait: the agent's own stays
	render.CallTimeout = int(gateway.HubCallWait().Seconds()) - 10
	ok(t)(SaveServer("render", render))
	if e := entryOf(t, "claude", HubEntry); e["timeout"] != nil {
		t.Fatalf("a call timeout under the hub's own gave its entry %v", e["timeout"])
	}

	render.CallTimeout = 600
	ok(t)(SaveServer("render", render))
	if e := entryOf(t, "claude", HubEntry); !isNumber(e["timeout"], (600+hubCallMargin)*1000) {
		t.Fatalf("claude's hub entry: timeout = %v", e["timeout"])
	}
	// Codex isn't given render through the hub: its hub entry is fs's alone
	if e := entryOf(t, "codex", HubEntry); e == nil || e["tool_timeout_sec"] != nil {
		t.Fatalf("codex's hub entry, given no server with a call timeout: %v", e)
	}

	render.CallTimeout = 0
	ok(t)(SaveServer("render", render))
	if e := entryOf(t, "claude", HubEntry); e == nil || e["timeout"] != nil {
		t.Fatalf("the hub's timeout stays once no server has one: %v", e)
	}

	// the user's own on the hub's entry
	claude := filepath.Join(h, ".claude.json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(read(t, claude)), &doc); err != nil {
		t.Fatal(err)
	}
	doc["mcpServers"].(map[string]any)[HubEntry].(map[string]any)["timeout"] = 3600000
	b, _ := json.Marshal(doc)
	write(t, claude, string(b))
	render.CallTimeout = 900
	ok(t)(SaveServer("render", render))
	if e := entryOf(t, "claude", HubEntry); !isNumber(e["timeout"], 3600000) {
		t.Fatalf("the user's timeout on the hub's entry became %v", e["timeout"])
	}
}

// The hub's tools carry their server's call timeout, for the hub to wait
// that long for them.
func TestHubToolsCarryCallTimeout(t *testing.T) {
	hubSandbox(t)
	fake := fakeStdio(t, "ok", nil)
	fake.Agents = []string{"claude"}
	fake.CallTimeout = 420
	ok(t)(SaveServer("", *fake))
	ok(t)(SetMCPHub(true))
	ts := theHub{}.Tools(t.Context(), "claude")
	if len(ts) == 0 {
		t.Fatal("no tools")
	}
	for _, x := range ts {
		if x.Wait.Seconds() != 420 {
			t.Errorf("%s/%s waits %v", x.Server, x.Tool, x.Wait)
		}
	}
}

// An agent whose key isn't its format's is given its own: OmO's
// requestTimeoutMs (a key it doesn't know refuses its whole mcp.json, Pi's
// timeout too), AtomCode's timeout_ms; and dsh's goes in the plugin's
// config.
func TestCallTimeoutOwnKeys(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".omo", "agent", "settings.json"), "{}")
	holdCLI(t, h, "omo")
	write(t, filepath.Join(h, ".atomcode", "config.toml"), "")
	holdCLI(t, h, "atomcode")
	patch := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	write(t, patch, "[]\n")
	for _, id := range []string{"omo", "atomcode", "dsh"} {
		if targetByID(id) == nil {
			t.Fatalf("no %s in the sandbox", id)
		}
	}
	ok(t)(SaveServer("", Server{Name: "render", Transport: "stdio", Command: "npx", Args: []string{"render-mcp"},
		CallTimeout: 300, Agents: []string{"omo", "atomcode", "dsh"}}))
	if e := entryOf(t, "omo", "render"); !isNumber(e["requestTimeoutMs"], 300000) || e["timeout"] != nil {
		t.Errorf("OmO: %v", e)
	}
	if e := entryOf(t, "atomcode", "render"); !isNumber(e["timeout_ms"], 300000) || e["timeout"] != nil {
		t.Errorf("AtomCode: %v", e)
	}
	if s := read(t, patch); !strings.Contains(s, "toolCallTimeoutMs: 300000") {
		t.Errorf("dsh:\n%s", s)
	}
	ok(t)(SaveServer("render", Server{Name: "render", Transport: "stdio", Command: "npx", Args: []string{"render-mcp"},
		Agents: []string{"omo", "atomcode", "dsh"}}))
	if e := entryOf(t, "omo", "render"); e == nil || e["requestTimeoutMs"] != nil {
		t.Errorf("OmO kept the timeout taken off: %v", e)
	}
	if s := read(t, patch); strings.Contains(s, "toolCallTimeoutMs") {
		t.Errorf("dsh kept the timeout taken off:\n%s", s)
	}
}
