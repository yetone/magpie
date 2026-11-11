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

// Coordinate_siwu on X: two ChatGPT subscription accounts of different
// emails, one answers 529 (overloaded) to the model however often it is
// asked, the other serves it. The turn goes to the other account.
func TestCodexOverloadedAccountFailsOverToOther(t *testing.T) {
	const turn = `"messages":[{"role":"user","content":"ping"}]`
	said := map[string]reply{
		"529":        {529, "application/json", overloadedStatus},
		"529 empty":  {529, "", ""},
		"529 html":   {529, "text/html", "<html><body>Site is overloaded</body></html>"},
		"529 detail": {529, "application/json", `{"detail":"Too many requests, please slow down"}`},
		"529 rate":   {529, "application/json", `{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached for gpt-5.5 on tokens per min (TPM): Limit 30000"}}`},
		"529 usage":  {529, "application/json", `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":7200}}`},
		"500":        {500, "application/json", `{"error":{"message":"The server had an error while processing your request","type":"server_error"}}`},
		"503":        {503, "application/json", `{"error":{"message":"The model is overloaded. Please try again later.","type":"server_error"}}`},
		"503 plain":  {503, "text/plain", "upstream connect error or disconnect/reset before headers"},
		"429 rate":   {429, "application/json", `{"error":{"type":"rate_limit_exceeded","code":"rate_limit_exceeded","message":"Rate limit reached for gpt-5.5 in organization org-x on tokens per min (TPM): Limit 30000"}}`},
		"stream 200": {200, "", sse(append(append([]string{}, overloadedLead...), overloadedFailed)...)},
	}
	post := func(s *Server, path, body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		if path == CodexPath+"/responses" {
			req.Header.Set("Authorization", "Bearer chatgpt-token")
			req.Header.Set("chatgpt-account-id", "acct-1")
		} else {
			req.Header.Set("Authorization", "Bearer "+TokenFor("opencode"))
			if strings.HasSuffix(path, "/messages") {
				req.Header.Set("anthropic-version", "2023-06-01")
			}
		}
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	doors := map[string][2]string{
		"codex":       {CodexPath + "/responses", oneAccountTurn},
		"chat stream": {"/v1/chat/completions", `{"model":"codex/gpt-5.5","stream":true,` + turn + `}`},
		"chat whole":  {"/v1/chat/completions", `{"model":"codex/gpt-5.5",` + turn + `}`},
		"messages":    {"/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":50,` + turn + `}`},
		"messages s":  {"/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":50,"stream":true,` + turn + `}`},
	}
	for _, routing := range []string{"", provider.Ordered, provider.Rotate, provider.Drain} {
		for sn, x := range said {
			for dn, door := range doors {
				t.Run(routing+"/"+sn+"/"+dn, func(t *testing.T) {
					codexSignedIn(t, "spare@example.com")
					if routing != "" {
						if err := provider.SetRouting("codex", routing); err != nil {
							t.Fatal(err)
						}
					}
					var mu sync.Mutex
					seen := map[string]int{}
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						io.ReadAll(r.Body)
						id := r.Header.Get("chatgpt-account-id")
						mu.Lock()
						seen[id]++
						mu.Unlock()
						y := x
						if id != "acct-1" {
							y = reply{200, "", codexAnswer}
						}
						if y.ctype != "" {
							w.Header().Set("Content-Type", y.ctype)
						} else {
							w.Header()["Content-Type"] = nil
						}
						w.WriteHeader(y.code)
						io.WriteString(w, y.body)
					}))
					t.Cleanup(up.Close)
					was := provider.CodexBase
					provider.CodexBase = up.URL + "/backend-api/codex"
					t.Cleanup(func() { provider.CodexBase = was })
					pause := retryPause
					retryPause = time.Millisecond
					t.Cleanup(func() { retryPause = pause })
					code, body := post(New(), door[0], door[1])
					mu.Lock()
					defer mu.Unlock()
					if code != 200 || seen["acct-2"] == 0 {
						t.Fatalf("%d %.200s (asked %v)", code, body, seen)
					}
				})
			}
		}
	}
}

// A conversation kept on the account that answered its first turn
// (affinity) moves to the other when that account is overloaded
// however often it is asked, as a first turn does.
func TestCodexKeptConversationMovesOffOverloadedAccount(t *testing.T) {
	const turn = `"messages":[{"role":"user","content":"ping"}]`
	for dn, door := range map[string][2]string{
		"codex":       {CodexPath + "/responses", oneAccountTurn},
		"chat stream": {"/v1/chat/completions", `{"model":"codex/gpt-5.5","stream":true,` + turn + `}`},
		"messages":    {"/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":50,` + turn + `}`},
	} {
		t.Run(dn, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			var mu sync.Mutex
			var first string
			bad := false
			seen := map[string]int{}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				id := r.Header.Get("chatgpt-account-id")
				mu.Lock()
				defer mu.Unlock()
				if first == "" {
					first = id
				}
				if bad {
					seen[id]++
				}
				if bad && id == first {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(529)
					io.WriteString(w, overloadedStatus)
					return
				}
				w.Header()["Content-Type"] = nil
				io.WriteString(w, codexAnswer)
			}))
			t.Cleanup(up.Close)
			was := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = was })
			pause := retryPause
			retryPause = time.Millisecond
			t.Cleanup(func() { retryPause = pause })
			s := New()
			post := func() (int, string) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", door[0], strings.NewReader(door[1]))
				if door[0] == CodexPath+"/responses" {
					req.Header.Set("Authorization", "Bearer chatgpt-token")
					req.Header.Set("chatgpt-account-id", "acct-1")
				} else {
					req.Header.Set("Authorization", "Bearer "+TokenFor("opencode"))
					req.Header.Set("anthropic-version", "2023-06-01")
				}
				s.Handler().ServeHTTP(rec, req)
				return rec.Code, rec.Body.String()
			}
			if code, body := post(); code != 200 {
				t.Fatalf("first turn %d %.200s", code, body)
			}
			mu.Lock()
			bad = true
			mu.Unlock()
			code, body := post()
			mu.Lock()
			defer mu.Unlock()
			if code != 200 {
				t.Fatalf("second turn %d %.200s (asked after: %v, first %s)", code, body, seen, first)
			}
		})
	}
}

// Codex's own way to the gateway is a WebSocket: a turn on the account
// that is overloaded goes on to the other account's, as an HTTP turn does.
func TestCodexSocketOverloadedAccountFailsOver(t *testing.T) {
	for name, x := range map[string]reply{
		"529":        {529, "application/json", overloadedStatus},
		"503":        {503, "application/json", `{"error":{"message":"The model is overloaded. Please try again later.","type":"server_error"}}`},
		"stream 200": {200, "", sse(append(append([]string{}, overloadedLead...), overloadedFailed)...)},
	} {
		t.Run(name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			var mu sync.Mutex
			seen := map[string]int{}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				id := r.Header.Get("chatgpt-account-id")
				mu.Lock()
				seen[id]++
				mu.Unlock()
				y := x
				if id != "acct-1" {
					y = reply{200, "", codexAnswer}
				}
				if y.ctype != "" {
					w.Header().Set("Content-Type", y.ctype)
				} else {
					w.Header()["Content-Type"] = nil
				}
				w.WriteHeader(y.code)
				io.WriteString(w, y.body)
			}))
			t.Cleanup(up.Close)
			was := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = was })
			pause := retryPause
			retryPause = time.Millisecond
			t.Cleanup(func() { retryPause = pause })
			_, ts := sockServer(t)
			h := http.Header{"Authorization": {"Bearer chatgpt-token"}, "Chatgpt-Account-Id": {"acct-1"},
				"Openai-Beta": {"responses_websockets=2026-02-06"}, "User-Agent": {"codex_cli_rs/0.166.0"}}
			c, _, err := dialSock(t, ts, CodexPath+"/responses", h)
			if err != nil {
				t.Fatal(err)
			}
			got := c.turn(`{"type":"response.create","model":"gpt-5.5","stream":true,"store":false,"input":[` + userHi + `]}`)
			mu.Lock()
			defer mu.Unlock()
			if last(got).Get("type").String() != "response.completed" || seen["acct-2"] == 0 {
				t.Fatalf("%v (asked %v)", got, seen)
			}
		})
	}
}
