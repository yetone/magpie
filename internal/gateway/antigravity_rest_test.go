package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// turnedAway is Antigravity's answer to Claude Code's and the Agent SDK's
// system prompt: a 429 that reads as a used-up plan whatever the quota
// left, on every model (#666: Claude Desktop's chats always 429 while its
// titles and Codex on the same account answer). magpie says why, and now
// also leaves the account alone, since nothing is wrong with it.
const turnedAway = `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`

// claudeCodeSystem is Claude Code's, as 2.1.288 sends it: its billing line
// or the SDK's identity line anywhere in the system instruction.
const claudeCodeSystem = "x-anthropic-billing-header: cc_version=2.1.288.e3f; cc_entrypoint=sdk-cli;\nYou are Claude Code, Anthropic's official CLI for Claude."

// antigravityTurnedAwayGroup is a routing group whose first member holds the
// Antigravity accounts signed in at users, and whose second — "other", another
// provider altogether, not a mate of Antigravity's — answers anything, so the
// first has someone else to fail over to. say is what those accounts' upstream
// answers. The second value counts how often an account's usage was asked of
// the vendor again.
func antigravityTurnedAwayGroup(t *testing.T, users []string, say func(w http.ResponseWriter, r *http.Request)) (*Server, *int) {
	return antigravityGroupFor(t, users, "m", say)
}

func antigravityGroupFor(t *testing.T, users []string, model string, say func(w http.ResponseWriter, r *http.Request)) (*Server, *int) {
	t.Helper()
	fresh(t)
	var logins []map[string]any
	for _, user := range users {
		logins = append(logins, map[string]any{
			"agent": "antigravity", "user": user, "on": true,
			"auth": map[string]any{"access_token": user, "refresh_token": "ref-" + user, "project": "p1",
				"expiry_date": time.Now().Add(time.Hour).UnixMilli()},
		})
	}
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), mustJSON(logins), 0o600); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasSuffix(r.URL.Path, "latest-arm64-mac.yml"):
			body = "version: 2.9.1\n"
		case strings.Contains(r.URL.Path, ":loadCodeAssist"):
			body = `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"p1"}`
		case strings.Contains(r.URL.Path, ":fetchAvailableModels"):
			body = fmt.Sprintf(`{"models":{%q:{}}}`, model)
		default:
			return nil, fmt.Errorf("unexpected request to %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = old })
	p, err := provider.Find("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from the other"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	}))
	t.Cleanup(other.Close)
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Models: []string{model}, Anthropic: other.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"antigravity/" + model, "other/" + model}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	askedUsage := new(int)
	oldAsk := staleAllowance
	staleAllowance = func(agent, user string) { *askedUsage++ }
	t.Cleanup(func() { staleAllowance = oldAsk })
	s := New()
	// the Antigravity accounts' own upstream, which only the gateway asks;
	// any other member is answered by its own test server
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Host, "googleapis.com") {
			return http.DefaultTransport.RoundTrip(r)
		}
		rec := httptest.NewRecorder()
		say(rec, r)
		return rec.Result(), nil
	})}
	return s, askedUsage
}

// claudeTurn is a turn as Claude Code sends it on the group, with system
// as the system instruction the client asked for.
func claudeTurn(system string) string {
	return `{"model":"group/g","max_tokens":100,"system":` + quote(system) + `,"messages":[{"role":"user","content":"hi"}]}`
}

// turnOn posts a turn to this gateway and says what came back.
func turnOn(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// the account Antigravity turned away is healthy: it rests no quarter of
// an hour for a request it refused, its usage isn't asked again, and the
// next turn asks it first again — as the refusal it is, not a spent quota.
func TestAntigravityTurnedAwayRestsNobody(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	s, askedUsage := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, turnedAway)
	})
	code, out := turnOn(t, s, claudeTurn(claudeCodeSystem))
	if code != 200 || !strings.Contains(out, "from the other") {
		t.Fatalf("the turn: %d %s", code, out)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 429 || r.Tries[0].Fail != failPrompt || r.Tries[0].Rest != nil {
		t.Fatalf("the turned-away try: %+v", r.Tries)
	}
	if !strings.Contains(r.Tries[0].Error, antigravityTurnedAwayHint) {
		t.Fatalf("the attempt isn't told why: %s", r.Tries[0].Error)
	}
	if *askedUsage != 0 {
		t.Fatalf("the account's usage was asked again %d times for a request it refused", *askedUsage)
	}
	if _, ok := restOf("antigravity@u@example.com"); ok {
		t.Fatalf("antigravity@u@example.com rests for a request it answered with a refusal: %v", rests())
	}
	// the same account is asked first again on the next turn that doesn't
	// carry that system prompt (one that does is foreseen, #2156)
	code, out = turnOn(t, s, claudeTurn("You are a helpful assistant."))
	if code != 200 || !strings.Contains(out, "from the other") {
		t.Fatalf("the next turn: %d %s", code, out)
	}
	if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[0].ID != "antigravity" || r.Tries[0].Status != 429 {
		t.Fatalf("the next turn asked %+v first", r.Tries)
	}
}

// #2156: once Antigravity has turned the system prompt away, the group's
// next turn carrying it doesn't wait on the same refusal: Antigravity isn't
// asked, the member after it answers, and the skip says why. A turn without
// that prompt still asks it, the account alone is still asked (and told
// as before), and once the last refusal is old, it is asked again, so a
// block Google lifts is noticed.
func TestAntigravityTurnedAwayIsForeseen(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	asked := 0
	s, askedUsage := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, turnedAway)
	})
	if code, out := turnOn(t, s, claudeTurn(claudeCodeSystem)); code != 200 || !strings.Contains(out, "from the other") {
		t.Fatalf("the first turn: %d %s", code, out)
	}
	if asked != 1 {
		t.Fatalf("the first turn asked Antigravity %d times", asked)
	}
	// the next turn with that prompt: only the other member is asked
	if code, out := turnOn(t, s, claudeTurn(claudeCodeSystem)); code != 200 || !strings.Contains(out, "from the other") {
		t.Fatalf("the next turn: %d %s", code, out)
	}
	if asked != 1 {
		t.Fatalf("Antigravity was asked again for the prompt it just turned away")
	}
	if r := lastRoute(s); len(r.Tries) != 1 || r.Tries[0].ID != "other" {
		t.Fatalf("the next turn asked %+v", r.Tries)
	}
	if c := s.Recent()[0]; !strings.Contains(c.Fallback, antigravityTurnedAwayHint) {
		t.Fatalf("the skip doesn't say why: %q", c.Fallback)
	}
	if *askedUsage != 0 {
		t.Fatalf("the account's usage was asked again %d times", *askedUsage)
	}
	if _, ok := restOf("antigravity@u@example.com"); ok {
		t.Fatalf("the skipped account rests: %v", rests())
	}
	// the Agent SDK's identity line is foreseen as the billing line is
	if code, _ := turnOn(t, s, claudeTurn("You are a Claude agent, built on Anthropic's Claude Agent SDK.")); code != 200 || asked != 1 {
		t.Fatalf("the SDK's turn: %d, Antigravity asked %d times", code, asked)
	}
	// Antigravity alone, the last one left: still asked, and told why
	code, out := turnOn(t, s, directTurn(claudeCodeSystem, false))
	if code != http.StatusBadRequest || !strings.Contains(out, antigravityTurnedAwayHint) || asked != 2 {
		t.Fatalf("Antigravity alone: %d (asked %d times) %s", code, asked, out)
	}
	// an old refusal foresees nothing: it is asked again
	s.agTurnedAway.Store(time.Now().Add(-agForeseeing - time.Minute).UnixNano())
	if code, _ := turnOn(t, s, claudeTurn(claudeCodeSystem)); code != 200 || asked != 3 {
		t.Fatalf("after an old refusal: %d, Antigravity asked %d times", code, asked)
	}
	// a turn without it (a title, Codex) still asks Antigravity first; last,
	// as this upstream answers it with the 429 that is a quota and rests it
	if code, _ := turnOn(t, s, claudeTurn("You are a helpful assistant.")); code != 200 || asked != 4 {
		t.Fatalf("a turn without that prompt: %d, Antigravity asked %d times", code, asked)
	}
}

// A 429 that really is the plan used up — no such system prompt asking —
// still rests the account, as it did (#147, #530).
func TestAntigravityQuotaStillRests(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	s, askedUsage := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, turnedAway)
	})
	code, out := turnOn(t, s, claudeTurn("You are a helpful assistant."))
	if code != 200 || !strings.Contains(out, "from the other") {
		t.Fatalf("the turn: %d %s", code, out)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 429 || r.Tries[0].Fail != failQuota || r.Tries[0].Rest == nil {
		t.Fatalf("the used-up try: %+v", r.Tries)
	}
	if d := time.Until(r.Tries[0].Rest.Until); d < 14*time.Minute || d > 15*time.Minute {
		t.Fatalf("rests %v, not the 15 minutes a used-up plan takes", d)
	}
	if *askedUsage != 1 {
		t.Fatalf("the account's usage was asked again %d times, once as it was", *askedUsage)
	}
	if _, ok := restOf("antigravity@u@example.com"); !ok {
		t.Fatal("antigravity@u@example.com doesn't rest for a plan used up")
	}
}

// Only the refusal itself leaves the account alone. A Claude Code turn is
// no excuse for an account that really is broken, overloaded or denied: it
// rests as it would on any other request (the maintenance review of #873).
func TestAntigravityTurnedAwayIsOnlyTheRefusal(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	for _, tc := range []struct {
		name   string
		status int
		body   string
		fail   string
	}{
		{"500 INTERNAL", http.StatusInternalServerError, `{"error":{"code":500,"message":"internal error","status":"INTERNAL"}}`, failOther},
		{"503 UNAVAILABLE", http.StatusServiceUnavailable, `{"error":{"code":503,"message":"unavailable","status":"UNAVAILABLE"}}`, failOther},
		{"403 PERMISSION_DENIED", http.StatusForbidden, `{"error":{"code":403,"message":"denied","status":"PERMISSION_DENIED"}}`, failOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			code, out := turnOn(t, s, claudeTurn(claudeCodeSystem))
			if code != 200 || !strings.Contains(out, "from the other") {
				t.Fatalf("the turn: %d %s", code, out)
			}
			r := lastRoute(s)
			if len(r.Tries) != 2 || r.Tries[0].Status != tc.status || r.Tries[0].Fail != tc.fail || r.Tries[0].Rest == nil {
				t.Fatalf("a %d on a Claude Code turn isn't the refusal: %+v", tc.status, r.Tries)
			}
			if _, ok := restOf("antigravity@u@example.com"); !ok {
				t.Fatalf("antigravity@u@example.com doesn't rest for a %d, as it wouldn't on any other request", tc.status)
			}
		})
	}
}

// The refusal costs the account nothing, but it still happened, and the
// ledger keeps it: a Claude Code turn's 429 is as visible as any other
// failed call (the maintenance review of #873).
func TestAntigravityTurnedAwayStillLandsInTheLedger(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, turnedAway)
	})
	if code, out := turnOn(t, s, claudeTurn(claudeCodeSystem)); code != 200 || !strings.Contains(out, "from the other") {
		t.Fatalf("the turn: %d %s", code, out)
	}
	rows, _, _ := usage.Ledger(usage.All, usage.Filter{Provider: "antigravity"})
	if len(rows) == 0 {
		t.Fatal("the refusal is in no usage row")
	}
	for _, row := range rows {
		if row.Status == http.StatusTooManyRequests && row.Failed() {
			return
		}
	}
	t.Fatalf("no 429 row of the refusal among %d: %+v", len(rows), rows)
}

// Its mates are asked last, not first: they carry the same system prompt,
// so each of them is turned away just the same, and asking them first costs
// a round-trip apiece before the group reaches the member that answers
// (the maintenance review of #873).
func TestAntigravityTurnedAwayLeavesItsMatesForLast(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com", "v@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, turnedAway)
	})
	if code, out := turnOn(t, s, claudeTurn(claudeCodeSystem)); code != 200 || !strings.Contains(out, "from the other") {
		t.Fatalf("the turn: %d %s", code, out)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 {
		t.Fatalf("asked %d times: the second account of the member was asked before the one that answers: %+v", len(r.Tries), r.Tries)
	}
	if r.Tries[0].ID != "antigravity" || r.Tries[0].Status != 429 || r.Tries[1].ID != "other" {
		t.Fatalf("asked: %+v", r.Tries)
	}
	for _, user := range []string{"u@example.com", "v@example.com"} {
		if _, ok := restOf("antigravity@" + user); ok {
			t.Fatalf("antigravity@%s rests for a request it was turned away from", user)
		}
	}
}

// The refusal also comes said inside the reply: a 200 that opens with the
// same error event, where the Code Assist decoder keeps only its message.
// It leaves the account alone as the 429 does, and an error event of any
// other kind still rests it.
func TestAntigravityTurnedAwaySaidInsideTheReply(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	for _, tc := range []struct {
		name string
		body string
		rest bool
	}{
		{"the refusal", `data: {"error":{"message":"Resource has been exhausted (e.g. check quota)."}}`, false},
		{"another error", `data: {"error":{"message":"api key not valid"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(tc.body))
			})
			if code, out := turnOn(t, s, claudeTurn(claudeCodeSystem)); code != 200 || !strings.Contains(out, "from the other") {
				t.Fatalf("the turn: %d %s", code, out)
			}
			r := lastRoute(s)
			if len(r.Tries) != 2 || r.Tries[1].Status != 200 {
				t.Fatalf("the turn: %+v", r.Tries)
			}
			_, resting := restOf("antigravity@u@example.com")
			if resting != tc.rest {
				t.Fatalf("rests %v, want %v: %+v", resting, tc.rest, r.Tries)
			}
			if want := failPrompt; !tc.rest && r.Tries[0].Fail != want {
				t.Fatalf("the refusal said inside the reply: Fail=%q: %+v", r.Tries[0].Fail, r.Tries)
			}
		})
	}
}

// rests is what rests now, to say it when nothing should.
func rests() map[string]time.Time {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	out := map[string]time.Time{}
	for k, v := range restingUntil.m {
		if v.After(time.Now()) {
			out[k] = v
		}
	}
	return out
}

// antigravityQuota is Antigravity's 429 when the plan's own allowance on the
// model is used up: it names the quota and when it resets, which the refusal
// of #666 never does.
const antigravityQuota = `{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 3h19m30s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","domain":"cloudcode-pa.googleapis.com"}]}}`

// directTurn is a Claude Desktop chat on Antigravity's model itself, not a
// group: the one account is the last left to ask (#1425).
func directTurn(system string, stream bool) string {
	return `{"model":"antigravity/m","max_tokens":100,"stream":` + fmt.Sprint(stream) + `,"system":` + quote(system) + `,"messages":[{"role":"user","content":"hi"}]}`
}

// #1425: Claude Desktop's chat on Antigravity alone, with quota to spare.
// Nobody is left to ask, and the agent is told why at a status the
// Anthropic and OpenAI SDKs don't retry — Antigravity's own 429 had Claude
// Desktop ask again ten times over, each turned away the same. The Routing
// page tells it as the agent's prompt turned away, not a quota used up; the
// account doesn't rest and isn't asked again.
func TestAntigravityTurnedAwayLastIsNoQuota(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			asked := 0
			s, askedUsage := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
				asked++
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, turnedAway)
			})
			code, out := turnOn(t, s, directTurn(claudeCodeSystem, stream))
			if code != http.StatusBadRequest {
				t.Fatalf("the agent got %d, which its SDK retries: %s", code, out)
			}
			if !strings.Contains(out, "invalid_request_error") || !strings.Contains(out, "Resource has been exhausted") || !strings.Contains(out, antigravityTurnedAwayHint) {
				t.Fatalf("the agent isn't told Antigravity's words and why: %s", out)
			}
			if asked != 1 {
				t.Fatalf("Antigravity was asked %d times for a request it turns away every time", asked)
			}
			r := lastRoute(s)
			if r.Status != http.StatusBadRequest || len(r.Tries) != 1 {
				t.Fatalf("the route: %d %+v", r.Status, r.Tries)
			}
			if try := r.Tries[0]; try.Status != http.StatusTooManyRequests || try.Fail != failPrompt || try.Rest != nil {
				t.Fatalf("the try is told as %q (rest %v), not the agent's prompt turned away: %+v", try.Fail, try.Rest, try)
			}
			if *askedUsage != 0 {
				t.Fatalf("the account's usage was asked again %d times for a request it refused", *askedUsage)
			}
			if _, ok := restOf("antigravity@u@example.com"); ok {
				t.Fatalf("antigravity@u@example.com rests for a request it turned away: %v", rests())
			}
			rows, _, _ := usage.Ledger(usage.All, usage.Filter{Provider: "antigravity"})
			if len(rows) != 1 || rows[0].Status != http.StatusBadRequest || rows[0].ErrType != turnedAwayErrType {
				t.Fatalf("the request list's row: %+v", rows)
			}
		})
	}
}

// The same refusal said inside the reply (a 200 that opens with the error
// event), with nobody left: told the same, at a status not retried, and not
// asked again as a failure that may pass.
func TestAntigravityTurnedAwayInsideTheReplyLast(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	asked := 0
	s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"error":{"message":"Resource has been exhausted (e.g. check quota)."}}`))
	})
	code, out := turnOn(t, s, directTurn(claudeCodeSystem, true))
	if code != http.StatusBadRequest || !strings.Contains(out, "Antigravity: Resource has been exhausted (e.g. check quota). — "+antigravityTurnedAwayHint) {
		t.Fatalf("the agent got %d: %s", code, out)
	}
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
	if r := lastRoute(s); len(r.Tries) != 1 || r.Tries[0].Fail != failPrompt || r.Tries[0].Rest != nil {
		t.Fatalf("the try: %+v", r.Tries)
	}
}

// When the member after Antigravity's is out of its own allowance, the agent
// is told what Antigravity turned away and why, at a status it doesn't
// retry — not the other's quota, which waiting wouldn't fix either.
func TestAntigravityTurnedAwayThenAQuota(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, turnedAway)
	})
	spent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"weekly usage limit reached"}}`)
	}))
	t.Cleanup(spent.Close)
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Models: []string{"m"}, Anthropic: spent.URL}); err != nil {
		t.Fatal(err)
	}
	code, out := turnOn(t, s, claudeTurn(claudeCodeSystem))
	if code != http.StatusBadRequest || !strings.Contains(out, antigravityTurnedAwayHint) {
		t.Fatalf("the agent got %d: %s", code, out)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failPrompt || r.Tries[1].Fail != failQuota {
		t.Fatalf("the tries: %+v", r.Tries)
	}
}

// Antigravity's 429 for a plan really used up stays the quota it is on a
// Claude Code turn too: its words name the quota and when it resets, not
// the refusal's. It rests the account, goes on to the next member, and with
// nobody left the agent gets the 429 (Rest-of-#666 review: refusal, account
// or model, told apart by what was said, not by the system prompt alone).
func TestAntigravityQuotaOnAClaudeCodeTurnStaysQuota(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	t.Run("in a group", func(t *testing.T) {
		s, askedUsage := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, antigravityQuota)
		})
		if code, out := turnOn(t, s, claudeTurn(claudeCodeSystem)); code != 200 || !strings.Contains(out, "from the other") {
			t.Fatalf("the turn: %d %s", code, out)
		}
		r := lastRoute(s)
		if len(r.Tries) != 2 || r.Tries[0].Fail != failQuota || r.Tries[0].Rest == nil {
			t.Fatalf("the used-up try: %+v", r.Tries)
		}
		if strings.Contains(r.Tries[0].Error, antigravityTurnedAwayHint) {
			t.Fatalf("a used-up plan is said not to be a quota: %s", r.Tries[0].Error)
		}
		if *askedUsage != 1 {
			t.Fatalf("the account's usage was asked again %d times, once as for any used-up plan", *askedUsage)
		}
	})
	t.Run("the last left", func(t *testing.T) {
		s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, antigravityQuota)
		})
		code, out := turnOn(t, s, directTurn(claudeCodeSystem, false))
		if code != http.StatusTooManyRequests || strings.Contains(out, antigravityTurnedAwayHint) {
			t.Fatalf("the agent got %d: %s", code, out)
		}
		if r := lastRoute(s); len(r.Tries) != 1 || r.Tries[0].Fail != failQuota {
			t.Fatalf("the try: %+v", r.Tries)
		}
	})
}

// The Routing page says the note apart from Antigravity's words, in its own
// language, only when it knows the very words the gateway adds.
func TestAntigravityTurnedAwayHintIsTheRoutingPages(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	b, err := os.ReadFile(filepath.Join("..", "gui", "assets", "plugins", "routing", "client.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), quote(antigravityTurnedAwayHint)) {
		t.Fatal("plugins/routing/client.js doesn't know the note the gateway adds to Antigravity's refusal")
	}
}

// Antigravity's 429 for a model's quota used up says when it is back,
// "Your quota will reset after 3h19m30s.", and the account rests that long:
// read as a quota that says nothing, it rested 15 minutes, and the account
// first in order was asked again, and refused, every quarter of an hour
// while the next ticked one had all its quota left (Dani Ariyanto on
// Discord, 10-24: OpenCode on Gemini 3.8 Flash, seven accounts).
func TestAntigravityQuotaRestsUntilItsReset(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	var asked []string
	s, _ := antigravityGroupFor(t, []string{"u1@example.com", "u2@example.com"}, "gemini-3.8-flash", func(w http.ResponseWriter, r *http.Request) {
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		asked = append(asked, who)
		if who == "u1@example.com" {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, antigravityQuota)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"from u2"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}}`))
	})
	turn := func(n int) (int, string) {
		body := fmt.Sprintf(`{"model":"antigravity/gemini-3.8-flash","stream":false,"messages":[{"role":"system","content":"You are opencode, an interactive CLI tool."},{"role":"user","content":"hi %d"}]}`, n)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	if code, out := turn(1); code != 200 || !strings.Contains(out, "from u2") || !slices.Equal(asked, []string{"u1@example.com", "u2@example.com"}) {
		t.Fatalf("the turn: %d %s, asked %v", code, out, asked)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failQuota || r.Tries[0].Rest == nil {
		t.Fatalf("the used-up try: %+v", r.Tries)
	}
	want := 3*time.Hour + 19*time.Minute + 30*time.Second
	if d := time.Until(r.Tries[0].Rest.Until); r.Tries[0].Rest.By != "resets" || d < want-time.Minute || d > want {
		t.Fatalf("u1 rests %v by %q, not the %v its quota resets after", d, r.Tries[0].Rest.By, want)
	}
	asked = nil
	if code, _ := turn(2); code != 200 || !slices.Equal(asked, []string{"u2@example.com"}) {
		t.Fatalf("the next turn: %d, asked %v", code, asked)
	}

	for _, c := range []struct {
		body string
		want time.Duration
	}{
		{antigravityQuota, want},
		{`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 45m2s.","status":"RESOURCE_EXHAUSTED"}}`, 45*time.Minute + 2*time.Second},
		{`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 26s.","status":"RESOURCE_EXHAUSTED"}}`, 26 * time.Second},
		{`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 155h2m1s.","status":"RESOURCE_EXHAUSTED"}}`, 155*time.Hour + 2*time.Minute + time.Second},
		{`{"error":{"code":429,"message":"Your quota will reset after 200h0m0s.","status":"RESOURCE_EXHAUSTED"}}`, 0}, // past longestWritten
		{`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after ","status":"RESOURCE_EXHAUSTED"}}`, 0},
		{turnedAway, 0},
	} {
		if got := resetsAfter([]byte(c.body)); got != c.want {
			t.Errorf("%s: %v, want %v", c.body, got, c.want)
		}
	}
}
