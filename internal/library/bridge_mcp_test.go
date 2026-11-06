package library

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// fakeRemotes has the Remote magpies in providers.json be hs, or
// providers.json unreadable with err.
type fakeRemotes struct {
	hs  []provider.RemoteHub
	err error
}

func fakeRemoteHubs(t *testing.T, hs ...provider.RemoteHub) *fakeRemotes {
	t.Helper()
	f := &fakeRemotes{hs: hs}
	old := remoteHubs
	t.Cleanup(func() { remoteHubs = old })
	remoteHubs = func() ([]provider.RemoteHub, error) { return slices.Clone(f.hs), f.err }
	return f
}

func loaded(t *testing.T) *Library {
	t.Helper()
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// A Remote magpie that says it serves its MCP router (treeman_26897 on
// Discord) is a Library server of its own: named for the provider, at its
// router with its key, kept to this computer, on for the agents given the
// hub's entry, its tools in their hub. It is asked as a router asks
// another's, and isn't given to another magpie's router. The provider's
// key changed, the server's is; edited in the Library's editor as it was,
// it stays the provider's; edited to another address, the user's own.
func TestRemoteMagpieRouterIsAServer(t *testing.T) {
	hubSandbox(t)
	var seen http.Header
	srv := httptest.NewServer(mcpHandler(t, false, &seen))
	t.Cleanup(srv.Close)
	f := fakeRemoteHubs(t, provider.RemoteHub{ID: "home", Name: "Home magpie", Root: srv.URL + "/", Key: "k1"})
	fake := fakeStdio(t, "ok", nil)
	fake.Agents = []string{"claude"}
	ok(t)(SaveServer("", *fake))
	ok(t)(SetMCPHub(true))

	if provider.RemoteHubAsk("home") {
		t.Fatal("a Remote magpie whose switch is off is asked")
	}
	ok(t)(SetRemoteMCP("home", true))
	if !provider.RemoteHubAsk("home") {
		t.Fatal("a Remote magpie switched on, not offered yet, isn't asked")
	}
	provider.RemoteHubFound("home")
	l := loaded(t)
	s := l.server("home-magpie")
	if s == nil {
		t.Fatalf("no server for the Remote magpie: %+v", l.MCP)
	}
	if s.Bridge != "home" || !s.Local || s.Transport != "http" || s.URL != srv.URL+gateway.HubMCPAll ||
		s.Headers["Authorization"] != "Bearer k1" || !slices.Equal(s.Agents, []string{"claude"}) {
		t.Fatalf("the Remote magpie's server: %+v", s)
	}
	if provider.RemoteHubAsk("home") {
		t.Error("a Remote magpie offered once is asked again")
	}

	ctx := context.Background()
	hub := theHub{}
	if got := hubNames(hub.Tools(ctx, "claude")); !slices.Contains(got, "home-magpie/a") {
		t.Fatalf("claude's hub tools %v, want home-magpie's", got)
	}
	if seen.Get(gateway.RouterHeader) == "" || seen.Get("Authorization") != "Bearer k1" {
		t.Errorf("the router asked with %v, want its key and %s", seen, gateway.RouterHeader)
	}
	if ss := hub.Servers("claude"); !slices.ContainsFunc(ss, func(s gateway.HubServer) bool { return s.Name == "home-magpie" && s.Bridge }) {
		t.Errorf("the gateway isn't told home-magpie is a Remote magpie's router: %+v", ss)
	}
	// another magpie's router isn't given it
	rctx := gateway.WithRouter(ctx)
	if got := hubNames(hub.Tools(rctx, "claude")); slices.Contains(got, "home-magpie/a") || !slices.Contains(got, "fake/a") {
		t.Fatalf("a router's tools %v, want fake's alone", got)
	}
	if _, err := hub.Call(rctx, "claude", "home-magpie", "a", json.RawMessage(`{}`)); err == nil {
		t.Error("home-magpie's tool ran for a router")
	}
	if res, err := hub.Call(ctx, "claude", "home-magpie", "a", json.RawMessage(`{}`)); err != nil || !strings.HasPrefix(hubText(t, res), "remote ") {
		t.Fatalf("home-magpie's a: %s %v", res, err)
	}

	// the provider's key changed
	f.hs[0].Key = "k2"
	ok(t)(Sync())
	if s := loaded(t).server("home-magpie"); s.Headers["Authorization"] != "Bearer k2" {
		t.Fatalf("the key changed, the server's headers: %v", s.Headers)
	}
	// edited as it was, by an editor that sends neither Bridge nor Local
	ed := *loaded(t).server("home-magpie")
	ed.Bridge, ed.Local, ed.Agents = "", false, []string{"claude", "codex"}
	ok(t)(SaveServer("home-magpie", ed))
	if s := loaded(t).server("home-magpie"); s.Bridge != "home" || !s.Local {
		t.Fatalf("edited as it was, no longer the Remote magpie's: %+v", s)
	}
	// edited to another address: the user's own, not changed again
	ed.URL = srv.URL + "/mine"
	ok(t)(SaveServer("home-magpie", ed))
	f.hs[0].Key = "k3"
	ok(t)(Sync())
	if s := loaded(t).server("home-magpie"); s.Bridge != "" || !s.Local || s.URL != srv.URL+"/mine" || s.Headers["Authorization"] != "Bearer k2" {
		t.Fatalf("edited to another address: %+v", s)
	}
}

// The server is offered once each time its switch is turned on: removed
// by the user, it isn't added again until the switch is turned on again
// (removing it turned the switch off); the provider removed and added
// again, its switch is off. Switched off for an agent, it stays off. One the user has at that magpie's router already
// is the one. The provider removed takes its server with it; one turned
// off keeps it (RemoteHubs still lists it); providers.json unreadable
// takes nothing out. A name a server has already, or the hub's own entry's,
// isn't given it.
func TestRemoteMagpieRouterOfferedOnce(t *testing.T) {
	hubSandbox(t)
	home := provider.RemoteHub{ID: "home", Name: "Home", Root: "http://192.168.1.9:3425", Key: "k"}
	f := fakeRemoteHubs(t, home)
	has := func(name string) bool { t.Helper(); return loaded(t).server(name) != nil }

	// the user's own at that magpie's router: none beside it
	ok(t)(SaveServer("", Server{Name: "office", Transport: "http", URL: "http://192.168.1.9:3425" + gateway.HubMCPPath + "claude"}))
	ok(t)(SetRemoteMCP("home", true))
	provider.RemoteHubFound("home")
	if l := loaded(t); len(l.MCP) != 1 || !slices.Contains(l.BridgesOffered, "home") {
		t.Fatalf("beside the user's own: %+v, offered %v", l.MCP, l.BridgesOffered)
	}
	ok(t)(RemoveServer("office"))
	provider.RemoteHubFound("home")
	if has("home") {
		t.Fatal("offered once already, added after the user's own was removed")
	}

	// the provider removed and added again: offered again
	f.hs = nil
	ok(t)(Sync())
	if slices.Contains(loaded(t).BridgesOffered, "home") {
		t.Fatal("the provider removed, still offered")
	}
	f.hs = []provider.RemoteHub{home}
	provider.RemoteHubFound("home")
	if has("home") || RemoteMCPOn()["home"] {
		t.Fatal("the provider added again, its switch on")
	}
	ok(t)(SetRemoteMCP("home", true))
	provider.RemoteHubFound("home")
	if !has("home") {
		t.Fatal("the provider added again and switched on, no server")
	}
	if a := loaded(t).server("home").Agents; len(a) != 0 {
		t.Errorf("with the hub off, on for %v, want none", a)
	}
	// switched on, then off, for claude: off it stays
	ok(t)(ServerAgents("home", []string{"claude"}))
	ok(t)(ServerAgents("home", nil))
	ok(t)(Sync())
	provider.RemoteHubFound("home")
	if a := loaded(t).server("home").Agents; len(a) != 0 {
		t.Errorf("switched off, on for %v", a)
	}
	// removed by the user: not back
	ok(t)(RemoveServer("home"))
	ok(t)(Sync())
	provider.RemoteHubFound("home")
	if has("home") {
		t.Fatal("a server the user removed came back")
	}
	if RemoteMCPOn()["home"] || provider.RemoteHubAsk("home") {
		t.Fatal("its server removed in the Library, the switch is still on")
	}

	// providers.json unreadable takes nothing out
	f.hs = nil
	ok(t)(Sync())
	f.hs = []provider.RemoteHub{home}
	ok(t)(SetRemoteMCP("home", true))
	provider.RemoteHubFound("home")
	f.err = errors.New("unreadable")
	f.hs = nil
	ok(t)(Sync())
	if !has("home") || !slices.Contains(loaded(t).BridgesOffered, "home") {
		t.Fatal("providers.json unreadable took the server out")
	}
	f.err = nil
	ok(t)(Sync())
	if has("home") {
		t.Fatal("the provider removed kept its server")
	}
	if l := loaded(t); len(l.BridgesOn) != 0 {
		t.Fatalf("the provider removed kept its switch: %v", l.BridgesOn)
	}

	// a provider being tried in the editor, not saved: nothing
	provider.RemoteHubFound("nope")
	if l := loaded(t); len(l.MCP) != 0 || len(l.BridgesOffered) != 0 {
		t.Fatalf("a provider not saved: %+v, offered %v", l.MCP, l.BridgesOffered)
	}

	// names: the hub's own entry's isn't given, nor one a server has
	ok(t)(SaveServer("", Server{Name: "magpie-remote", Transport: "http", URL: "https://example.com/mcp"}))
	f.hs = []provider.RemoteHub{{ID: "m", Name: "magpie", Root: "http://10.0.0.2:3425", Key: "k"}, {ID: "x", Name: "!!", Root: "http://10.0.0.3:3425", Key: "k"}}
	ok(t)(SetRemoteMCP("m", true))
	ok(t)(SetRemoteMCP("x", true))
	provider.RemoteHubFound("m")
	provider.RemoteHubFound("x")
	l := loaded(t)
	if s := l.server("magpie-remote-2"); s == nil || s.Bridge != "m" {
		t.Errorf("a Remote magpie called magpie: %+v", l.MCP)
	}
	if s := l.server("x"); s == nil || s.Bridge != "x" {
		t.Errorf("a Remote magpie whose name gives no name: %+v", l.MCP)
	}
}

// Adding a Remote magpie's router to the Library is the user's call
// (treeman_26897): with the switch off it isn't asked nor given a server;
// turned on, it is; turned off, its server is taken out, and on again it
// is offered again. A server added before the switch was there (no
// BridgesOn) is kept, the switch shown on, and the provider's key still
// reaches it.
func TestRemoteMagpieMCPIsOptIn(t *testing.T) {
	hubSandbox(t)
	f := fakeRemoteHubs(t, provider.RemoteHub{ID: "home", Name: "Home", Root: "http://192.168.1.9:3425", Key: "k"})
	has := func() bool { t.Helper(); return loaded(t).server("home") != nil }

	if provider.RemoteHubAsk("home") || RemoteMCPOn()["home"] {
		t.Fatal("switched off, asked")
	}
	provider.RemoteHubFound("home")
	if has() {
		t.Fatal("switched off, given a server")
	}
	ok(t)(SetRemoteMCP("home", true))
	if !RemoteMCPOn()["home"] {
		t.Fatal("switched on, read as off")
	}
	provider.RemoteHubFound("home")
	if !has() {
		t.Fatal("switched on, no server")
	}
	ok(t)(SetRemoteMCP("home", false))
	if has() || RemoteMCPOn()["home"] || provider.RemoteHubAsk("home") {
		t.Fatal("switched off, its server kept")
	}
	ok(t)(SetRemoteMCP("home", true))
	provider.RemoteHubFound("home")
	if !has() {
		t.Fatal("switched on again, not offered again")
	}

	// one added before the switch: a server with its Bridge, no BridgesOn
	ok(t)(change(func(l *Library) error { l.BridgesOn = nil; return nil }))
	if !has() || !RemoteMCPOn()["home"] {
		t.Fatalf("a server from before the switch: kept %v, switch on %v", has(), RemoteMCPOn()["home"])
	}
	f.hs[0].Key = "k2"
	ok(t)(Sync())
	if s := loaded(t).server("home"); s == nil || s.Headers["Authorization"] != "Bearer k2" {
		t.Fatalf("a server from before the switch, the key changed: %+v", s)
	}
	ok(t)(SetRemoteMCP("home", false))
	if has() {
		t.Fatal("a server from before the switch, switched off, kept")
	}
}

// A Remote magpie renamed, its name and id (treeman_26897: its server
// went), keeps its server, under the id it has now, with its switch and
// its offer; and renamed again, still.
func TestRemoteMagpieMCPFollowsRename(t *testing.T) {
	hubSandbox(t)
	f := fakeRemoteHubs(t, provider.RemoteHub{ID: "remote-magpie", Name: "Remote magpie", Root: "http://192.168.1.9:3425", Key: "k"})
	ok(t)(SetRemoteMCP("remote-magpie", true))
	provider.RemoteHubFound("remote-magpie")
	if s := loaded(t).server("remote-magpie"); s == nil || s.Bridge != "remote-magpie" {
		t.Fatalf("no server: %+v", loaded(t).MCP)
	}
	for _, to := range []provider.RemoteHub{
		{ID: "home", Name: "Home", Root: "http://192.168.1.9:3425", Key: "k", Was: []string{"remote-magpie"}},
		{ID: "lab", Name: "Lab", Root: "http://192.168.1.9:3425", Key: "k", Was: []string{"remote-magpie", "home"}},
	} {
		f.hs = []provider.RemoteHub{to}
		if on := RemoteMCPOn(); !on[to.ID] {
			t.Fatalf("renamed %s, read before a sync: switch %v", to.ID, on)
		}
		ok(t)(Sync())
		l := loaded(t)
		s := l.server("remote-magpie")
		if s == nil || s.Bridge != to.ID {
			t.Fatalf("renamed %s, the server: %+v", to.ID, l.MCP)
		}
		if !slices.Equal(l.BridgesOn, []string{to.ID}) || !slices.Equal(l.BridgesOffered, []string{to.ID}) {
			t.Fatalf("renamed %s: on %v, offered %v", to.ID, l.BridgesOn, l.BridgesOffered)
		}
		if provider.RemoteHubAsk(to.ID) {
			t.Errorf("renamed %s, offered again", to.ID)
		}
	}
	// an id it had that another provider has now is that one's
	f.hs = append(f.hs, provider.RemoteHub{ID: "home", Name: "Home", Root: "http://10.0.0.7:3425", Key: "k"})
	ok(t)(Sync())
	if l := loaded(t); len(l.MCP) != 1 || l.MCP[0].Bridge != "lab" || slices.Contains(l.BridgesOn, "home") {
		t.Fatalf("an id taken by another: %+v, on %v", l.MCP, l.BridgesOn)
	}
	// switched off under its id now
	ok(t)(SetRemoteMCP("lab", false))
	if l := loaded(t); len(l.MCP) != 0 {
		t.Fatalf("switched off after the rename: %+v", l.MCP)
	}
}

// A server carried in from another computer is never that computer's
// Remote magpie's here.
func TestCarriedServerIsNoBridge(t *testing.T) {
	l := &Library{}
	l.putServers([]*Server{{Name: "home", Transport: "http", URL: "http://10.0.0.2:3425" + gateway.HubMCPAll, Bridge: "home"}})
	if l.MCP[0].Bridge != "" {
		t.Fatalf("carried in as a Remote magpie's: %+v", l.MCP[0])
	}
}
