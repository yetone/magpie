package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// relayOfChatGPT is a Responses provider in front of the ChatGPT backend,
// as Sub2API is: it answers the lead and seals its spawn_agent task, and
// the task it sealed it opens again. got holds every request it was sent.
type relayOfChatGPT struct {
	mu  sync.Mutex
	got []string
}

func (f *relayOfChatGPT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, string(b))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"on it"}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
}

func (f *relayOfChatGPT) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func relayOn(t *testing.T, id string) *relayOfChatGPT {
	t.Helper()
	f := &relayOfChatGPT{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "sk-" + id, Models: []string{"gpt-6-astra"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	return f
}

// codexSubTurn is a request Codex, signed in to ChatGPT, sends magpie for one
// of magpie's models, with the web_search Codex offers on every turn.
func codexSubTurn(s *Server, model, session, parent, input string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body := `{"model":"` + model + `","stream":true,"tools":[{"type":"web_search","external_web_access":true}],"input":[` + input + `]}`
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("session_id", session)
	if parent != "" {
		req.Header.Set("x-codex-parent-thread-id", parent)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const leadAsk = `{"type":"message","role":"user","content":[{"type":"input_text","text":"spawn a worker"}]}`

// #1109: Codex on Ultra with s2a/gpt-6-astra for both the lead and its
// subagent, s2a a Sub2API relay of the ChatGPT backend. The task the lead's
// spawn_agent got sealed goes back to s2a, which sealed it, as it came —
// not turned away before s2a is asked, nor translated without it.
func TestSealedTaskGoesBackToTheRelayThatSealedIt(t *testing.T) {
	fresh(t)
	s2a := relayOn(t, "s2a")
	s := New()
	if rec := codexSubTurn(s, "s2a/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	rec := codexSubTurn(s, "s2a/gpt-6-astra", "worker-1", "lead-1", sealedHandoff)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "on it") {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	got := s2a.requests()
	if len(got) != 2 || !strings.Contains(got[1], `"encrypted_content":"gAAAAATest_ciphertext=="`) || !strings.Contains(got[1], `"agent_message"`) {
		t.Fatalf("s2a was sent %d requests; the subagent's: %v", len(got), got[len(got)-1:])
	}
}

// A subagent on another provider than the one that sealed its task is
// still turned away before anyone is asked, and told which one sealed it,
// not to use a Magpie-served model it already uses.
func TestSealedTaskRefusedByAnotherRelay(t *testing.T) {
	fresh(t)
	s2a := relayOn(t, "s2a")
	other := relayOn(t, "s2b")
	s := New()
	if rec := codexSubTurn(s, "s2b/gpt-6-astra", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	rec := codexSubTurn(s, "s2a/gpt-6-astra", "worker-1", "lead-1", sealedHandoff)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "(s2b)") || strings.Contains(rec.Body.String(), "Magpie-served") || strings.Contains(rec.Body.String(), "gAAAAA") {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(s2a.requests()); n != 0 || len(other.requests()) != 1 {
		t.Fatalf("s2a asked %d times, s2b %d", n, len(other.requests()))
	}
	// nor does a subagent whose lead magpie never answered go to a relay
	rec = codexSubTurn(s, "s2a/gpt-6-astra", "worker-2", "lead-elsewhere", sealedHandoff)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "s2a/gpt-6-astra is neither") || len(s2a.requests()) != 0 {
		t.Fatalf("unknown lead: %d %s, s2a asked %d times", rec.Code, rec.Body.String(), len(s2a.requests()))
	}
}
