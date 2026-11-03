package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// chatgptSeen stands in for the ChatGPT backend, noting each request's
// account and model.
type chatgptSeen struct {
	mu     sync.Mutex
	models []string
}

func (c *chatgptSeen) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.models...)
}

// codexSubagentSetup is Codex signed in to ChatGPT with another account on
// in magpie, both serving gpt-5.5 and gpt-6-astra, and the ChatGPT backend
// a fake noting what it was asked.
func codexSubagentSetup(t *testing.T) *chatgptSeen {
	t.Helper()
	codexSignedIn(t, "spare@example.com")
	home, _ := os.UserHomeDir()
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"etag":"\"v1\"","models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","priority":1,"context_window":272000},
		{"slug":"gpt-6-astra","display_name":"GPT-6 Astra","visibility":"list","priority":2,"context_window":272000}]}`), 0o644)
	provider.ForgetAccounts()
	c := &chatgptSeen{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(404)
			return
		}
		c.mu.Lock()
		c.models = append(c.models, modelOf(b))
		c.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"`+modelOf(b)+`"}}`,
			`data: {"type":"response.output_text.delta","delta":"on it"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return c
}

// codexAsks is a request of Codex's, a subagent's when kind is
// collab_spawn, a lead's when it is "".
func codexAsks(s *Server, model, kind, input string) *httptest.ResponseRecorder {
	return codexAsksIn(s, "worker-1", model, kind, input)
}

// codexAsksIn is codexAsks from the thread session.
func codexAsksIn(s *Server, session, model, kind, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body := `{"model":"` + model + `","stream":true,"input":[` + input + `]}`
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("User-Agent", "codex_cli_rs/0.159.0 (Mac OS 26.2.0; arm64)")
	req.Header.Set("session_id", session)
	if kind != "" {
		req.Header.Set("x-openai-subagent", kind)
		req.Header.Set("x-codex-parent-thread-id", "lead-1")
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// willz on Discord: Codex's lead puts each subagent on a model of its own
// pick in spawn_agent, and Codex's config can't overrule it. With a model
// set for Codex's subagents, a subagent's sealed task on one of Codex's
// own models goes to a ChatGPT account on that model instead, and the
// trace says what it was asked for and what it went on.
func TestCodexSubagentPutOnTheModelSet(t *testing.T) {
	up := codexSubagentSetup(t)
	if err := provider.SetCodexSubagentModel("codex/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := codexAsks(s, "gpt-5.5", "collab_spawn", sealedTaskFernet)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "on it") {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 1 || got[0] != "gpt-6-astra" {
		t.Fatalf("ChatGPT was asked for %v, not gpt-6-astra", got)
	}
	r := lastRoute(s)
	if r.Subagent == nil || *r.Subagent != (SubagentPick{Asked: "gpt-5.5", To: "codex/gpt-6-astra"}) || r.Model != "gpt-5.5" || r.Kind != "collab_spawn" {
		t.Fatalf("trace: model %q kind %q subagent %+v", r.Model, r.Kind, r.Subagent)
	}
}

// A subagent the lead put on another provider's model (a relay's) — its
// task sealed for ChatGPT accounts, so refused there before anyone is
// asked — goes on the model set for Codex's subagents, a ChatGPT
// account's, and the relay isn't asked.
func TestCodexSubagentOnAnotherProviderPutOnTheModelSet(t *testing.T) {
	up := codexSubagentSetup(t)
	relay := relayOn(t, "third")
	if err := provider.SetCodexSubagentModel("codex/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := codexAsks(s, "third/gpt-6-astra", "collab_spawn", sealedTaskFernet)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "on it") {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 1 || got[0] != "gpt-6-astra" {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if n := len(relay.requests()); n != 0 {
		t.Fatalf("the relay was asked %d times", n)
	}
	r := lastRoute(s)
	if r.Subagent == nil || *r.Subagent != (SubagentPick{Asked: "third/gpt-6-astra", To: "codex/gpt-6-astra"}) || r.Model != "third/gpt-6-astra" {
		t.Fatalf("trace: model %q subagent %+v", r.Model, r.Subagent)
	}
}

// The lead's own turns, and every request with no model set for the
// subagents, go on the model asked for, the trace saying nothing of it.
func TestCodexSubagentModelLeavesOtherRequests(t *testing.T) {
	up := codexSubagentSetup(t)
	s := New()
	// none set: a subagent as asked
	if rec := codexAsks(s, "gpt-5.5", "collab_spawn", sealedTaskFernet); rec.Code != 200 {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.Subagent != nil {
		t.Fatalf("none set, traced %+v", r.Subagent)
	}
	if err := provider.SetCodexSubagentModel("codex/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	// set: the lead's turn and a title as asked
	for _, kind := range []string{"", "thread_title", "guardian"} {
		if rec := codexAsks(s, "gpt-5.5", kind, leadAsk); rec.Code != 200 {
			t.Fatalf("%q: %d %s", kind, rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); r.Subagent != nil {
			t.Fatalf("%q traced %+v", kind, r.Subagent)
		}
	}
	got := up.asked()
	for i, m := range got {
		if m != "gpt-5.5" {
			t.Fatalf("request %d went on %s: %v", i, m, got)
		}
	}
}

// The model set no longer served by a ChatGPT account in magpie (its
// account signed out, or the model gone from its plan): a subagent goes on
// the model its lead asked for, and the trace says why it wasn't put on
// the one set. One of Codex's own bare names can't be set at all.
func TestCodexSubagentModelNotServedKeepsTheAsked(t *testing.T) {
	up := codexSubagentSetup(t)
	if err := provider.SetCodexSubagentModel("gpt-6-astra"); err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("set a bare name: %v", err)
	}
	// kept as set, as a backup put back on a computer whose accounts
	// differ sets it
	if err := provider.SetCodexSubagentModel("codex/gpt-9"); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := codexAsks(s, "gpt-5.5", "collab_spawn", sealedTaskFernet)
	if rec.Code != 200 {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("ChatGPT was asked for %v, not the model asked", got)
	}
	r := lastRoute(s)
	if r.Subagent == nil || r.Subagent.To != "codex/gpt-9" || r.Subagent.Kept != "no ChatGPT account on in magpie serves it" {
		t.Fatalf("trace: %+v", r.Subagent)
	}
}

// A gateway key held to some models (#882) that doesn't name the model set
// for Codex's subagents: a subagent on it goes on the model asked for, as
// the key may use that one, and the trace says why.
func TestCodexSubagentModelHeldKeyKeepsTheAsked(t *testing.T) {
	up := codexSubagentSetup(t)
	if err := provider.SetCodexSubagentModel("codex/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	s := New()
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"codex/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[`+sealedTaskFernet+`]}`))
	req.Header.Set("Authorization", "Bearer "+secrets[0])
	req.Header.Set("User-Agent", "codex_cli_rs/0.159.0 (Mac OS 26.2.0; arm64)")
	req.Header.Set("x-openai-subagent", "collab_spawn")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("ChatGPT was asked for %v, not the model asked", got)
	}
	if r := lastRoute(s); r.Subagent == nil || r.Subagent.Kept != "the gateway key may not use it" {
		t.Fatalf("trace: %+v", r.Subagent)
	}
}
