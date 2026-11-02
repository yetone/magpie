package library

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
)

// hubSandbox is a sandbox with the hub's servers ended when the test is.
func hubSandbox(t *testing.T) string {
	h := sandbox(t)
	t.Cleanup(hubStopAll)
	return h
}

func hubText(t *testing.T, res json.RawMessage) string {
	t.Helper()
	var r struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(res, &r); err != nil || len(r.Content) == 0 {
		t.Fatalf("result %s: %v", res, err)
	}
	return r.Content[0].Text
}

func hubNames(ts []gateway.HubTool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Server+"/"+t.Tool)
	}
	slices.Sort(out)
	return out
}

// The hub lists the tools of the servers ticked for an agent, but those
// turned off, and runs each call on the server it is of: a command once
// for every agent, a URL in its session.
func TestHubToolsAndCalls(t *testing.T) {
	hubSandbox(t)
	var seen http.Header
	srv := httptest.NewServer(mcpHandler(t, false, &seen))
	t.Cleanup(srv.Close)
	fake := fakeStdio(t, "ok", nil)
	fake.Agents = []string{"claude", "codex"}
	fake.DisabledTools = []string{"b"}
	ok(t)(SaveServer("", *fake))
	ok(t)(SaveServer("", Server{Name: "web", Transport: "http", URL: srv.URL, Agents: []string{"claude"}}))
	ctx := context.Background()
	h := theHub{}

	if h.On() || len(h.Tools(ctx, "claude")) != 0 {
		t.Fatal("the hub lists tools while it is off")
	}
	ok(t)(SetMCPHub(true))
	if !h.On() {
		t.Fatal("the hub isn't on")
	}
	if got, want := hubNames(h.Tools(ctx, "claude")), []string{"fake/a", "fake/c", "web/a", "web/b"}; !slices.Equal(got, want) {
		t.Fatalf("claude's tools %v, want %v (fake's b is turned off)", got, want)
	}
	if got, want := hubNames(h.Tools(ctx, "codex")), []string{"fake/a", "fake/c"}; !slices.Equal(got, want) {
		t.Fatalf("codex's tools %v, want %v", got, want)
	}
	if got := h.Tools(ctx, "gemini"); len(got) != 0 {
		t.Fatalf("gemini, given no server, lists %v", got)
	}
	// any other app (agent "") is given every server in the library, one
	// ticked for no agent too
	ok(t)(SaveServer("", Server{Name: "lone", Transport: "http", URL: srv.URL}))
	if got, want := hubNames(h.Tools(ctx, "")), []string{"fake/a", "fake/c", "lone/a", "lone/b", "web/a", "web/b"}; !slices.Equal(got, want) {
		t.Fatalf("another app's tools %v, want %v", got, want)
	}
	if got := hubNames(h.Tools(ctx, "claude")); slices.Contains(got, "lone/a") {
		t.Fatalf("claude is given lone, ticked for no agent: %v", got)
	}
	if res, err := h.Call(ctx, "", "lone", "b", json.RawMessage(`{}`)); err != nil || !strings.HasPrefix(hubText(t, res), "remote ") {
		t.Fatalf("lone's b for another app: %s %v", res, err)
	}
	if _, err := h.Call(ctx, "", "nope", "a", json.RawMessage(`{}`)); err == nil || err.Error() != "it isn't in magpie's Library" {
		t.Errorf("a server not in the library, for another app: %v", err)
	}
	if _, err := h.Call(ctx, "", "fake", "b", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Errorf("a tool turned off ran for another app: %v", err)
	}
	ok(t)(RemoveServer("lone"))

	res, err := h.Call(ctx, "claude", "fake", "a", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	first := hubText(t, res)
	if !strings.HasPrefix(first, `a {"x":1} pid=`) {
		t.Fatalf("fake's a answered %q", first)
	}
	// one process for every agent
	res, err = h.Call(ctx, "codex", "fake", "c", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if pid := strings.SplitAfter(hubText(t, res), "pid=")[1]; pid != strings.SplitAfter(first, "pid=")[1] {
		t.Errorf("codex's call ran in pid %s, claude's in %s: not one server for both", pid, first)
	}
	res, err = h.Call(ctx, "claude", "web", "b", json.RawMessage(`{}`))
	if err != nil || hubText(t, res) != "remote s-1" {
		t.Fatalf("web's b: %s %v", res, err)
	}
	if _, err := h.Call(ctx, "claude", "fake", "b", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Errorf("a tool turned off ran: %v", err)
	}
	if _, err := h.Call(ctx, "codex", "web", "a", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "isn't switched on for codex") {
		t.Errorf("a server not ticked for codex ran: %v", err)
	}

	// off, nothing is listed and the servers are ended
	ok(t)(SetMCPHub(false))
	if len(h.Tools(ctx, "claude")) != 0 {
		t.Error("the hub lists tools after it was turned off")
	}
	hubPool.Lock()
	n := len(hubPool.m)
	hubPool.Unlock()
	if n != 0 {
		t.Errorf("%d servers still held after the hub was turned off", n)
	}
}

// A server that dies, or doesn't answer, is an error naming why, and the
// next call starts it again; one left unused is ended, and started again
// when next called.
func TestHubServerDiesAndIdles(t *testing.T) {
	hubSandbox(t)
	fake := fakeStdio(t, "ok", nil)
	fake.Agents = []string{"claude"}
	ok(t)(SaveServer("", *fake))
	ok(t)(SetMCPHub(true))
	h := theHub{}
	ctx := context.Background()
	pid := func() string {
		t.Helper()
		res, err := h.Call(ctx, "claude", "fake", "a", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		return strings.SplitAfter(hubText(t, res), "pid=")[1]
	}
	p1 := pid()

	_, err := h.Call(ctx, "claude", "fake", "die", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "exited (5)") || !strings.Contains(err.Error(), "lost the database") {
		t.Fatalf("a server that died answered %v, want its exit and last line", err)
	}
	p2 := pid()
	if p2 == p1 {
		t.Fatalf("the call after it died ran in the dead server's pid %s", p1)
	}

	// a call it never answers ends at the caller's deadline, and the
	// server is still used after
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	_, err = h.Call(short, "claude", "fake", "slow", json.RawMessage(`{}`))
	cancel()
	if err == nil || !strings.Contains(err.Error(), "didn't answer in time") {
		t.Fatalf("a call never answered gave %v", err)
	}
	if p := pid(); p != p2 {
		t.Errorf("a call that timed out restarted the server (%s, then %s)", p2, p)
	}

	// idle: ended, then started again
	idle := hubIdle
	hubIdle = 50 * time.Millisecond
	t.Cleanup(func() { hubIdle = idle })
	if len(h.Tools(ctx, "claude")) != 3 {
		t.Fatal("fake's tools not listed")
	}
	pid()
	b := backendOf(fake)
	for end := time.Now().Add(10 * time.Second); ; {
		b.mu.Lock()
		gone := b.conn == nil
		b.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(end) {
			t.Fatal("an idle server wasn't ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// what it listed is kept: a list doesn't start it again
	hubIdle = idle
	if len(h.Tools(ctx, "claude")) != 3 {
		t.Fatal("fake's tools not listed after the idle stop")
	}
	b.mu.Lock()
	started := b.conn != nil || b.ready != nil
	b.mu.Unlock()
	if started {
		t.Error("listing the tools started an idle server again")
	}
	if p := pid(); p == p2 {
		t.Errorf("the call after the idle stop ran in the ended server's pid %s", p)
	}
}

// With the hub on, an agent is given magpie's one entry in place of the
// servers behind it; off, each server's own again. An agent with a
// "magpie" of its own, or one that can't reach a URL, keeps each server's.
func TestHubEntrySwap(t *testing.T) {
	h := hubSandbox(t)
	claude := filepath.Join(h, ".claude.json")
	write(t, claude, `{"numStartups": 3, "mcpServers": {"mine": {"command": "x"}}}`)
	codex := filepath.Join(h, ".codex/config.toml")
	write(t, codex, "[mcp_servers.magpie]\ncommand = \"their-own\"\n")
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}, Agents: []string{"claude", "codex"}}))
	ok(t)(SaveServer("", Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Agents: []string{"claude"}}))
	entries := func(id string) map[string]map[string]any {
		t.Helper()
		e, err := targetByID(id).MCP.entries()
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	names := func(id string) []string {
		var out []string
		for k := range entries(id) {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	if got := names("claude"); !slices.Equal(got, []string{"fs", "mine", "web"}) {
		t.Fatalf("claude before: %v", got)
	}

	r, err := SetMCPHub(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.Problems {
		if p.Agent == "claude" {
			t.Errorf("problem: %+v", p)
		}
	}
	if got := names("claude"); !slices.Equal(got, []string{"magpie", "mine"}) {
		t.Fatalf("claude with the hub on: %v\n%s", got, read(t, claude))
	}
	if u, _ := entries("claude")["magpie"]["url"].(string); !strings.HasSuffix(u, gateway.HubMCPPath+"claude") {
		t.Errorf("claude's magpie entry: %v", entries("claude")["magpie"])
	}
	// codex has a magpie of its own: left as it is, its servers kept
	if got := names("codex"); !slices.Equal(got, []string{"fs", "magpie"}) || entries("codex")["magpie"]["command"] != "their-own" {
		t.Fatalf("codex with the hub on: %v\n%s", got, read(t, codex))
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.MCPHub || v.HubPath != gateway.HubMCPPath {
		t.Errorf("view: mcpHub %v, hubPath %q", v.MCPHub, v.HubPath)
	}
	for _, a := range v.Agents {
		switch a.ID {
		case "claude":
			if a.Hub != "on" {
				t.Errorf("claude's hub %q, want on", a.Hub)
			}
		case "codex":
			if a.Hub != hubOwn {
				t.Errorf("codex's hub %q, want %q", a.Hub, hubOwn)
			}
		}
	}

	ok(t)(SetMCPHub(false))
	if got := names("claude"); !slices.Equal(got, []string{"fs", "mine", "web"}) {
		t.Fatalf("claude with the hub off again: %v\n%s", got, read(t, claude))
	}
	if !strings.Contains(read(t, claude), `"numStartups": 3`) {
		t.Errorf("claude's own keys lost:\n%s", read(t, claude))
	}
	if got := names("codex"); !slices.Equal(got, []string{"fs", "magpie"}) || entries("codex")["magpie"]["command"] != "their-own" {
		t.Fatalf("codex with the hub off: %v\n%s", got, read(t, codex))
	}
}

// An agent given none of the hub's servers has no magpie entry and no
// state; the Connect sheet's Connect gives it every server
// (EveryServerAgents, servers/agents-all), and it then has the hub's one
// entry and says on.
func TestHubConnectAnAgent(t *testing.T) {
	h := hubSandbox(t)
	claude := filepath.Join(h, ".claude.json")
	write(t, claude, `{"mcpServers": {}}`)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}}))
	ok(t)(SetMCPHub(true))
	hub := func() string {
		t.Helper()
		v, err := Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range v.Agents {
			if a.ID == "claude" {
				return a.Hub
			}
		}
		t.Fatal("no claude in the view")
		return ""
	}
	if got := hub(); got != "" {
		t.Fatalf("claude given nothing: hub %q", got)
	}
	if strings.Contains(read(t, claude), `"magpie"`) {
		t.Fatalf("claude given nothing has magpie:\n%s", read(t, claude))
	}
	ok(t)(EveryServerAgents([]string{"claude"}, true))
	if got := hub(); got != "on" {
		t.Fatalf("claude connected: hub %q", got)
	}
	e, err := targetByID("claude").MCP.entries()
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := e["magpie"]["url"].(string); !strings.HasSuffix(u, gateway.HubMCPPath+"claude") || e["fs"] != nil {
		t.Fatalf("claude connected:\n%s", read(t, claude))
	}
}

// A library server called magpie keeps every agent on each server's own
// entry, said why.
func TestHubNameTaken(t *testing.T) {
	hubSandbox(t)
	ok(t)(SaveServer("", Server{Name: "magpie", Transport: "stdio", Command: "npx", Args: []string{"x"}, Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))
	e, err := targetByID("claude").MCP.entries()
	if err != nil {
		t.Fatal(err)
	}
	if e["magpie"]["command"] != "npx" {
		t.Errorf("the library's own magpie was replaced: %v", e["magpie"])
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if a.ID == "claude" && a.Hub != hubTaken {
			t.Errorf("claude's hub %q, want %q", a.Hub, hubTaken)
		}
	}
}

// A server the library no longer gives is ended by the next prune, though
// no agent asks the hub again: ticked for no agent, edited, or the switch
// turned off by another magpie (here a change that doesn't end them
// itself, as the CLI's doesn't end the gateway's).
func TestHubPrune(t *testing.T) {
	hubSandbox(t)
	fake := fakeStdio(t, "ok", nil)
	fake.Agents = []string{"claude"}
	ok(t)(SaveServer("", *fake))
	ok(t)(SetMCPHub(true))
	h := theHub{}
	ctx := context.Background()
	held := func() hubConn {
		t.Helper()
		hubPool.Lock()
		b := hubPool.m[fake.Name]
		hubPool.Unlock()
		if b == nil {
			return nil
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.conn
	}
	run := func() hubConn {
		t.Helper()
		if _, err := h.Call(ctx, "claude", fake.Name, "a", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		c := held()
		if c == nil {
			t.Fatal("no server held after a call")
		}
		return c
	}

	// ended: its output closed once its process went (the reader marks it)
	ended := func(c hubConn) bool {
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if !c.alive() {
				return true
			}
		}
		return false
	}

	c := run()
	hubPrune()
	if held() != c || !c.alive() {
		t.Fatal("a server the library still gives was ended")
	}

	// ticked for no agent, it is still given to other apps (HubMCPAll)
	ok(t)(ServerAgents(fake.Name, nil))
	hubPrune()
	if held() != c || !c.alive() {
		t.Fatal("a server ticked for no agent, still given to other apps, was ended")
	}
	ok(t)(ServerAgents(fake.Name, []string{"claude"}))

	edited := *fake
	edited.Env = map[string]string{"EDITED": "1"}
	for k, v := range fake.Env {
		edited.Env[k] = v
	}
	ok(t)(SaveServer(fake.Name, edited))
	hubPrune()
	if held() != nil || !ended(c) {
		t.Fatal("a server edited since it was started is still running")
	}

	c = run()
	removed := *fake
	ok(t)(RemoveServer(fake.Name))
	hubPrune()
	if held() != nil || !ended(c) {
		t.Fatal("a server taken out of the library is still running")
	}

	ok(t)(SaveServer("", removed))
	c = run()
	ok(t)(change(func(l *Library) error { l.MCPHub = false; return nil }))
	hubPrune()
	if held() != nil || !ended(c) {
		t.Fatal("the hub was turned off and its server is still running")
	}
}

// An agent that reaches the gateway from another machine (a WSL distro
// under NAT, a computer on the LAN) is given the hub at that address, with
// the LAN key while sharing is on; one on this machine, with no key.
func TestHubEntryFromAnotherMachine(t *testing.T) {
	hubSandbox(t)
	const there = "http://172.20.0.1:3425"
	at := func(gw string) *Server {
		return (&Library{}).hubServerFor(&Target{Agent: &agent.Agent{ID: "claude", Gateway: func() string { return gw }}})
	}
	if s := at(there); s.URL != there+gateway.HubMCPPath+"claude" || len(s.Headers) != 0 {
		t.Errorf("nothing shared: %+v", s)
	}
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { access.ConfigureLAN(false, false) })
	lan := access.LANSecret()
	if !strings.HasPrefix(lan, access.Prefix) {
		t.Fatalf("LAN key %q", lan)
	}
	if s := at(there + "/"); s.URL != there+gateway.HubMCPPath+"claude" || s.Headers["Authorization"] != "Bearer "+lan {
		t.Errorf("from another machine: %+v", s)
	}
	if s := at("http://127.0.0.1:3425"); len(s.Headers) != 0 {
		t.Errorf("on this machine: %+v", s)
	}
}

// HubLink is each app's own install link for the hub at this computer's
// address, and an error for an app with none.
func TestHubLink(t *testing.T) {
	hubSandbox(t)
	want := strings.TrimRight(gateway.URL(), "/") + gateway.HubMCPAll
	link, err := HubLink("cursor")
	if err != nil {
		t.Fatal(err)
	}
	const cursor = "cursor://anysphere.cursor-deeplink/mcp/install?name=magpie&config="
	if !strings.HasPrefix(link, cursor) {
		t.Fatalf("cursor's link %q", link)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, cursor))
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]string
	if err := json.Unmarshal(raw, &c); err != nil || len(c) != 1 || c["url"] != want {
		t.Fatalf("cursor's config %s (%v), want url %s", raw, err, want)
	}

	link, err = HubLink("vscode")
	if err != nil {
		t.Fatal(err)
	}
	q, found := strings.CutPrefix(link, "vscode:mcp/install?")
	if !found {
		t.Fatalf("vscode's link %q", link)
	}
	q, err = url.QueryUnescape(q)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(q), &v); err != nil || v["name"] != "magpie" || v["type"] != "http" || v["url"] != want || len(v) != 3 {
		t.Fatalf("vscode's config %s (%v), want magpie at %s", q, err, want)
	}

	if _, err := HubLink("javascript:alert(1)"); err == nil {
		t.Fatal("an app with no install link was given one")
	}
}

// A server kept direct (#4382, cuipengcx90) stays each agent's own entry
// with the hub on, written as with the hub off, beside magpie's for the
// rest; switching it either way writes the agents again, the entry it had
// the other way gone, and the hub neither lists nor runs it.
func TestHubDirectServer(t *testing.T) {
	h := hubSandbox(t)
	claude := filepath.Join(h, ".claude.json")
	write(t, claude, `{"mcpServers": {"mine": {"command": "x"}}}`)
	codex := filepath.Join(h, ".codex/config.toml")
	write(t, codex, "")
	ok(t)(SaveServer("", Server{Name: "index", Transport: "stdio", Command: "npx", Args: []string{"-y", "indexer"}, Env: map[string]string{"K": "v"}, Agents: []string{"claude", "codex"}, Hub: "direct"}))
	ok(t)(SaveServer("", Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Agents: []string{"claude", "codex"}, Hub: "shared"}))
	entries := func(id string) map[string]map[string]any {
		t.Helper()
		e, err := targetByID(id).MCP.entries()
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	names := func(id string) []string {
		var out []string
		for k := range entries(id) {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	off := map[string]string{} // each agent's index entry with the hub off
	for _, id := range []string{"claude", "codex"} {
		b, _ := json.Marshal(entries(id)["index"])
		off[id] = string(b)
	}
	if l, err := load(); err != nil || l.server("web").Hub != "" || l.server("index").Hub != HubDirect {
		t.Fatalf("saved: %v %+v", err, l)
	}

	ok(t)(SetMCPHub(true))
	for id, want := range map[string][]string{"claude": {"index", "magpie", "mine"}, "codex": {"index", "magpie"}} {
		if got := names(id); !slices.Equal(got, want) {
			t.Fatalf("%s with the hub on: %v, want %v\n%s\n%s", id, got, want, read(t, claude), read(t, codex))
		}
		if b, _ := json.Marshal(entries(id)["index"]); string(b) != off[id] {
			t.Errorf("%s's direct entry %s, with the hub off %s", id, b, off[id])
		}
	}
	for _, id := range []string{"claude", ""} {
		var got []string
		for _, s := range hubServers(id) {
			got = append(got, s.Name)
		}
		if !slices.Equal(got, []string{"web"}) {
			t.Errorf("the hub serves %q: %v, want only web", id, got)
		}
	}

	// shared again: behind the hub, its own entry gone
	ok(t)(ServerHub("index", false))
	for _, id := range []string{"claude", "codex"} {
		if got := names(id); slices.Contains(got, "index") || !slices.Contains(got, "magpie") {
			t.Fatalf("%s with index shared: %v\n%s\n%s", id, got, read(t, claude), read(t, codex))
		}
	}
	// every server direct: the hub's entry goes, each written as with it off
	ok(t)(ServerHub("index", true))
	ok(t)(ServerHub("web", true))
	if got := names("claude"); !slices.Equal(got, []string{"index", "mine", "web"}) {
		t.Fatalf("claude with every server direct: %v\n%s", got, read(t, claude))
	}
	if got := names("codex"); !slices.Equal(got, []string{"index", "web"}) {
		t.Fatalf("codex with every server direct: %v\n%s", got, read(t, codex))
	}
	if _, err := ServerHub("nope", true); err == nil {
		t.Error("a server not in the library was set")
	}
	if _, err := SaveServer("", Server{Name: "odd", Transport: "stdio", Command: "x", Hub: "session"}); err == nil {
		t.Error("an unknown hub policy was saved")
	}
}

// magpie's own image server is in the hub too: listed as a tool that
// writes into the agent's project (Roots), and each call made with the
// agent's roots and name in _meta, an empty list when it has none here,
// so it saves into that agent's project. The agent's file has the hub's
// entry and not the server's own.
// An agent is told what each server behind the hub is for, from the
// market: the featured server it is by its address whatever it is named,
// the featured one it is named after, and nothing made up for the rest.
func TestHubSaysWhatServersAreFor(t *testing.T) {
	hubSandbox(t)
	ok(t)(SaveServer("", Server{Name: "wiki", Transport: "http", URL: "https://mcp.deepwiki.com/mcp/", Agents: []string{"pi"}}))
	c7 := fakeStdio(t, "ok", nil)
	c7.Name, c7.Agents = "context7", []string{"pi"}
	ok(t)(SaveServer("", *c7))
	notes := fakeStdio(t, "ok", nil)
	notes.Name, notes.Agents = "notes", []string{"pi", "claude"}
	ok(t)(SaveServer("", *notes))
	ok(t)(SetMCPHub(true))
	m, _ := marketServer("deepwiki")
	c, _ := marketServer("context7")
	want := []gateway.HubServer{{Name: "context7", About: c.Description}, {Name: "notes"}, {Name: "wiki", About: m.Description}}
	if got := (theHub{}).Servers("pi"); !reflect.DeepEqual(got, want) || m.Description == "" || c.Description == "" {
		t.Fatalf("pi's servers %+v, want %+v", got, want)
	}
	if got := (theHub{}).Servers("claude"); !reflect.DeepEqual(got, []gateway.HubServer{{Name: "notes"}}) {
		t.Fatalf("claude's servers %+v", got)
	}
}

// Pi writes its first prompt before it has connected to the hub (it waits
// for no codemode server), so the hub entry in its mcp.json says what the
// hub serves as its description, the line Pi shows of a server, and the
// line follows the servers ticked. Another entry's description is the
// user's.
func TestHubPiDescription(t *testing.T) {
	h := hubSandbox(t)
	setPiVersion(t, "1.4.0")
	file := filepath.Join(h, ".pi/agent/mcp.json")
	write(t, file, `{"mcpServers": {"theirs": {"command": "t", "description": "My own."}}}`)
	ok(t)(SaveServer("", Server{Name: "wiki", Transport: "http", URL: "https://mcp.deepwiki.com/mcp", Agents: []string{"pi"}}))
	ok(t)(SetMCPHub(true))
	about := func() string {
		t.Helper()
		d, _ := piServers(t, file)[HubEntry]["description"].(string)
		return d
	}
	wiki, _ := marketServer("deepwiki")
	if d := about(); d != gateway.HubLine([]gateway.HubServer{{Name: "wiki", About: wiki.Description}}) || !strings.Contains(d, "GitHub repository") {
		t.Fatalf("the hub entry's description %q:\n%s", d, read(t, file))
	}
	ok(t)(SaveServer("", Server{Name: "context7", Transport: "http", URL: "https://mcp.context7.com/mcp", Agents: []string{"pi"}}))
	if d := about(); !strings.Contains(d, "context7 (") || !strings.Contains(d, "wiki (") {
		t.Fatalf("context7 ticked: %q", d)
	}
	// one edited by hand is magpie's again on the next sync
	write(t, file, strings.Replace(read(t, file), "Tools of", "Tools from", 1))
	ok(t)(Sync())
	if d := about(); !strings.HasPrefix(d, "Tools of context7") {
		t.Fatalf("after a sync: %q", d)
	}
	if d, _ := piServers(t, file)["theirs"]["description"].(string); d != "My own." {
		t.Fatalf("the user's description: %q", d)
	}
}

func TestHubGivesMagpieImage(t *testing.T) {
	h := hubSandbox(t)
	claude := filepath.Join(h, ".claude.json")
	write(t, claude, `{"mcpServers": {}}`)
	img := fakeStdio(t, "ok", nil)
	img.Name, img.Args, img.Agents = selfServerName, []string{"mcp", "image"}, []string{"claude"}
	ok(t)(SaveServer("", *img))
	ok(t)(SetMCPHub(true))
	hub := theHub{}
	ctx := context.Background()

	tools := hub.Tools(ctx, "claude")
	if got := hubNames(tools); !slices.Equal(got, []string{"magpie-image/a", "magpie-image/b", "magpie-image/c"}) {
		t.Fatalf("claude's tools %v, want magpie-image's", got)
	}
	for _, x := range tools {
		if !x.Roots {
			t.Errorf("%s/%s isn't said to write into the project", x.Server, x.Tool)
		}
	}
	e, err := targetByID("claude").MCP.entries()
	if err != nil {
		t.Fatal(err)
	}
	if e["magpie"] == nil || e[selfServerName] != nil {
		t.Fatalf("claude's file:\n%s", read(t, claude))
	}

	res, err := hub.Call(gateway.WithHubCaller(ctx, gateway.HubCaller{Roots: []string{"file:///work/proj"}, Client: "claude-code/2.1.314"}), "claude", selfServerName, "a", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := hubText(t, res); !strings.Contains(got, `meta={"magpie/client":"claude-code/2.1.314","magpie/roots":["file:///work/proj"]}`) {
		t.Errorf("called as %q", got)
	}
	res, err = hub.Call(ctx, "claude", selfServerName, "a", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := hubText(t, res); !strings.Contains(got, `meta={"magpie/roots":[]}`) {
		t.Errorf("no roots: called as %q", got)
	}
}

// The router runs one Magpie Browser for every agent behind it, so each
// call names its caller's session in _meta, for the browser to keep each
// caller in a tab of its own; another server is sent no session, and a
// call with none (a caller not told apart) is sent none.
func TestHubTellsMagpieBrowserItsCaller(t *testing.T) {
	h := hubSandbox(t)
	write(t, filepath.Join(h, ".claude.json"), `{"mcpServers": {}}`)
	br := fakeStdio(t, "ok", nil)
	br.Name, br.Args, br.Agents = browserServerName, []string{"mcp", "browser"}, []string{"claude"}
	ok(t)(SaveServer("", *br))
	other := fakeStdio(t, "ok", nil)
	other.Agents = []string{"claude"}
	ok(t)(SaveServer("", *other))
	ok(t)(SetMCPHub(true))
	hub := theHub{}
	ctx := context.Background()

	for _, s := range []string{"hub-claude-one", "hub-claude-two"} {
		res, err := hub.Call(gateway.WithHubCaller(ctx, gateway.HubCaller{Session: s}), "claude", browserServerName, "a", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := hubText(t, res); !strings.Contains(got, `meta={"magpie/session":"`+s+`"}`) {
			t.Errorf("browser called as %q, want session %s", got, s)
		}
	}
	res, err := hub.Call(gateway.WithHubCaller(ctx, gateway.HubCaller{Session: "hub-claude-one"}), "claude", "fake", "a", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := hubText(t, res); strings.Contains(got, "meta=") {
		t.Errorf("another server was sent the session: %q", got)
	}
	res, err = hub.Call(ctx, "claude", browserServerName, "a", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := hubText(t, res); strings.Contains(got, "meta=") {
		t.Errorf("no session: called as %q", got)
	}
}

// Another magpie's router is sent the session too, to pass on to its
// Magpie Browser; any other URL isn't.
func TestKeepsSession(t *testing.T) {
	for _, c := range []struct {
		s    Server
		want bool
	}{
		{Server{Transport: "stdio", Command: "/Applications/Magpie.app/Contents/MacOS/magpie", Args: []string{"mcp", "browser"}}, true},
		{Server{Transport: "stdio", Command: "magpie", Args: []string{"mcp", "image"}}, false},
		{Server{Transport: "http", URL: "http://10.0.0.2:3425/mcp/magpie/hub"}, true},
		{Server{Transport: "http", URL: "http://10.0.0.2:3425/mcp/magpie/hub/claude"}, true},
		{Server{Transport: "http", URL: "https://mcp.example.com/mcp"}, false},
		{Server{Transport: "http", URL: "https://example.com/x/mcp/magpie/hub"}, false},
	} {
		if got := keepsSession(&c.s); got != c.want {
			t.Errorf("%+v: %v, want %v", c.s, got, c.want)
		}
	}
}
