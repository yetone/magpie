package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Every Claude Code subagent's turn, Desktop's or a terminal's, moved, kept
// or neither, keeps the id it was sent with, as the trace's subagentId the
// Routing page tags. Kind stays empty. The lead's turns, a subagent's
// tool-less helper calls and a blank id carry none.
func TestSubagentTurnTagged(t *testing.T) {
	for _, tc := range []struct {
		name, key, sub, model, effort string
		tools                         bool
		want                          string
	}{
		{"desktop", "claude-desktop", "worker", "", "", true, "worker"},
		{"desktop moved", "claude-desktop", "worker", "fake/m2", "", true, "worker"},
		{"desktop kept", "claude-desktop", "worker", "fake/missing", "", true, "worker"},
		{"desktop effort", "claude-desktop", "worker", "", "high", true, "worker"},
		{"terminal", "claude", "worker", "", "", true, "worker"},
		{"trimmed", "claude", "  worker  ", "", "", true, "worker"},
		{"lead", "claude-desktop", "", "fake/m2", "high", true, ""},
		{"helper", "claude-desktop", "worker", "fake/m2", "high", false, ""},
		{"terminal helper", "claude", "worker", "", "", false, ""},
		{"blank", "claude-desktop", " ", "fake/m2", "high", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desktopSubagentSetup(t)
			desktopSubagentSet(t, tc.model)
			if err := provider.SetDesktopSubagentEffort(tc.effort); err != nil {
				t.Fatal(err)
			}
			s := New()
			desktopSubagentAsk(t, s, Token+"-"+tc.key, "fake/m1", tc.sub, "/v1/messages", tc.tools)
			r := lastRoute(s)
			if r.SubagentID != tc.want || r.Kind != "" {
				t.Fatalf("subagentId %q kind %q, want %q and no kind", r.SubagentID, r.Kind, tc.want)
			}
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(b), `"subagentId":"worker"`); got != (tc.want != "") {
				t.Fatalf("route JSON %s", b)
			}
		})
	}
}

// A call that names a kind keeps its own tag: the subagent id on it adds
// none.
func TestSubagentTurnKindFirst(t *testing.T) {
	desktopSubagentSetup(t)
	desktopSubagentSet(t, "fake/m2")
	s := New()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"fake/m1","max_tokens":200,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`))
	req.Header.Set("x-api-key", Token+"-claude-desktop")
	req.Header.Set("User-Agent", desktopCodeUA)
	req.Header.Set("x-claude-code-agent-id", "worker")
	req.Header.Set("x-openai-subagent", "review")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if r := lastRoute(s); r.Kind != "review" || r.SubagentID != "" {
		t.Fatalf("kind %q subagentId %q", r.Kind, r.SubagentID)
	}
}

// A day's history keeps the id, gzipped too, and a route kept before the id
// was has none.
func TestHistoryKeepsSubagentID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	saveRoute(Route{ID: 1, Time: now, Agent: "claude-desktop", SubagentID: "worker", Done: true})
	saveRoute(Route{ID: 2, Time: now, Agent: "claude-desktop", Done: true})
	pruneHistory(HistoryDir(), now.AddDate(0, 0, 1))
	_, routes, _ := History(now.Format(dayForm))
	if len(routes) != 2 || routes[0].SubagentID != "worker" || routes[1].SubagentID != "" {
		t.Fatalf("history: %+v", routes)
	}
}

// A turn a Desktop pick or effort found is trusted, and a request with no
// agent id is left alone, so neither decodes the body: past reading the
// headers, the tag allocates nothing.
func TestSubagentTurnDoesNotDecode(t *testing.T) {
	tools := []byte(`{"tools":[{"name":"Bash"}]}`)
	lead := httptest.NewRequest("POST", "/v1/messages", nil)
	sub := httptest.NewRequest("POST", "/v1/messages", nil)
	sub.Header.Set("x-claude-code-agent-id", "worker")
	for _, tc := range []struct {
		name  string
		r     *http.Request
		body  []byte
		known bool
		want  string
	}{
		{"lead", lead, tools, false, ""},
		{"known", sub, tools, true, "worker"},
		// trusted without a look at the body
		{"known without tools", sub, []byte(`{}`), true, "worker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := testing.AllocsPerRun(10, func() { subagentOf(tc.r.Header) })
			allocs := testing.AllocsPerRun(10, func() {
				if got := subagentTurnOf(tc.r, "claude-desktop", "", tc.body, tc.known); got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			})
			if allocs > headers {
				t.Fatalf("allocated %g times, reading the headers %g", allocs, headers)
			}
		})
	}
	// the decode the cases above leave out shows in the count
	if got := subagentTurnOf(sub, "claude-desktop", "", tools, false); got != "worker" {
		t.Fatalf("unknown turn with tools: %q", got)
	}
	headers := testing.AllocsPerRun(10, func() { subagentOf(sub.Header) })
	if n := testing.AllocsPerRun(10, func() { subagentTurnOf(sub, "claude-desktop", "", tools, false) }); n <= headers {
		t.Fatalf("reading an unknown turn's tools allocated %g times, its headers %g", n, headers)
	}
}
