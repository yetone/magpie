package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// What api.deepseek.com answered on 2026-11-02, byte for byte, to a tool
// loop continued with a tool turn that has no thinking: on its Messages
// API and on its Chat API.
const (
	deepseekThinkingBackLive  = `{"error":{"message":"The ` + "`content[].thinking`" + ` in the thinking mode must be passed back to the API. (request_id: cae816b6-d668-4a55-935d-99d1b8ad3686)","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`
	deepseekReasoningBackLive = `{"error":{"message":"The ` + "`reasoning_content`" + ` in the thinking mode must be passed back to the API. (request_id: 6360375a-b044-4a13-b51e-548de94677ea)","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`
	// qisishiqi's error on Discord (#help, 2026-11-02), as magpie passed it on
	qisishiqiError = `{"code":null,"message":"My Claude: The ` + "`content[].thinking`" + ` in the thinking mode must be passed back to the API.","param":null,"type":"invalid_request_error"}`
)

// thinkingBackStream is the Messages stream the fakes answer with.
var thinkingBackStream = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`,
	`data: {"type":"content_block_stop","index":0}`,
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
	`data: {"type":"message_stop"}`)

// codexMidLoop is Codex continuing a tool loop its own model began: the
// reasoning it has for the call is OpenAI's, sealed, with no text.
const codexMidLoop = `{"model":"%s","stream":false,"reasoning":{"effort":"high"},
	"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}],
	"input":[
	{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},
	{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAA-codex-sealed"},
	{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"ls\"}"},
	{"type":"function_call_output","call_id":"call_1","output":"main.go"}]}`

func TestThinkingBackRefusalRead(t *testing.T) {
	for _, s := range []string{deepseekThinkingBackLive, deepseekReasoningBackLive, qisishiqiError,
		`{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API."}}`} {
		if !thinkingBackRefusal.Match(wordsOf([]byte(s))) {
			t.Errorf("not read as asking for thinking back: %s", s)
		}
	}
	for _, s := range []string{
		"The `reasoning_text` in the thinking mode must be passed back", // DeepSeek's Responses, its own path
		"messages.1.content.0: Invalid signature in thinking block",
		"prompt is too long",
	} {
		if thinkingBackRefusal.MatchString(s) {
			t.Errorf("%q read as asking for thinking back", s)
		}
	}
}

// qisishiqi (Discord): a routing group fell from a rate-limited Codex
// subscription to "My Claude", an Anthropic endpoint whose host and
// model don't say DeepSeek, mid tool loop. DeepSeek's model behind it
// asked for the call's thinking, which Codex's sealed reasoning can't
// give. magpie asks once more with a space for it, and from then on
// sends it at once; a provider that never asked is sent none.
func TestThinkingBackLearnedBehindAnotherName(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-6", "relay-model"} {
		t.Run(model, func(t *testing.T) {
			plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
			up := setup(t, provider.Responses, plan)
			mine := &fake{t: t, reply: thinkingBackStream, refuse: func(b []byte) (int, string) {
				if code, _ := deepseekRefuseAnthropic(b); code == 400 {
					return 400, deepseekThinkingBackLive
				}
				return 0, ""
			}}
			mineSrv := httptest.NewServer(mine)
			t.Cleanup(mineSrv.Close)
			plain := &fake{t: t, reply: thinkingBackStream}
			plainSrv := httptest.NewServer(plain)
			t.Cleanup(plainSrv.Close)
			for _, p := range []provider.Provider{
				{ID: "plan", Name: "Plan", Key: "k", Models: []string{"gpt-5.5"}, Responses: up.URL + "/v1"},
				{ID: "myclaude", Name: "My Claude", Key: "k", Models: []string{model}, Anthropic: mineSrv.URL},
				{ID: "plain", Name: "Plain", Key: "k", Models: []string{model}, Anthropic: plainSrv.URL},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{ID: "cc", Name: "cc", Members: []string{"plan/gpt-5.5", "myclaude/" + model}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			srv := New()
			req := strings.Replace(codexMidLoop, "%s", "group/cc", 1)
			code, body := postTo(t, srv, "/v1/responses", req)
			if code != 200 || mine.calls != 2 {
				t.Fatalf("%d after %d calls to My Claude, want 200 on the second: %s", code, mine.calls, body)
			}
			turn := gjson.GetBytes(mine.got, `messages.#(role=="assistant")`)
			if th := turn.Get(`content.#(type=="thinking")`); th.Get("thinking").String() != noReasoning || turn.Get("content.0.type").String() != "thinking" {
				t.Errorf("the tool turn went as %s, want a thinking block first", turn.Raw)
			}

			// learned: the next request is taken the first time
			mine.calls = 0
			if code, body = postTo(t, srv, "/v1/responses", req); code != 200 || mine.calls != 1 {
				t.Fatalf("%d after %d calls, want 200 on the first: %s", code, mine.calls, body)
			}

			// a provider that never asked gets the turn as before
			if code, body = postTo(t, srv, "/v1/responses", strings.Replace(codexMidLoop, "%s", "plain/"+model, 1)); code != 200 {
				t.Fatalf("plain: %d %s", code, body)
			}
			if strings.Contains(string(plain.got), `"thinking":" "`) {
				t.Errorf("a provider that never asked was given thinking: %s", plain.got)
			}
		})
	}
}

// The sibling on Chat: DeepSeek's model behind a Chat provider whose host
// and model don't say so, given Codex's loop, asks for reasoning_content
// back; magpie asks once more with it, and from then on.
func TestReasoningBackLearnedBehindAnotherName(t *testing.T) {
	f := &fake{t: t, reply: chatStreamOK, refuse: func(b []byte) (int, string) {
		if code, _ := deepseekRefuseToolTurns(b); code == 400 {
			return 400, deepseekReasoningBackLive
		}
		return 0, ""
	}}
	up := setup(t, provider.Chat, f)
	chatProvider(t, up.URL, provider.Provider{ID: "relay", Models: []string{"relay-model"}})
	srv := New()
	req := strings.Replace(codexMidLoop, "%s", "relay/relay-model", 1)
	code, body := postTo(t, srv, "/v1/responses", req)
	if code != 200 || f.calls != 2 {
		t.Fatalf("%d after %d calls, want 200 on the second: %s", code, f.calls, body)
	}
	if rc := gjson.GetBytes(f.got, `messages.#(role=="assistant").reasoning_content`); !rc.Exists() || rc.String() != noReasoning {
		t.Errorf("the tool turn's reasoning_content = %s, want a space: %s", rc.Raw, f.got)
	}
	f.calls = 0
	if code, body = postTo(t, srv, "/v1/responses", req); code != 200 || f.calls != 1 {
		t.Fatalf("%d after %d calls, want 200 on the first: %s", code, f.calls, body)
	}
}
