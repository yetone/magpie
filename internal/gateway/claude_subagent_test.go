package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// testdata/claude_subagents holds the first request of each kind of
// subagent, as Claude Code 2.1.327 sent it to a fake Anthropic server
// (sandbox HOME, ANTHROPIC_MODEL=fake/lead, CLAUDE_CODE_SUBAGENT_MODEL=
// fake/sub, the lead told to spawn one): Explore and Plan asked for
// fake/lead, general-purpose for fake/sub, and a custom agent whose
// model: line says fake/own for fake/own (#7176). Paths are the sandbox's,
// made /home/user/project.

// claudeSubagentBody is the request a subagent of kind sent, asking for
// model.
func claudeSubagentBody(t *testing.T, kind, model string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude_subagents", kind+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	v["model"] = model
	v["stream"] = false
	out, _ := json.Marshal(v)
	return out
}

// claudeSubagentSet has ClaudeSubagent answer set for Claude Code, as the
// agent package reads it from settings routed through magpie.
func claudeSubagentSet(t *testing.T, set string) {
	t.Helper()
	was := ClaudeSubagent
	ClaudeSubagent = func(agent string) string {
		if agent != "claude" {
			return ""
		}
		return set
	}
	t.Cleanup(func() { ClaudeSubagent = was })
}

// claudeSubagentAsk sends the request of a subagent of kind (none for the
// lead's turn) as Claude Code in a terminal sends it, on model.
func claudeSubagentAsk(t *testing.T, s *Server, key, kind, model, path string, sub bool, byPath ...bool) {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(string(claudeSubagentBody(t, kind, model))))
	if len(byPath) != 0 && byPath[0] {
		r = r.WithContext(context.WithValue(r.Context(), agentPathCtx{}, "claude"))
	}
	r.Header.Set("x-api-key", key)
	r.Header.Set("User-Agent", "claude-cli/2.1.327 (external, cli)")
	r.Header.Set("x-claude-code-session-id", "lead-session")
	if sub {
		r.Header.Set("x-claude-code-agent-id", "acd536fa4f017c34d")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s on %s: %d %s", kind, model, w.Code, w.Body.String())
	}
}

// Claude Code's Explore and Plan ask for the lead's model whatever its
// subagent model is set to (#7176): their turns go on the model set, its
// effort too, and the trace and the ledger keep the model they asked for.
func TestClaudeInheritingSubagentsMoved(t *testing.T) {
	for _, kind := range []string{"explore", "plan"} {
		t.Run(kind, func(t *testing.T) {
			u := desktopSubagentSetup(t)
			claudeSubagentSet(t, "fake/m2")
			s := New()
			claudeSubagentAsk(t, s, Token+"-claude", kind, "fake/m1", "/v1/messages", true)
			if got, _ := u.seen(); got != "m2" {
				t.Fatalf("%s sent %q, want m2", kind, got)
			}
			r := lastRoute(s)
			if r.Subagent == nil || *r.Subagent != (SubagentPick{Asked: "fake/m1", To: "fake/m2"}) || r.Model != "fake/m1" || r.SubagentID != "acd536fa4f017c34d" {
				t.Fatalf("trace: model %q subagent %+v tag %q", r.Model, r.Subagent, r.SubagentID)
			}
			log, err := os.ReadFile(usage.Path())
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(log)), "\n")
			var rec usage.Record
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
				t.Fatal(err)
			}
			if rec.Requested != "fake/m1" || rec.Model != "m2" || rec.Subagent != "acd536fa4f017c34d" {
				t.Fatalf("ledger: requested %q model %q subagent %q", rec.Requested, rec.Model, rec.Subagent)
			}
			// its token count is on the model the turn goes to
			claudeSubagentAsk(t, s, Token+"-claude", kind, "fake/m1", "/v1/messages/count_tokens", true)
			if got, path := u.seen(); got != "m2" || path != "/v1/messages/count_tokens" {
				t.Fatalf("%s count sent %q to %q, want m2", kind, got, path)
			}
		})
	}
}

// The model set at an effort ("<model>:<level>", as magpie writes it with
// one) gives the moved turn that effort, as general-purpose's own ask
// for it would.
func TestClaudeInheritingSubagentEffort(t *testing.T) {
	desktopSubagentSetup(t)
	claudeSubagentSet(t, "fake/m2:high")
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("x-claude-code-agent-id", "worker")
	pick, effort := claudeSubagentPick(r, "claude", "fake/m1", "fake/m1", claudeSubagentBody(t, "explore", "fake/m1"))
	if pick == nil || *pick != (SubagentPick{Asked: "fake/m1", To: "fake/m2"}) || effort != "high" {
		t.Fatalf("pick %+v effort %q, want fake/m2 at high", pick, effort)
	}
}

// What Claude Code already puts on its own model stays as asked:
// general-purpose (it reads the variable itself), a custom agent's model:
// line, the lead's turns, Desktop's Code tab (its own setting), and every
// request when no model is set or the one set is the one asked for.
func TestClaudeSubagentLeavesOtherRequests(t *testing.T) {
	for _, tc := range []struct {
		name, key, kind, model, set string
		sub                         bool
	}{
		{"general-purpose", "claude", "general-purpose", "fake/m1", "fake/m2", true},
		{"custom agent", "claude", "custom", "fake/m3", "fake/m2", true},
		{"lead", "claude", "explore", "fake/m1", "fake/m2", false},
		{"desktop", "claude-desktop", "explore", "fake/m1", "fake/m2", true},
		{"codex", "codex", "explore", "fake/m1", "fake/m2", true},
		{"unset", "claude", "explore", "fake/m1", "", true},
		{"same", "claude", "explore", "fake/m2", "fake/m2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := desktopSubagentSetup(t)
			claudeSubagentSet(t, tc.set)
			s := New()
			claudeSubagentAsk(t, s, Token+"-"+tc.key, tc.kind, tc.model, "/v1/messages", tc.sub)
			if got, _ := u.seen(); "fake/"+got != tc.model {
				t.Fatalf("sent %q, want %s", got, tc.model)
			}
			if r := lastRoute(s); r.Subagent != nil {
				t.Fatalf("untouched request traced %+v", r.Subagent)
			}
		})
	}
}

// A model set that magpie doesn't serve, that the user disabled, or that
// the gateway key may not use, leaves Explore on the lead's model, and the
// trace says why.
func TestClaudeInheritingSubagentKept(t *testing.T) {
	for _, tc := range []struct{ name, why, target string }{
		{"unserved", "magpie doesn't serve it now", "fake/missing"},
		{"disabled", "it is disabled in magpie", "fake/m2"},
		{"held", "the gateway key may not use it", "fake/m2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := desktopSubagentSetup(t)
			key := Token + "-claude"
			switch tc.name {
			case "disabled":
				if err := provider.DisableModels("fake/m2"); err != nil {
					t.Fatal(err)
				}
			case "held":
				keys, secrets := newCaller(t, "Held")
				if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"fake/m1"}}); err != nil {
					t.Fatal(err)
				}
				key = secrets[0]
			}
			claudeSubagentSet(t, tc.target)
			s := New()
			// a named key with Claude Code's User-Agent reads as
			// claude-cli, a client magpie didn't wire; one that names
			// Claude Code by its address (AgentPath) is Claude Code
			claudeSubagentAsk(t, s, key, "explore", "fake/m1", "/v1/messages", true, tc.name == "held")
			if got, _ := u.seen(); got != "m1" {
				t.Fatalf("sent %q, want m1", got)
			}
			r := lastRoute(s)
			if r.Subagent == nil || *r.Subagent != (SubagentPick{Asked: "fake/m1", To: tc.target, Kept: tc.why}) {
				t.Fatalf("trace %+v (agent %q model %q), want kept: %s", r.Subagent, r.Agent, r.Model, tc.why)
			}
		})
	}
}

// A lead's turn reads no settings and decodes nothing, and a subagent's
// that isn't Explore's or Plan's reads no settings either.
func TestClaudeSubagentCheapChecksFirst(t *testing.T) {
	reads := 0
	was := ClaudeSubagent
	ClaudeSubagent = func(string) string { reads++; return "fake/m2" }
	t.Cleanup(func() { ClaudeSubagent = was })
	lead := httptest.NewRequest("POST", "/v1/messages", nil)
	body := claudeSubagentBody(t, "explore", "fake/m1")
	// reading the header is all it costs, as it is for subagentTurnOf
	header := testing.AllocsPerRun(10, func() { subagentOf(lead.Header) })
	allocs := testing.AllocsPerRun(10, func() {
		if p, _ := claudeSubagentPick(lead, "claude", "fake/m1", "fake/m1", body); p != nil {
			t.Fatalf("lead pick %+v", p)
		}
	})
	if allocs > header || reads != 0 {
		t.Fatalf("lead's turn: %g allocations (the header read takes %g), %d settings reads", allocs, header, reads)
	}
	sub := httptest.NewRequest("POST", "/v1/messages", nil)
	sub.Header.Set("x-claude-code-agent-id", "worker")
	if p, _ := claudeSubagentPick(sub, "claude", "fake/m1", "fake/m1", claudeSubagentBody(t, "general-purpose", "fake/m1")); p != nil || reads != 0 {
		t.Fatalf("general-purpose: pick %+v, %d settings reads", p, reads)
	}
}
