package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// standInGroup is 01huadalang's setup (Discord, #feedback): a group "G"
// of Codex, signed in to two accounts, listed first and a pay-as-you-go
// MiMo key listed second as the backup, routed routing with Stays mode.
// Codex answers 429 usage_limit_reached while down; allowances, when
// known, says each account's 5 hours used (100 while down, five after)
// and its week at weekly. up has Codex back: the 5 hours renewed and every
// rest over, as when the vendor's reset passes.
type standInGroup struct {
	s      *Server
	mu     sync.Mutex
	down   bool
	five   float64
	weekly float64
}

func newStandInGroup(t *testing.T, routing, mode string, known bool) *standInGroup {
	t.Helper()
	codexSignedIn(t, "spare@example.com")
	g := &standInGroup{down: true, five: 10, weekly: 40}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		g.mu.Lock()
		down := g.down
		g.mu.Unlock()
		if down {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7000,"output_tokens":1,"input_tokens_details":{"cached_tokens":6000}}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	wasA := allowances
	t.Cleanup(func() { allowances = wasA })
	allowances = func(agent string) map[string]provider.Allowance {
		if !known || agent != "codex" {
			return nil
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		five := g.five
		if g.down {
			five = 100
		}
		a := provider.Allowance{
			{Used: five, Resets: time.Now().Add(2 * time.Hour), Span: 5 * time.Hour},
			{Used: g.weekly, Resets: time.Now().Add(72 * time.Hour), Span: 7 * 24 * time.Hour},
		}
		return map[string]provider.Allowance{"me@example.com": a, "spare@example.com": a}
	}
	mimo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
			`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7000,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":6000}}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(mimo.Close)
	if err := provider.Save(provider.Provider{ID: "mimo", Name: "MiMo", Key: "k", Models: []string{"mimo-v2.6-pro"}, Chat: mimo.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5", "mimo/mimo-v2.6-pro"}, Routing: routing, Affinity: mode}); err != nil {
		t.Fatal(err)
	}
	g.s = New()
	return g
}

func (g *standInGroup) up() {
	g.mu.Lock()
	g.down = false
	g.mu.Unlock()
	restingUntil.Lock()
	for k := range restingUntil.m {
		restingUntil.m[k] = time.Now().Add(-time.Second)
	}
	restingUntil.Unlock()
	provider.ForgetAccounts()
}

// responsesTurn is the Responses body of a conversation's n-th user turn
// (n from 1), each earlier turn answered.
func responsesTurn(n int) string {
	var b strings.Builder
	b.WriteString(`{"model":"group/g","stream":true,"input":[`)
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteString(`{"role":"assistant","content":[{"type":"output_text","text":"yo"}]},`)
		}
		b.WriteString(`{"role":"user","content":[{"type":"input_text","text":"turn ` + itoa(i) + `"}]}`)
		if i < n {
			b.WriteString(",")
		}
	}
	b.WriteString(`]}`)
	return b.String()
}

// ask sends turn n of session sess, and says who answered it: "codex" or
// "mimo", and the affinity's why.
func (g *standInGroup) ask(t *testing.T, sess string, n int) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(responsesTurn(n)))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", sess)
	g.s.Handler().ServeHTTP(rec, req)
	why := ""
	if a := lastRoute(g.s).Affinity; a != nil {
		why = a.Why
	}
	switch body := rec.Body.String(); {
	case strings.Contains(body, "pong"):
		return "codex", why
	case strings.Contains(body, "hello"):
		return "mimo", why
	default:
		t.Fatalf("turn %d of %s: %d %s", n, sess, rec.Code, body)
	}
	return "", why
}

// TestGroupBackupGivesTheConversationBack is 01huadalang's report: Codex
// first in a group, a MiMo key as the backup. Codex's accounts were out,
// the conversation went to MiMo, and once Codex was back it stayed on
// MiMo, kept there by MiMo's cache, for as long as it ran. A group routed
// Smart (or any way but In order) now gives a conversation that went to a
// later member only because the ones ahead couldn't take it back at its
// next turn, as In order and a provider's fallback do. Each Stays mode is
// held to what it says: Auto gives it back, Within a turn and Off route
// the new turn afresh, Session and Strict keep it where it is.
func TestGroupBackupGivesTheConversationBack(t *testing.T) {
	for _, c := range []struct {
		name, routing, mode string
		known               bool // the vendor says what Codex's accounts have left
		on, why             string
	}{
		// Codex out of its 5 hours, said by its usage: MiMo is first
		// while it is, and Codex first again when it renews
		{"smart, auto, usage known", "", "", true, "codex", "back"},
		// Codex's usage not told: its 429 sends the conversation on
		{"smart, auto, usage unknown", "", "", false, "codex", "back"},
		{"in order, auto", provider.Ordered, "", true, "codex", "order"},
		// in turn: the next turn goes round, to the one after the last
		{"in turn, auto", provider.Rotate, "", true, "codex", "new-turn"},
		{"smart, within a turn", "", provider.AffinityTurn, true, "codex", "new-turn"},
		{"smart, off", "", provider.AffinityOff, true, "codex", "off"},
		{"smart, session", "", provider.AffinitySession, true, "mimo", "session"},
		{"smart, strict", "", provider.AffinityStrict, true, "mimo", "strict"},
		{"in order, session", provider.Ordered, provider.AffinitySession, true, "mimo", "session"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newStandInGroup(t, c.routing, c.mode, c.known)
			for n := 1; n <= 2; n++ {
				if on, why := g.ask(t, "s1", n); on != "mimo" {
					t.Fatalf("turn %d with Codex out went to %s (%s), want mimo", n, on, why)
				}
			}
			g.up()
			on, why := g.ask(t, "s1", 3)
			if on != c.on || why != c.why {
				t.Fatalf("turn 3 with Codex back went to %s (%s), want %s (%s)", on, why, c.on, c.why)
			}
			if on, why := g.ask(t, "s2", 1); on != "codex" {
				t.Fatalf("a new conversation with Codex back went to %s (%s), want codex", on, why)
			}
			if c.on != "codex" || c.routing == provider.Rotate {
				return
			}
			// given back, it stays on Codex by Codex's cache, as before
			if on, why := g.ask(t, "s1", 4); on != "codex" || why != "cache" && c.mode == "" {
				t.Fatalf("turn 4 went to %s (%s), want codex (cache)", on, why)
			}
		})
	}
}

// TestGroupBackupStandingInWhileTheFirstIsBackAndBehind: the backup that
// answers on while Codex is back but routing still puts the backup first
// (Smart's low tier, Codex's 5 hours at 92%: a rate cap near full; a week
// at 92% alone no longer is, as what it has left is lost at its reset,
// #5209) is a stand-in still, and gives the conversation back once
// routing puts Codex first again.
func TestGroupBackupStandingInWhileTheFirstIsBackAndBehind(t *testing.T) {
	g := newStandInGroup(t, "", "", true)
	if on, why := g.ask(t, "s1", 1); on != "mimo" {
		t.Fatalf("turn 1 with Codex out went to %s (%s)", on, why)
	}
	g.mu.Lock()
	g.five = 92
	g.mu.Unlock()
	g.up()
	if on, why := g.ask(t, "s1", 2); on != "mimo" || why != "cache" {
		t.Fatalf("turn 2 with Codex back at 92%% of its 5 hours went to %s (%s), want mimo (cache)", on, why)
	}
	g.mu.Lock()
	g.five = 10
	g.mu.Unlock()
	provider.ForgetAccounts()
	if on, why := g.ask(t, "s1", 3); on != "codex" || why != "back" {
		t.Fatalf("turn 3 with Codex's 5 hours renewed went to %s (%s), want codex (back)", on, why)
	}
}

// TestGroupMemberPutFirstOnItsMeritsKeepsItsCache: a member routing put
// first while the one listed ahead was ready and had room stood in for
// nobody, and keeps the conversation by its cache when routing later
// prefers the one ahead, as a Smart group always has: moving it would
// have it sent afresh, and paid in full, to no end the user asked for.
func TestGroupMemberPutFirstOnItsMeritsKeepsItsCache(t *testing.T) {
	g := newStandInGroup(t, "", "", true)
	g.mu.Lock()
	g.five = 92 // its rate cap low, not spent: Smart puts the unknown key first
	g.mu.Unlock()
	g.up()
	if on, why := g.ask(t, "s1", 1); on != "mimo" || why != "first" {
		t.Fatalf("turn 1 with Codex at 92%% of its 5 hours went to %s (%s), want mimo (first)", on, why)
	}
	g.mu.Lock()
	g.five = 10
	g.mu.Unlock()
	provider.ForgetAccounts()
	if on, why := g.ask(t, "s1", 2); on != "mimo" || why != "cache" {
		t.Fatalf("turn 2 went to %s (%s), want mimo (cache)", on, why)
	}
	if on, why := g.ask(t, "s2", 1); on != "codex" {
		t.Fatalf("a new conversation went to %s (%s), want codex", on, why)
	}
}

// TestGroupWeekLowIsSpentBeforeTheKey: Codex back at 92% of its week, its
// 5 hours with room, goes ahead of the pay-as-you-go key listed after it
// in a Smart group: what the week has left is lost at its reset
// (ysicing_01858, #5209), where the key's tokens keep.
func TestGroupWeekLowIsSpentBeforeTheKey(t *testing.T) {
	g := newStandInGroup(t, "", "", true)
	g.mu.Lock()
	g.weekly = 92
	g.mu.Unlock()
	g.up()
	if on, why := g.ask(t, "s1", 1); on != "codex" {
		t.Fatalf("a new conversation with Codex at 92%% of its week went to %s (%s), want codex", on, why)
	}
}

// TestGroupStandInStaysWithinATurn: within a turn, the agent handing tool
// results back, the backup keeps the turn it began, on the reasoning it
// sealed for it, though Codex is back.
func TestGroupStandInStaysWithinATurn(t *testing.T) {
	g := newStandInGroup(t, "", "", true)
	if on, why := g.ask(t, "s1", 1); on != "mimo" {
		t.Fatalf("turn 1 with Codex out went to %s (%s)", on, why)
	}
	g.up()
	body := `{"model":"group/g","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"turn 1"}]},` +
		`{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"a.txt"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", "s1")
	g.s.Handler().ServeHTTP(rec, req)
	a := lastRoute(g.s).Affinity
	if !strings.Contains(rec.Body.String(), "hello") || a == nil || a.Why != "turn" {
		t.Fatalf("a tool result within turn 1 went to %q (%+v), want mimo (turn)", rec.Body.String(), a)
	}
}

// TestGroupStandInSurvivesARestart: magpie restarted (an update) while
// the backup stands in still knows it stood in, from affinity.json, and
// gives the conversation back once Codex is.
func TestGroupStandInSurvivesARestart(t *testing.T) {
	g := newStandInGroup(t, "", "", true)
	if on, why := g.ask(t, "s1", 1); on != "mimo" {
		t.Fatalf("turn 1 with Codex out went to %s (%s)", on, why)
	}
	saveSticks()
	sticks.Lock()
	sticks.m, sticks.from, sticks.mod = map[string]stick{}, "", time.Time{}
	sticks.Unlock()
	g.up()
	if on, why := g.ask(t, "s1", 2); on != "codex" || why != "back" {
		t.Fatalf("turn 2 after a restart, Codex back, went to %s (%s), want codex (back)", on, why)
	}
}
