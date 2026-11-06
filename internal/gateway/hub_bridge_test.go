package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// bridgeHub is fakeHub whose Tools, as the library's, leaves out a
// Remote magpie's router for a request of another magpie's router, and
// says which requests were.
type bridgeHub struct {
	*fakeHub
	fromRouter []bool
}

func (b *bridgeHub) Tools(ctx context.Context, agent string) []HubTool {
	b.mu.Lock()
	b.fromRouter = append(b.fromRouter, FromRouter(ctx))
	b.mu.Unlock()
	ts := b.fakeHub.Tools(ctx, agent)
	if FromRouter(ctx) {
		ts = slices.DeleteFunc(slices.Clone(ts), func(t HubTool) bool { return t.Server == "home" })
	}
	return ts
}

// A request of another magpie's router (RouterHeader) is one to the
// library (FromRouter), and isn't given a server that is a Remote
// magpie's router: neither listed, nor run when named. Two magpies that
// have each other as Remote magpies don't ask each other round. Without
// the header, it is given as any server.
func TestHubLeavesRemoteRouterToARouter(t *testing.T) {
	fresh(t)
	f := &bridgeHub{fakeHub: &fakeHub{on: true,
		tools: map[string][]HubTool{"": {
			{Server: "fs", Tool: "read", Spec: hubSpec("read")},
			{Server: "home", Tool: "search", Spec: hubSpec("search")},
		}},
		servers: map[string][]HubServer{"": {{Name: "fs", Known: true}, {Name: "home", Known: true, Bridge: true}}},
	}}
	old := MCPHub
	MCPHub = f
	t.Cleanup(func() { MCPHub = old })
	h := lanGuard(New().Handler())
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	names := func(m mcpReply) []string {
		var out []string
		for _, x := range m.Result.Tools {
			out = append(out, x.Name)
		}
		slices.Sort(out)
		return out
	}

	if got := names(readMCP(t, hubPostAt(h, HubMCPAll, here, list))); !slices.Equal(got, []string{"fs__read", "home__search"}) {
		t.Fatalf("an agent's tools %v, want fs's and home's", got)
	}
	if got := names(readMCP(t, hubPostAt(h, HubMCPAll, here, list, RouterHeader, "1"))); !slices.Equal(got, []string{"fs__read"}) {
		t.Fatalf("a router's tools %v, want fs's alone", got)
	}
	if !slices.Equal(f.fromRouter, []bool{false, true}) {
		t.Errorf("the library was told %v of the two requests, want false then true", f.fromRouter)
	}
	// named, home's tool isn't run for a router (hubToolNamed asks
	// Servers), and is for an agent
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"home__search","arguments":{"q":"x"}}}`
	if m := readMCP(t, hubPostAt(h, HubMCPAll, here, call, RouterHeader, "1")); m.Error == nil {
		t.Fatalf("home's tool ran for a router: %+v", m.Result)
	}
	if ran := f.ran(); len(ran) != 0 {
		t.Fatalf("ran %v for a router", ran)
	}
	if m := readMCP(t, hubPostAt(h, HubMCPAll, here, call)); m.Error != nil || m.Result.IsError {
		t.Fatalf("home's tool for an agent: %+v", m)
	}
}

// The gateway's info says its router has a server to give ("mcp_hub")
// while the hub is on with a server that isn't a Remote magpie's router,
// for another magpie that has this one as a provider to give it a server.
func TestInfoSaysMCPHub(t *testing.T) {
	fresh(t)
	f := &fakeHub{on: true, servers: map[string][]HubServer{"": {{Name: "home", Bridge: true}}}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	says := func() bool {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v1", nil)
		r.RemoteAddr, r.Host = here, "127.0.0.1:3425"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var info struct {
			Name   string `json:"name"`
			MCPHub bool   `json:"mcp_hub"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Name != "magpie" {
			t.Fatalf("info %d: %s", w.Code, w.Body.String())
		}
		return info.MCPHub
	}
	if says() {
		t.Error("mcp_hub with only a Remote magpie's router behind it")
	}
	f.mu.Lock()
	f.servers[""] = append(f.servers[""], HubServer{Name: "fs"})
	f.mu.Unlock()
	if !says() {
		t.Error("no mcp_hub with fs behind the hub")
	}
	f.mu.Lock()
	f.on = false
	f.mu.Unlock()
	if says() {
		t.Error("mcp_hub with the hub off")
	}
}
