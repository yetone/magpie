package gui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
)

// changqiu4890 on Discord: dsh sends no session id on its chat-completions
// and messages routes, so the Routing page's requests list put every dsh
// request under "No session ID". The routes it is served name the dsh
// session their conversation id was read from, as the Context page does
// (TestContextDshSessionTitles); another agent's route, and a conversation
// no file holds, stay as they came.
func TestRoutingDshRoutesNameTheirSession(t *testing.T) {
	sandboxHome(t)
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	resetRouteConvs := func() {
		routeConvs.Lock()
		routeConvs.of, routeConvs.at = nil, time.Time{}
		routeConvs.Unlock()
	}
	resetRouteConvs()
	t.Cleanup(resetRouteConvs)
	defer func(f, k time.Duration) { convsFresh, convsKept = f, k }(convsFresh, convsKept)
	convsFresh = 0

	const chat, msgs = "session-508e7b0a-37c0-41f7-ac62-e48ab3146111", "session-8d93476d-ca34-461c-9e8e-7031c1dedfff"
	route := func(id int64, agent, conv, session string) gateway.Route {
		return gateway.Route{ID: id, Agent: agent, Conv: conv, Session: session}
	}
	routes := []gateway.Route{
		route(1, "dsh", "magpie-950998433c3e70e50dc32994", ""),      // chat-completions turn
		route(2, "dsh", "magpie-be31a0561e12cf603d3e79d9", ""),      // messages turn
		route(3, "dsh", "magpie-000000000000000000000000", ""),      // no file's
		route(4, "other", "magpie-950998433c3e70e50dc32994", ""),    // not dsh
		route(5, "dsh", "magpie-950998433c3e70e50dc32994", "own-1"), // names its own
	}
	sessionsOf := func() map[int64]string {
		out := map[int64]string{}
		for _, r := range pricedRoutes(routes) {
			out[r.ID] = r.Session
		}
		return out
	}
	// listed before dsh wrote its files: nothing to name yet
	if got := sessionsOf(); got[1] != "" || got[2] != "" {
		t.Fatalf("before the files: %v", got)
	}
	fixture := filepath.Join("..", "sessions", "testdata", "dsh-gateway", "sessions")
	if err := os.CopyFS(filepath.Join(home, "sessions"), os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	// a session begun since the last listing is found on the next poll
	got := sessionsOf()
	want := map[int64]string{1: chat, 2: msgs, 3: "", 4: "", 5: "own-1"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("route %d: session %q, want %q", id, got[id], w)
		}
	}
}
