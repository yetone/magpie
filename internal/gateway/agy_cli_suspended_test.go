package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// agyBridgeSuspended is the agy-cli plugin's stream for an account Google
// suspended, byte for byte: agy's "⚠ PERMISSION_DENIED (code 403): This
// service has been disabled…" made refusedError(ACCOUNT_DISABLED_REFUSED)
// and sent by streamErrorOf as an Anthropic error event after the 200
// (agy-cli 0.1.138 to 0.1.147). Its message is dmiedog's (plugins#1563).
const agyBridgeSuspended = `{"type":"error","error":{"type":"permission_error","message":"agy bridge: Google has disabled this account's access to Antigravity for violating the Terms of Service (HTTP 403: \"This service has been disabled in this account for violation of Terms of Service\"). Submit an appeal with Google before using this account again.","status":403,"noRetry":true}}`

// An account Google suspended, said in the plugin's stream after its 200,
// is asked once and rests as restricted, in every protocol the agent
// speaks: the error event names its 403, and the try fails as it. Read by
// its words instead, it was a 502 that passes, and the request was sent
// to the suspended account again and again (dmiedog's dsh, plugins#1563:
// tries=10 in a minute, each a new agy session at Google; here 15 tries
// on one account, 26 on two).
func TestAgyCLISuspendedAskedOnce(t *testing.T) {
	t.Setenv("FAKE_KIRO", "1") // the fake's claude model, on @ai-sdk/anthropic
	const (
		responses = `{"model":"%s/%s","stream":true,"input":"hi"}`
		chat      = `{"model":"%s/%s","stream":true,"messages":[{"role":"user","content":"hi"}]}`
		messages  = `{"model":"%s/%s","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
		whole     = `{"model":"%s/%s","input":"hi"}`
		bridge    = "event: error\ndata: " + agyBridgeSuspended + "\n\n"
		// the same refusal from a plugin on Chat or on Responses, in its
		// own protocol's error shape naming its status: their decoders
		// read the status as Anthropic's does
		said       = `agy bridge: Google has disabled this account's access to Antigravity for violating the Terms of Service (HTTP 403: \"This service has been disabled in this account for violation of Terms of Service\").`
		chatErr    = `data: {"error":{"message":"` + said + `","type":"permission_error","status":403}}` + "\n\n"
		respFailed = `event: response.failed` + "\n" + `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"permission_error","message":"` + said + `","status":403}}}` + "\n\n"
	)
	for _, c := range []struct {
		name, path, body, model, up string
		accounts                    int
	}{
		{"responses stream, one account", "/v1/responses", responses, "claude-opus-5.5", bridge, 1},
		{"responses stream, two accounts", "/v1/responses", responses, "claude-opus-5.5", bridge, 2},
		{"chat stream, two accounts", "/v1/chat/completions", chat, "claude-opus-5.5", bridge, 2},
		{"messages stream, two accounts", "/v1/messages", messages, "claude-opus-5.5", bridge, 2},
		{"responses whole, two accounts", "/v1/responses", whole, "claude-opus-5.5", bridge, 2},
		{"a Chat plugin's, two accounts", "/v1/messages", messages, "fake-1", chatErr, 2},
		{"a Responses plugin's, two accounts", "/v1/messages", messages, "fake-resp", respFailed, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			asked := map[string]int{}
			pid := besideFake(t, "agy-cli", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				mu.Lock()
				asked[r.Header.Get("x-api-key")+r.Header.Get("Authorization")]++
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, c.up)
			}))
			if c.accounts == 2 {
				if _, err := plugin.APIKey(context.Background(), "agy-cli", 0, nil, "k2", plugin.NewAccount); err != nil {
					t.Fatal(err)
				}
			}
			s := New()
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(fmt.Sprintf(c.body, pid, c.model))))
			if !strings.Contains(rec.Body.String(), "Google has disabled this account") {
				t.Fatalf("the agent isn't told why: %d %s", rec.Code, rec.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			n := 0
			for who, k := range asked {
				if k != 1 {
					t.Errorf("%s asked %d times, not once", who, k)
				}
				n += k
			}
			r := lastRoute(s)
			if n != c.accounts || len(r.Tries) != c.accounts {
				t.Fatalf("%d asks over %d tries for %d accounts: %v", n, len(r.Tries), c.accounts, asked)
			}
			for i, tr := range r.Tries {
				if tr.Status != http.StatusForbidden || tr.Fail != failRestricted || tr.Rest == nil || tr.Rest.By != "restricted" {
					t.Errorf("try %d (%s): status %d, fails %q, rests %+v; want 403 restricted", i, tr.ID, tr.Status, tr.Fail, tr.Rest)
				}
			}
		})
	}
}
