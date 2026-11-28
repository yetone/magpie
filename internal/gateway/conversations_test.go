package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

func TestGatewayConversationCapture(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	s := New()
	post := func(id string) {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"hello"}]}`))
		r.Header.Set("User-Agent", "claude-cli/2.1.0")
		r.Header.Set("X-Magpie-Session", id)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "done") {
			t.Fatalf("relay changed: %d %s", w.Code, w.Body)
		}
	}
	post("s")
	agent := usage.AgentOf("claude-cli/2.1.0")
	tr, err := sessions.GatewayTranscript(agent, "s")
	if err != nil || tr.Captured != 0 {
		t.Fatalf("default must be off: %+v %v", tr, err)
	}
	if err := sessions.SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	post("s")
	post("")
	tr, err = sessions.GatewayTranscript(agent, "s")
	if err != nil || tr.Captured != 1 || len(tr.Parts) != 2 || tr.Parts[0].Text != "hello" || tr.Parts[1].Text != "done" {
		t.Fatalf("captured: %+v %v", tr, err)
	}
}

func TestGatewayConversationCodexDirectRelay(t *testing.T) {
	fresh(t)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(SessionHeader) != "" {
			t.Error("gateway identity leaked to vendor")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"direct answer"}]}],"usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	if err := sessions.SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"direct question"}`))
	r.Header.Set("Authorization", "Bearer chatgpt-token")
	r.Header.Set("User-Agent", "codex/1")
	r.Header.Set("session_id", "codex-thread")
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, r)
	id := w.Header().Get(SessionHeader)
	tr, err := sessions.GatewayTranscript("codex", id)
	if w.Code != 200 || err != nil || tr.Captured != 1 || len(tr.Parts) != 2 || tr.Parts[1].Text != "direct answer" {
		t.Fatalf("direct relay missing from Sessions: status=%d id=%q transcript=%+v error=%v", w.Code, id, tr, err)
	}
	if id != "codex-thread" {
		t.Fatalf("recorded direct relay should expose Codex's session, got %q", id)
	}
}

// Text is recorded only under a session the client named, so every recorded
// conversation is one the Sessions page lists and can open. A request with no
// session is not recorded at all, and magpie makes up no id for it (#1355).
func TestGatewayConversationNeedsTheClientsSession(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	_, secrets := newCaller(t, "NAS client")
	server := lanGuard(New().Handler())
	post := func(header, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"hello"}]}`))
		r.RemoteAddr = "192.168.1.19:12345"
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		r.Header.Set("User-Agent", "review-client/1")
		if header != "" {
			r.Header.Set(header, id)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "done") {
			t.Fatalf("relay changed: %d %s", w.Code, w.Body)
		}
		return w
	}
	if err := sessions.SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	if got := post("", "").Header().Get(SessionHeader); got != "" {
		t.Fatalf("a request with no session was given %q", got)
	}
	if entries, err := os.ReadDir(filepath.Join(settings.Dir(), "gateway-conversations")); len(entries) != 0 {
		t.Fatalf("a request with no session was recorded where no Sessions row can open it: %v %v", entries, err)
	}
	if got := post("session_id", "native-session").Header().Get(SessionHeader); got != "native-session" {
		t.Fatalf("native identity replaced: %q", got)
	}
	for range 2 {
		if got := post(SessionHeader, "s1").Header().Get(SessionHeader); got != "s1" {
			t.Fatalf("session continuity lost: %q", got)
		}
	}
	tr, err := sessions.GatewayTranscript("review-client", "s1")
	if err != nil || tr.Captured != 2 || len(tr.Parts) != 4 {
		t.Fatalf("named session content missing: %+v %v", tr, err)
	}
	if _, ok := usage.GatewaySessionByID("review-client", "s1", nil); !ok {
		t.Fatal("a recorded session has no Sessions row to open it from")
	}
}

// A session id is the client's own, whatever it starts with: one named
// "request-…" is listed, counted and readable like any other (#1355).
func TestGatewayConversationClientRequestPrefixedSession(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	if err := sessions.SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	const id = "request-7f3a"
	s := New()
	for range 2 {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"hello"}]}`))
		r.Header.Set("User-Agent", "review-client/1")
		r.Header.Set(SessionHeader, id)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("relay changed: %d %s", w.Code, w.Body)
		}
	}
	calls := 0
	usage.Visit(time.Time{}, func(r usage.Record) {
		if r.Session == id {
			calls++
		}
	})
	if calls != 2 {
		t.Fatalf("the ledger kept %d of 2 calls under the client's session %q", calls, id)
	}
	g, ok := usage.GatewaySessionByID("review-client", id, nil)
	if !ok || g.ID != id {
		t.Fatalf("the client's session %q is missing from Sessions: %+v", id, g)
	}
	if tr, err := sessions.GatewayTranscript("review-client", id); err != nil || tr.Captured != 2 {
		t.Fatalf("the client's session text is missing: %+v %v", tr, err)
	}
}

func TestGatewayConversationProtocols(t *testing.T) {
	for _, tc := range []struct {
		name                string
		proto               provider.Protocol
		input, output, want string
	}{
		{"chat", provider.Chat, `{"messages":[{"role":"system","content":"rules"},{"role":"user","content":"hello"}]}`, `{"choices":[{"message":{"role":"assistant","content":"answer"}}]}`, "answer"},
		{"anthropic", provider.Anthropic, `{"messages":[{"role":"user","content":"hello"}]}`, `{"content":[{"type":"text","text":"answer"}]}`, "answer"},
		{"responses", provider.Responses, `{"input":"hello"}`, `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`, "answer"},
		{"chat-stream", provider.Chat, `{"messages":[]}`, "data: {\"choices\":[{\"delta\":{\"content\":\"ans\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"wer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "answer"},
		{"anthropic-tool", provider.Anthropic, `{"messages":[]}`, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"bash\",\"input\":{}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n", "pwd"},
		{"responses-custom", provider.Responses, `{"input":[]}`, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"custom_tool_call\",\"call_id\":\"t\",\"name\":\"apply_patch\",\"input\":\"patch body\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", "patch body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := conversationTurn(tc.proto, []byte(tc.input), []byte(tc.output))
			b, _ := json.Marshal(turn.Output)
			if turn.Cut || !strings.Contains(string(b), tc.want) {
				t.Fatalf("parsed: %+v", turn)
			}
		})
	}
}

func TestGatewayConversationScrubsAndMarksIncomplete(t *testing.T) {
	turn := conversationTurn(provider.Chat, []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"t","type":"function","function":{"name":"login","arguments":"{\"password\":\"private-value\"}"}}]}]}`), []byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	b, _ := json.Marshal(turn)
	if !turn.Cut || strings.Contains(string(b), "private-value") || !strings.Contains(string(b), "partial") {
		t.Fatalf("scrub or incomplete failed: %s", b)
	}
}

func TestConversationTurnKeepsTheUsersRules(t *testing.T) {
	const key = "rz_TurnKey6677889900"
	input := []byte(`{"messages":[{"role":"user","content":"send it to ` + key + `"}]}`)
	output := []byte(`{"choices":[{"message":{"role":"assistant","content":"sent to ` + key + `"}}]}`)
	o := redact.Options{Secrets: true, Rules: []redact.Rule{{Kind: "RELAY", Prefix: "rz_"}}}
	b, _ := json.Marshal(conversationTurnWith(provider.Chat, input, output, o))
	if strings.Contains(string(b), key) || !strings.Contains(string(b), "[REDACTED:RELAY]") {
		t.Fatalf("turn with masking on: %s", b)
	}
	// A rule is the user's own masking, not a secret magpie always knows:
	// the plain reader keeps what the rule would match.
	if b, _ := json.Marshal(conversationTurn(provider.Chat, input, output)); !strings.Contains(string(b), key) {
		t.Fatalf("plain turn took the rule's match out: %s", b)
	}
}
