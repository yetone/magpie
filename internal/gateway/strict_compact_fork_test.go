package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// galaxy_huage on X: a provider with several accounts, Stays: Strict, and
// in Claude Code a conversation's fork (/branch, --resume --fork-session)
// and compact (/compact, auto-compact) went to another account than the
// rest of the session.
//
// The requests are Claude Code 2.1.325's own, captured from a real
// sandboxed run against a recording endpoint (testdata/
// claude_code_compact_fork, each text cut short and its paths put
// elsewhere): two turns, /compact (its summary request, then the count of
// the memory file it read again, then the next turn), and /branch's first
// turn. The compact keeps the session's x-claude-code-session-id; the
// branch has a new one and carries the whole conversation so far.

// keyRecorder is an Anthropic upstream that answers as each key and
// records which key each path was asked on.
type keyRecorder struct {
	mu    sync.Mutex
	tried []string // path → key, in order
}

func (k *keyRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	k.mu.Lock()
	k.tried = append(k.tried, r.URL.Path+" "+key)
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		io.WriteString(w, `{"input_tokens":2048}`)
		return
	}
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"from `+key+`"}],`+
		`"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":5,"cache_read_input_tokens":3000,"cache_creation_input_tokens":2000}}`)
}

func (k *keyRecorder) last() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.tried) == 0 {
		return ""
	}
	return k.tried[len(k.tried)-1]
}

// capturedClaudeCode is one of the captured requests, as an http.Request
// to the gateway, answered whole rather than streamed.
func capturedClaudeCode(t *testing.T, name string) *http.Request {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude_code_compact_fork", name))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Body    map[string]any    `json:"body"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.Body["stream"]; ok {
		rec.Body["stream"] = false
	}
	body, _ := json.Marshal(rec.Body)
	req := httptest.NewRequest("POST", rec.Path, strings.NewReader(string(body)))
	for k, v := range rec.Headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestStrictKeepsClaudeCodeCompactAndForkOnTheSessionsAccount(t *testing.T) {
	fresh(t)
	up := &keyRecorder{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "cc", Name: "CC", Key: "k1", Models: []string{"claude-opus-5-5"}, Anthropic: srv.URL + "/v1",
		Keys: []provider.KeyAccount{{Key: "k2"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("cc", provider.AffinityStrict); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func(req *http.Request) string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", req.URL.Path, rec.Code, rec.Body.String())
		}
		return up.last()
	}
	// another session takes the first account, so this one begins on the
	// second (spreadsSessions)
	other := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"cc/claude-opus-5-5","max_tokens":10,"messages":[{"role":"user","content":"another session"}]}`))
	other.Header.Set("x-claude-code-session-id", "99999999-2222-4333-8444-555555555555")
	otherKey := strings.Fields(send(other))[1]

	on := send(capturedClaudeCode(t, "1-turn.json"))
	if strings.HasSuffix(on, otherKey) {
		t.Fatalf("the session began on %s beside the other session", on)
	}
	key := strings.Fields(on)[1]
	for _, step := range []string{"2-turn.json", "3-compact.json", "4-count-tokens.json", "5-after-compact.json", "6-branch.json"} {
		got := send(capturedClaudeCode(t, step))
		if !strings.HasSuffix(got, " "+key) {
			t.Errorf("%s went to %q, not the session's %s", step, got, key)
		}
	}
	// the branch goes on there as a session of its own
	if got := send(capturedClaudeCode(t, "6-branch.json")); !strings.HasSuffix(got, " "+key) {
		t.Errorf("the branch's next request went to %q, not %s", got, key)
	}
}

// strictProvider saves an Anthropic provider "cc" of keys k1 and k2 at
// up, kept Strict and routed as routing says.
func strictProvider(t *testing.T, up string, routing string, keys ...string) {
	t.Helper()
	p := provider.Provider{ID: "cc", Name: "CC", Key: keys[0], Models: []string{"claude-opus-5-5"}, Anthropic: up + "/v1", Routing: routing}
	for _, k := range keys[1:] {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: k})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("cc", provider.AffinityStrict); err != nil {
		t.Fatal(err)
	}
}

// strictCCTurn is a Claude Code request of session (and subagent, when
// not ""), its messages msgs.
func strictCCTurn(session, agent, msgs string) *http.Request {
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"cc/claude-opus-5-5","max_tokens":10,"messages":`+msgs+`}`))
	req.Header.Set("x-claude-code-session-id", session)
	if agent != "" {
		req.Header.Set("x-claude-code-agent-id", agent)
	}
	return req
}

// Two sessions that open with the same words are two conversations: the
// second's history, answered elsewhere, isn't the first's, and it is
// spread as a session of its own.
func TestStrictForkNeedsTheSameHistory(t *testing.T) {
	fresh(t)
	up := &keyRecorder{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	strictProvider(t, srv.URL, "", "k1", "k2")
	s := New()
	send := func(req *http.Request) string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return strings.Fields(up.last())[1]
	}
	a := send(strictCCTurn("aaaaaaaa-0000-4000-8000-000000000001", "", `[{"role":"user","content":"hi"}]`))
	send(strictCCTurn("aaaaaaaa-0000-4000-8000-000000000001", "", `[{"role":"user","content":"hi"},{"role":"assistant","content":"from A"},{"role":"user","content":"more"}]`))
	// another session, the same opening, its own answer
	if b := send(strictCCTurn("bbbbbbbb-0000-4000-8000-000000000002", "", `[{"role":"user","content":"hi"},{"role":"assistant","content":"from B"},{"role":"user","content":"more"}]`)); b == a {
		t.Errorf("a session of its own went to %s, beside the one that opened alike", b)
	}
	// a fork of the first goes where it is
	if f := send(strictCCTurn("ffffffff-0000-4000-8000-000000000003", "", `[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":[{"type":"text","text":"from A","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":"more"},{"role":"assistant","content":"x"},{"role":"user","content":"in the branch"}]`)); f != a {
		t.Errorf("the fork went to %s, not %s", f, a)
	}
}

// Under Strict a subagent nobody has answered goes with its lead whatever
// the routing: In order put it on the first key while its lead stayed
// strictly on the other.
func TestStrictSubagentGoesWithItsLeadInOrder(t *testing.T) {
	fresh(t)
	up := &keyRecorder{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	strictProvider(t, srv.URL, provider.Ordered, "k1", "k2")
	s := New()
	send := func(req *http.Request) string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return strings.Fields(up.last())[1]
	}
	const session = "cccccccc-0000-4000-8000-000000000004"
	if got := send(strictCCTurn(session, "", `[{"role":"user","content":"lead"}]`)); got != "k1" {
		t.Fatalf("the lead began on %s", got)
	}
	// the user puts k2 first: the lead stays strictly where it is
	strictProvider(t, srv.URL, provider.Ordered, "k2", "k1")
	if got := send(strictCCTurn(session, "", `[{"role":"user","content":"lead"},{"role":"assistant","content":"ok"},{"role":"user","content":"go on"}]`)); got != "k1" {
		t.Fatalf("the lead moved to %s", got)
	}
	if got := send(strictCCTurn(session, "agent-1", `[{"role":"user","content":"a subagent's task"}]`)); got != "k1" {
		t.Errorf("the subagent went to %s, not its lead's k1", got)
	}
}
