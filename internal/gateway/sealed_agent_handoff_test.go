package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const sealedHandoff = `{"type":"agent_message","author":"/root","recipient":"/root/worker","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"gAAAAATest_ciphertext=="}]}`

func TestCodexSealedAgentHandoffGuidesBeforeRouting(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			f := &fake{t: t}
			setup(t, provider.Responses, f)
			var nativeCalls int
			chatgpt(t, func(w http.ResponseWriter, r *http.Request) { nativeCalls++ })
			body := `{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Start"}]},` + sealedHandoff + `,{"type":"compaction_trigger"}]}`
			req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(string(encodedBody(t, []byte(body), encoding))))
			req.Header.Set("Content-Encoding", encoding)
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, req)
			var response struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 400 || response.Error.Type != "invalid_request_error" ||
				!strings.Contains(response.Error.Message, "OpenAI lead") ||
				!strings.Contains(response.Error.Message, "Magpie-served model for the lead") ||
				!strings.Contains(response.Error.Message, "OpenAI subagent") {
				t.Fatalf("guidance: status=%d response=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "gAAAAA") || f.calls != 0 || nativeCalls != 0 {
				t.Fatalf("ciphertext leaked or request forwarded: fake=%d native=%d", f.calls, nativeCalls)
			}
		})
	}
}

func TestCodexSealedAgentHandoffDoesNotAffectOtherMessages(t *testing.T) {
	for _, tc := range []struct {
		name, input string
	}{
		{"plain agent task", `[{"type":"agent_message","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\nreply PONG"}]}]`},
		{"non-native encrypted part", `[{"type":"agent_message","content":[{"type":"input_text","text":"Payload:\n"},{"type":"encrypted_content","encrypted_content":"plain task"}]}]`},
		{"other encrypted item", `[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"encrypted_content","encrypted_content":"gAAAAATest_ciphertext=="}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)}
			setup(t, provider.Responses, f)
			body := `{"model":"fake/m1","stream":true,"input":` + tc.input + `}`
			s := New()
			code, response := postTo(t, s, CodexPath+"/responses", body)
			if code != 200 || f.calls != 1 || strings.Contains(response, "sealed subagent task") {
				t.Fatalf("normal request: status=%d upstream=%d response=%s", code, f.calls, response)
			}
			if tc.name == "plain agent task" && !strings.Contains(string(f.got), "reply PONG") {
				t.Fatal("plain task was not delivered")
			}
			for _, route := range s.Trace(context.Background(), 0, 0).Routes {
				if route.SealedTask || route.LeadAccount != "" {
					t.Fatalf("ordinary request marked as sealed: %+v", route)
				}
			}
		})
	}
}

func TestCodexSealedAgentHandoffNativePassthrough(t *testing.T) {
	var got []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
	})
	body := `{"model":"gpt-5.5","stream":true,"input":[` + sealedHandoff + `]}`
	code, response := codexPost(t, body)
	if code != 200 || !strings.Contains(string(got), "gAAAAATest_ciphertext==") || strings.Contains(response, "sealed subagent task") {
		t.Fatalf("native request: status=%d forwarded=%v response=%s", code, len(got) > 0, response)
	}
}

// A Codex subagent on a routing group whose lead a ChatGPT account of the
// group answered (Koohoko, #619): the task the lead's spawn_agent sealed
// goes to the group's ChatGPT account that answered the lead, not to the
// member routing puts first, which can't read it, nor is it turned away.
func TestCodexSealedAgentHandoffGoesToGroupsChatGPTAccount(t *testing.T) {
	var tried []string
	var got []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"on it"}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
	})
	codexSignedIn(t, "spare@example.com")
	sticks.Lock()
	sticks.m = map[string]stick{}
	sticks.Unlock()
	grok := &scripted{replies: []reply{{200, "text/event-stream", grokAnswer}}}
	scriptedOn(t, "xai", provider.Chat, grok)
	refusalGroup(t, "xai/m", "codex/gpt-5.5")
	s := New()
	ask := func(session, parent, pin, input string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"group/g","stream":true,"input":[`+input+`]}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		req.Header.Set("session_id", session)
		if parent != "" {
			req.Header.Set("x-codex-parent-thread-id", parent)
		}
		if pin != "" {
			req.Header.Set(AccountHeader, pin)
		}
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	// the lead, answered by the spare account
	lead := `{"type":"message","role":"user","content":[{"type":"input_text","text":"spawn a worker"}]}`
	if rec := ask("lead-1", "", "spare@example.com", lead); rec.Code != 200 || strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("lead: %d %s (tried %v)", rec.Code, rec.Body.String(), tried)
	}
	tried = nil
	rec := ask("worker-1", "lead-1", "", sealedHandoff)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "on it") {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if grok.n != 0 || strings.Join(tried, ",") != "acct-2" || !strings.Contains(string(got), "gAAAAATest_ciphertext==") {
		t.Fatalf("sealed task went to grok %d, accounts %v, forwarded sealed=%v", grok.n, tried, strings.Contains(string(got), "gAAAAATest_ciphertext=="))
	}
	for _, route := range s.Trace(context.Background(), 0, 0).Routes {
		if route.Session != "worker-1" {
			continue
		}
		if !route.SealedTask || route.LeadAccount == "" || route.LeadAccount != route.Order[0].ID {
			t.Fatalf("sealed task's routing reason missing: %+v", route)
		}
		for _, account := range route.Order {
			if account.Agent != "codex" {
				t.Fatalf("unreadable account in sealed task's order: %+v", account)
			}
		}
		b, err := json.Marshal(route)
		if err != nil || strings.Contains(string(b), "gAAAAA") {
			t.Fatalf("sealed task leaked into trace: %s, %v", b, err)
		}
		return
	}
	t.Fatal("sealed subagent trace missing")
}
