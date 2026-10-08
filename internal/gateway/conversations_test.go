package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
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
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, r)
	id := w.Header().Get(SessionHeader)
	tr, err := sessions.GatewayTranscript("codex", id)
	if w.Code != 200 || err != nil || tr.Captured != 1 || len(tr.Parts) != 2 || tr.Parts[1].Text != "direct answer" {
		t.Fatalf("direct relay missing from Sessions: status=%d id=%q transcript=%+v error=%v", w.Code, id, tr, err)
	}
	if _, ok := usage.GatewaySessionByID("codex", id, nil); !ok {
		t.Fatal("direct relay missing from session ledger")
	}
}

func TestGatewayConversationIdentityKeepsRoutingContext(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	input := []byte(`{"messages":[{"role":"user","content":"same prompt"}]}`)
	before := convOf(r.Header, input)
	r = withGatewaySession(httptest.NewRecorder(), r)
	if r.Header.Get(SessionHeader) != "" || convOf(r.Header, input) != before {
		t.Fatal("automatic ledger identity changed routing conversation")
	}
	forward := httptest.NewRequest("POST", "/", nil)
	passOnCaller(r.Context(), forward)
	if forward.Header.Get(SessionHeader) != gatewaySessionOf(r) {
		t.Fatal("automatic identity dropped on remote Magpie")
	}
	r.Header.Set("session_id", "native")
	r.Header.Set(SessionHeader, "override")
	if gatewaySessionOf(r) != "override" || nativeSessionOf(r.Header) != "native" {
		t.Fatal("client identities lost precedence")
	}
}

func TestGatewayConversationAutomaticRequestSession(t *testing.T) {
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
	first := post("", "").Header().Get(SessionHeader)
	second := post("", "").Header().Get(SessionHeader)
	if first == "" || second == "" || first == second {
		t.Fatalf("unidentified calls need separate sessions: %q %q", first, second)
	}
	if _, ok := usage.GatewaySessionByID("review-client", first, nil); !ok {
		t.Fatal("automatic session missing from gateway ledger")
	}
	tr, err := sessions.GatewayTranscript("review-client", first)
	if err != nil || tr.Captured != 0 {
		t.Fatalf("content recorded without consent: %+v %v", tr, err)
	}
	if err := sessions.SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := post(SessionHeader, first).Header().Get(SessionHeader); got != first {
			t.Fatalf("session continuity lost: %q", got)
		}
	}
	tr, err = sessions.GatewayTranscript("review-client", first)
	if err != nil || tr.Captured != 2 || len(tr.Parts) != 4 {
		t.Fatalf("reused session content missing: %+v %v", tr, err)
	}
	if got := post("session_id", "native-session").Header().Get(SessionHeader); got != "native-session" {
		t.Fatalf("native identity replaced: %q", got)
	}
	generated := post("", "").Header().Get(SessionHeader)
	tr, err = sessions.GatewayTranscript("review-client", generated)
	if err != nil || tr.Captured != 1 || len(tr.Parts) != 2 {
		t.Fatalf("automatic session transcript missing: %+v %v", tr, err)
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
