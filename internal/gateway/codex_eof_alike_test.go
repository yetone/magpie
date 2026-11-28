package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Zanghaihuase6m on X: a Cindy subagent's turn on two ChatGPT accounts
// ended in 502 "Codex: Post …/backend-api/codex/responses: EOF" after
// 1.7s. Each account's connection had closed before a byte of a reply,
// and the two errors read word for word alike while Codex was answering
// other requests, so the accounts loop took them for a 5xx the request
// itself gets (plainUnavailable, sameRefusal) and told the agent at
// once, where the last one left is asked again after a moment, as for
// any 502 that passes. A connection closed unanswered is no vendor's
// words about the request: it is asked again, and the retry answers.
func TestCodexEOFOnTwoAccountsAskedAgain(t *testing.T) {
	fresh(t)
	home := os.Getenv("HOME")
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token": claims(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("a@example.com", "acct-a")), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "codex", "user": "b@example.com", "plan": "pro", "on": true, "seen": time.Now(), "auth": auth("b@example.com", "acct-b")},
	}), 0o600)

	var drop atomic.Int32 // posts to close unanswered, from now on
	var mu sync.Mutex
	var posts []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// a fresh connection each time: one kept alive and closed is
		// sent again by the transport itself (replayable), unseen here
		w.Header().Set("Connection", "close")
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write(mustJSON(map[string]any{"models": []map[string]any{{"slug": "gpt-6.2-astra", "display_name": "GPT-6.2-Astra",
				"visibility": "list", "priority": 1, "supported_reasoning_levels": []map[string]any{{"effort": "medium"}},
				"default_reasoning_level": "medium", "context_window": 272000, "supported_in_api": true}}}))
			return
		}
		io.ReadAll(r.Body)
		mu.Lock()
		posts = append(posts, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		if drop.Add(-1) >= 0 {
			if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
				c.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.2-astra"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })

	s := New()
	turn := func() (int, string) {
		t.Helper()
		// Cindy's Codex's subagent turn, as it sends it
		body := `{"model":"codex/gpt-6.2-astra","instructions":"You are Codex, a coding agent based on GPT-5.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}],"tools":[],"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"medium","summary":"auto"},"store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"019ac9c4-cindy"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+TokenFor("cindy"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-openai-subagent", "collab_spawn")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// Codex answers: it is up, as it was for the reporter's other turns
	drop.Store(0)
	if code, out := turn(); code != 200 {
		t.Fatalf("first turn: %d %s", code, out)
	}
	mu.Lock()
	posts = nil
	mu.Unlock()

	// both accounts' connections close unanswered, then one answers
	drop.Store(2)
	code, out := turn()
	mu.Lock()
	got := strings.Join(posts, ",")
	mu.Unlock()
	if code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("two accounts' EOF: status %d after posts %s, not asked again: %.300s", code, got, out)
	}
	if n := len(strings.Split(got, ",")); n != 3 {
		t.Errorf("posts %s, want each account once and the last one again", got)
	}
}
