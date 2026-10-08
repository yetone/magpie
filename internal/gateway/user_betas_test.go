package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// betaRelay is an Anthropic endpoint that turns away betas it doesn't
// know, as Bedrock does, and keeps the anthropic-beta lines of each request.
type betaRelay struct {
	mu      sync.Mutex
	refuses []string
	seen    [][]string
}

func (b *betaRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	b.mu.Lock()
	b.seen = append(b.seen, slices.Clone(r.Header.Values("Anthropic-Beta")))
	b.mu.Unlock()
	var bad []string
	for _, v := range r.Header.Values("Anthropic-Beta") {
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); slices.Contains(b.refuses, x) {
				bad = append(bad, "`"+x+"`")
			}
		}
	}
	if len(bad) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Unexpected value(s) `+strings.Join(bad, ", ")+" for the `anthropic-beta` header.\"}}")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
}

func (b *betaRelay) last() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seen[len(b.seen)-1]
}

// PAMI on Discord: a provider's header.anthropic-beta replaced the betas
// the agent asked (Claude Code's own, 1M context's) rather than adding to
// them. They are now one list, the agent's first and then the user's, each
// once; the user's are left out only when the provider refuses them, and
// the agent's refused ones still go on the retry without them.
func TestUserBetasMergeWithTheAgents(t *testing.T) {
	fresh(t)
	up := &betaRelay{refuses: []string{"redact-thinking-2026-02-12", "my-refused-2026-01-01"}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models:  []string{"claude-opus-5-5"},
		Headers: map[string]string{"anthropic-beta": "fine-grained-tool-streaming-2025-05-14, ,claude-code-20250219,my-refused-2026-01-01"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(
			`{"model":"relay/claude-opus-5-5","max_tokens":20,"messages":[{"role":"user","content":"ping"}]}`))
		req.Header.Set("User-Agent", "claude-cli/2.1.289 (external, cli)")
		req.Header.Set("anthropic-beta", "claude-code-20250219,context-1m-2025-08-07,redact-thinking-2026-02-12")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		return rec.Code
	}
	ask()
	want := []string{"claude-code-20250219,context-1m-2025-08-07,fine-grained-tool-streaming-2025-05-14"}
	if got := up.last(); !slices.Equal(got, want) {
		t.Fatalf("betas sent %q, want %q", got, want)
	}
	up.mu.Lock()
	first := up.seen[0]
	up.mu.Unlock()
	if want := []string{"claude-code-20250219,context-1m-2025-08-07,redact-thinking-2026-02-12,fine-grained-tool-streaming-2025-05-14,my-refused-2026-01-01"}; !slices.Equal(first, want) {
		t.Fatalf("first ask sent %q, want %q", first, want)
	}

	// what it refused isn't asked again, the user's own included
	n := len(up.seen)
	ask()
	if got := up.last(); len(up.seen) != n+1 || !slices.Equal(got, want) {
		t.Fatalf("again: %d calls, betas %q", len(up.seen)-n, got)
	}
}

// A request with no betas of its own sends the user's alone.
func TestUserBetasAlone(t *testing.T) {
	fresh(t)
	up := &betaRelay{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL,
		Models: []string{"claude-opus-5-5"}, Headers: map[string]string{"Anthropic-Beta": "context-1m-2025-08-07"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"relay/claude-opus-5-5","max_tokens":20,"messages":[{"role":"user","content":"ping"}]}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := up.last(); !slices.Equal(got, []string{"context-1m-2025-08-07"}) {
		t.Fatalf("betas sent %q", got)
	}
}

// Claude Code signed in to claude.ai asks oauth-2025-04-20 with its
// sign-in, which magpie never passes on: a provider gets the other betas,
// as Claude Code with magpie's key asks them, and an oauth beta the user
// set on the provider (header.anthropic-beta) still goes.
func TestSignInBetaStaysWithMagpie(t *testing.T) {
	fresh(t)
	up := &betaRelay{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL, Models: []string{"claude-opus-5-5"}},
		{ID: "own", Name: "Own", Key: "k", Anthropic: srv.URL, Models: []string{"claude-opus-5-5"}, Headers: map[string]string{"anthropic-beta": "oauth-2025-04-20"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	ask := func(model, betas string) []string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(
			`{"model":"`+model+`","max_tokens":20,"messages":[{"role":"user","content":"ping"}]}`))
		req.Header.Set("User-Agent", "claude-cli/2.1.290 (external, cli)")
		req.Header.Set("Authorization", "Bearer sk-ant-oat01-"+strings.Repeat("a", 24)) // made up, of a sign-in's shape
		req.Header.Set("anthropic-beta", betas)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		return up.last()
	}
	for _, c := range []struct {
		model, asked string
		want         []string
	}{
		{"relay/claude-opus-5-5", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14", []string{"claude-code-20250219,interleaved-thinking-2025-05-14"}},
		{"relay/claude-opus-5-5", "oauth-2025-04-20", nil},
		{"own/claude-opus-5-5", "claude-code-20250219,oauth-2025-04-20", []string{"claude-code-20250219,oauth-2025-04-20"}},
	} {
		if got := ask(c.model, c.asked); !slices.Equal(got, c.want) {
			t.Errorf("%s asked %q: sent %q, want %q", c.model, c.asked, got, c.want)
		}
	}
}
