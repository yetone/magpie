package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

func TestFromLiteRemovesOnlyTheWebsocketLiteMarker(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "metadata with other fields",
			body: `{"client_metadata":{"session_id":"s","ws_request_header_x_openai_internal_codex_responses_lite":"true","x-codex-turn-metadata":"{\"thread_source\":\"thread_title\"}"},"reasoning":{"effort":"low","context":"all_turns"},"input":[]}`,
			want: `{"client_metadata":{"session_id":"s","x-codex-turn-metadata":"{\"thread_source\":\"thread_title\"}"},"reasoning":{"effort":"low"},"input":[]}`,
		},
		{
			name: "boolean marker",
			body: `{"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":true,"session_id":"s"},"input":[]}`,
			want: `{"client_metadata":{"session_id":"s"},"input":[]}`,
		},
		{
			name: "false marker",
			body: `{"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":false},"input":[]}`,
			want: `{"client_metadata":{},"input":[]}`,
		},
		{
			name: "without marker",
			body: `{"client_metadata":{"session_id":"s"},"input":[]}`,
			want: `{"client_metadata":{"session_id":"s"},"input":[]}`,
		},
		{
			name: "non object metadata",
			body: `{"client_metadata":"opaque","input":[]}`,
			want: `{"client_metadata":"opaque","input":[]}`,
		},
		{
			name: "malformed body",
			body: `{"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":true}`,
			want: `{"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":true}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(fromLite([]byte(test.body))); got != test.want {
				t.Fatalf("fromLite:\n got %s\nwant %s", got, test.want)
			}
		})
	}
}

func TestCodexTitleRelayKeepsLiteShapeConsistent(t *testing.T) {
	for _, asSent := range []bool{false, true} {
		name := "default"
		if asSent {
			name = "as sent"
		}
		t.Run(name, func(t *testing.T) {
			upstream := &fake{t: t}
			upstream.refuse = func([]byte) (int, string) {
				return http.StatusBadRequest, `{"error":{"message":"diagnostic upstream rejection"}}`
			}
			setup(t, provider.Responses, upstream)
			relay, err := provider.Find("fake")
			if err != nil {
				t.Fatal(err)
			}
			relay.CodexAsSent = asSent
			if err := provider.Save(*relay); err != nil {
				t.Fatal(err)
			}

			body := strings.Replace(liteRequest, `"model":"fake/m1",`,
				`"model":"fake/m1","reasoning":{"effort":"low","context":"all_turns"},"client_metadata":{"session_id":"title-session","ws_request_header_x_openai_internal_codex_responses_lite":"true","x-codex-turn-metadata":"{\"thread_source\":\"thread_title\"}"},`, 1)
			req := liteSent(body)
			req.URL.Path = CodexPath + "/responses"
			req.RequestURI = ""
			req.RemoteAddr = "127.0.0.1:51000"
			req.Header.Set("session_id", "title-session")
			req.Header.Set("x-codex-turn-metadata", `{"thread_source":"thread_title"}`)
			recorder := httptest.NewRecorder()
			New().Handler().ServeHTTP(recorder, req)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("upstream diagnostic status = %d, want 400: %s", recorder.Code, recorder.Body)
			}

			var got struct {
				Reasoning      map[string]any   `json:"reasoning"`
				ClientMetadata map[string]any   `json:"client_metadata"`
				Input          []map[string]any `json:"input"`
			}
			if err := json.Unmarshal(upstream.got, &got); err != nil {
				t.Fatalf("decode upstream request: %v", err)
			}
			if got.ClientMetadata["session_id"] != "title-session" ||
				got.ClientMetadata["x-codex-turn-metadata"] != `{"thread_source":"thread_title"}` {
				t.Fatalf("unrelated client metadata changed: %v", got.ClientMetadata)
			}
			_, hasContext := got.Reasoning["context"]
			_, hasMarker := got.ClientMetadata["ws_request_header_x_openai_internal_codex_responses_lite"]
			hasHeader := upstream.head.Get(liteHeader) != ""
			hasAdditionalTools := len(got.Input) > 0 && got.Input[0]["type"] == "additional_tools"
			if hasContext != asSent || hasMarker != asSent || hasHeader != asSent || hasAdditionalTools != asSent {
				t.Fatalf("asSent=%t: context=%t marker=%t header=%t additional_tools=%t; body=%s",
					asSent, hasContext, hasMarker, hasHeader, hasAdditionalTools, upstream.got)
			}
		})
	}
}

// Codex over its socket says it is Lite in client_metadata, not a header
// (codex-rs build_ws_client_metadata), and the socket's turn carried the
// marker on to a relay after its reasoning context was taken off: a CPA in
// front of the ChatGPT backend read it as Lite and was refused
// "X-OpenAI-Internal-Codex-Responses-Lite requires `reasoning.context` to be
// `all_turns`" (#4853, ChangQiu on Discord). The turn reaches the relay
// with none of the three.
func TestCodexSocketLiteTurnLeavesLiteForARelay(t *testing.T) {
	fresh(t)
	u := &respUp{}
	sockUp(t, u)
	_, ts := sockServer(t)
	c, _, err := dialSock(t, ts, CodexPath+"/responses", http.Header{"Openai-Beta": {"responses_websockets=2026-02-06"}})
	if err != nil {
		t.Fatal(err)
	}
	got := c.turn(create("", userHi,
		`"reasoning":{"effort":"low","context":"all_turns"}`,
		`"client_metadata":{"session_id":"s","ws_request_header_x_openai_internal_codex_responses_lite":"true"}`))
	if last(got).Get("type").String() != "response.completed" {
		t.Fatalf("%v", got)
	}
	if u.calls() != 1 {
		t.Fatalf("%d calls", u.calls())
	}
	body := u.body(0)
	u.mu.Lock()
	head := u.head[0]
	u.mu.Unlock()
	if gjson.GetBytes(body, "reasoning.context").Exists() ||
		gjson.GetBytes(body, "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite").Exists() ||
		head.Get(liteHeader) != "" {
		t.Errorf("still Lite: header %q, body %s", head.Get(liteHeader), body)
	}
	if gjson.GetBytes(body, "reasoning.effort").String() != "low" || gjson.GetBytes(body, "client_metadata.session_id").String() != "s" {
		t.Errorf("the rest changed: %s", body)
	}
}
