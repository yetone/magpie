package gui

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
)

// A dsh fork copies its source's conversation, so the hash of the opening
// names both sessions and names neither: Routing showed the source for the
// fork's own requests too, or nothing at all. The reply's id is the fork's
// own — dsh keeps the one magpie's gateway put on it — and names it
// (Route.ResponseID, sessions' ResponseIDs).
func TestRoutingTellsAForkByItsReply(t *testing.T) {
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

	const opening = `{"type":"user/message","seq":1,"time":1000,"data":{"source":{"kind":"user"},"content":[{"type":"text","text":"same first prompt"}]}}`
	const endSeed = `{"type":"session/end-seed","seq":2,"time":1000,"data":{"inherited":true}}`
	reply := func(seq int, id string) string {
		return `{"type":"assistant/message","seq":` + strconv.Itoa(seq) + `,"time":1000,"data":{"message":{"source":{"provider":"magpie","model":"group/flash","replayState":{"response":{"provider":"magpie","responseId":"` + id + `"}}}}}}`
	}
	write := func(name, header string, lines ...string) {
		dir := filepath.Join(home, "sessions", "proj", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b := header + "\n"
		for _, l := range lines {
			b += l + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "session.v4.jsonl"), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("session-source", `{"type":"session","version":4,"id":"session-source","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":false}`,
		opening, reply(2, "reply-source"))
	write("session-fork", `{"type":"session","version":4,"id":"session-fork","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":true}`,
		opening, reply(2, "reply-source"), endSeed, reply(4, "reply-fork"))

	var openingHash string
	for _, s := range sessions.List(0) {
		if s.ID == "session-source" && len(s.Convs) > 0 {
			openingHash = s.Convs[0]
		}
	}
	if openingHash == "" {
		t.Fatal("the fixture session names no conversation id")
	}
	routes := []gateway.Route{
		{ID: 1, Agent: "dsh", Conv: openingHash, ResponseID: "reply-fork"},   // the fork's own turn
		{ID: 2, Agent: "dsh", Conv: openingHash},                             // the opening alone: its source's
		{ID: 3, Agent: "dsh", Conv: openingHash, ResponseID: "reply-source"}, // the source's
		{ID: 4, Agent: "dsh", ResponseID: "reply-fork"},                      // the reply alone
	}
	got := map[int64]string{}
	for _, r := range pricedRoutes(routes) {
		got[r.ID] = r.Session
	}
	// 2 is the opening alone: the fork's inherited copy of it is its
	// source's (the seed ends before its own turns), so it names the source
	want := map[int64]string{1: "session-fork", 2: "session-source", 3: "session-source", 4: "session-fork"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("route %d: session %q, want %q", id, got[id], w)
		}
	}
}
