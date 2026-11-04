package gui

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/settings"
)

// A Quit the user chose while agents go through magpie asks first
// (Aisiverto on X: Codex's 502s after magpie was quit, with nothing to
// say why). The window's Quit reaches AskQuit; the question's own Quit
// (now=1) quits; a browser's page (magpie web) has no window to ask in
// and quits.
func TestWindowQuitAsksFirst(t *testing.T) {
	quits := 0
	asked := make(chan struct{}, 4)
	h := webPageHandler(t, askingHost{webHost{quit: func() { quits++ }}, asked}, nil)
	post := func(url string) {
		t.Helper()
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("POST", url, nil))
		if r.Code != 204 {
			t.Fatalf("%s: %d %s", url, r.Code, r.Body)
		}
	}

	post("/api/window/quit")
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the window's Quit didn't ask (AskQuit never ran)")
	}
	if quits != 0 {
		t.Fatalf("the window's Quit quit %d times without asking", quits)
	}

	post("/api/window/quit?now=1")
	if quits != 1 {
		t.Fatalf("the question's Quit (now=1) quit %d times, want 1", quits)
	}
	select {
	case <-asked:
		t.Fatal("the question's own Quit asked again")
	default:
	}

	web := webPageHandler(t, webHost{quit: func() { quits++ }}, nil)
	r := httptest.NewRecorder()
	web.ServeHTTP(r, httptest.NewRequest("POST", "/api/window/quit", nil))
	if quits != 2 {
		t.Fatalf("a browser's Quit quit %d times, want at once", quits-1)
	}
}

// askingHost is a host that asks, as the app's does.
type askingHost struct {
	webHost
	asked chan struct{}
}

func (h askingHost) AskQuit() { h.asked <- struct{}{} }

// The question names the agents magpie is in, and only those.
func TestQuitListsWiredAgents(t *testing.T) {
	yes := func() bool { return true }
	got := wiredAgents([]*agent.Agent{
		{ID: "codex", Name: "Codex", Icon: "openai", Joined: yes},
		{ID: "claude", Name: "Claude Code", Icon: "claude"},
		{ID: "hermes", Name: "Hermes", Icon: "hermes", Beside: yes},
	})
	want := []quitAgentJSON{{ID: "codex", Name: "Codex", Icon: "openai"}, {ID: "hermes", Name: "Hermes", Icon: "hermes"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("wired agents = %+v, want %+v", got, want)
	}
	// none is an empty list, which the page reads as "quit at once"
	if b, _ := json.Marshal(wiredAgents(nil)); string(b) != "[]" {
		t.Fatalf("no agents marshal as %s, want []", b)
	}
}

// GET /api/quit is what the question reads.
func TestQuitAPIListsAgents(t *testing.T) {
	old := quitAgents
	t.Cleanup(func() { quitAgents = old })
	quitAgents = func() []quitAgentJSON { return []quitAgentJSON{{ID: "codex", Name: "Codex", Icon: "openai"}} }
	r := httptest.NewRecorder()
	webPageHandler(t, webHost{}, nil).ServeHTTP(r, httptest.NewRequest("GET", "/api/quit", nil))
	var got struct{ Agents []quitAgentJSON }
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || len(got.Agents) != 1 || got.Agents[0].ID != "codex" {
		t.Fatalf("GET /api/quit = %d %s (%v)", r.Code, r.Body, err)
	}
}

// With no gateway served here (another magpie serves it, and goes on
// serving the agents) a Quit cuts nobody off.
func TestQuitAgentsNoneWithoutGateway(t *testing.T) {
	if served.Load() != nil {
		t.Skip("a gateway is served in this test binary")
	}
	if got := quitAgents(); len(got) != 0 {
		t.Fatalf("with no gateway served here, a Quit cuts off %+v", got)
	}
}

// With agents connected the first Quit asks; a second within a minute of
// the question quits (the user was told, and it is the way out should the
// window not show the question); later, it asks again.
func TestQuitNow(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	asked := now.Add(-10 * time.Second).UnixNano()
	for _, c := range []struct {
		name  string
		asked int64
		wired int
		want  bool
	}{
		{"nothing connected", 0, 0, true},
		{"nothing connected, asked before", asked, 0, true},
		{"connected, never asked", 0, 2, false},
		{"connected, asked 10s ago", asked, 1, true},
		{"connected, asked 2m ago", now.Add(-2 * time.Minute).UnixNano(), 1, false},
	} {
		if got := quitNow(c.asked, now, c.wired); got != c.want {
			t.Errorf("%s: quitNow = %v, want %v", c.name, got, c.want)
		}
	}
}

// "Don't ask again" (zhujiu on Discord): the question's Quit sent with
// noAsk=1 saves settings.QuitNoAsk and quits; from then on a Quit with
// agents connected quits at once, until Settings › General › Ask before
// quitting turns the question back on.
func TestQuitDontAskAgain(t *testing.T) {
	sandboxHome(t)
	old := quitAgents
	t.Cleanup(func() { quitAgents = old })
	quitAgents = func() []quitAgentJSON { return []quitAgentJSON{{ID: "codex", Name: "Codex", Icon: "openai"}} }
	now := time.Now()
	if quitAtOnce(0, now) {
		t.Fatal("with Codex connected, the first Quit quit without asking")
	}

	quits := 0
	h := webPageHandler(t, askingHost{webHost{quit: func() { quits++ }}, make(chan struct{}, 1)}, nil)
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("POST", "/api/window/quit?now=1&noAsk=1", nil))
	if r.Code != 204 || quits != 1 {
		t.Fatalf("the question's Quit with Don't ask again: %d %s, %d quits", r.Code, r.Body, quits)
	}
	if !settings.Load().QuitNoAsk {
		t.Fatal("Don't ask again wasn't saved")
	}
	if !quitAtOnce(0, now) {
		t.Fatal("after Don't ask again, a Quit with Codex connected still asks")
	}

	if err := settings.SetQuitNoAsk(false); err != nil {
		t.Fatal(err)
	}
	if quitAtOnce(0, now) {
		t.Fatal("with Ask before quitting on again, a Quit didn't ask")
	}
	// a plain Quit from the question saves nothing
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/window/quit?now=1", nil))
	if settings.Load().QuitNoAsk {
		t.Fatal("the question's Quit without Don't ask again saved it")
	}
}
