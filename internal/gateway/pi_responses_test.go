package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// piResponses is a turn as Pi's openai-responses API sends it (pi-ai's
// openai-responses.js): its system prompt as a developer message, an image,
// the reasoning it was sent before with its encrypted content, a tool call
// and its output, its tools, a thinking level, and how long a reply may be.
const piResponses = `{"model":%q,"stream":true,"store":false,"prompt_cache_key":"pi-session-1",
  "input":[
    {"role":"developer","content":"You are Pi."},
    {"role":"user","content":[{"type":"input_text","text":"what is this?"},{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,iVBORw0KGgo="}]},
    {"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc-1"},
    {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Let me look.","annotations":[]}],"status":"completed","id":"msg_1"},
    {"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.png\"}"},
    {"type":"function_call_output","call_id":"call_1","output":"PNG"}],
  "tools":[{"type":"function","name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}}},"strict":false}],
  "reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"max_output_tokens":32000}`

var piReply = sse(
	`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
	`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"a cat"}`,
	`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":9,"output_tokens":2}}}`)

// checkPiRelayed: what Pi sent reached the upstream on its own API, and the
// upstream's stream came back to Pi.
func checkPiRelayed(t *testing.T, f *fake, code int, body string) map[string]any {
	t.Helper()
	if code != 200 || !strings.Contains(body, `"delta":"a cat"`) || !strings.Contains(body, "response.completed") {
		t.Fatalf("reply: %d %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %s", f.got)
	}
	if r, _ := up["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != "auto" {
		t.Errorf("reasoning: %v", up["reasoning"])
	}
	if tools, _ := up["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["name"] != "read" {
		t.Errorf("tools: %v", up["tools"])
	}
	s := string(f.got)
	for _, want := range []string{`"input_image"`, `data:image/png;base64,iVBORw0KGgo=`, `"encrypted_content":"enc-1"`,
		`"function_call"`, `"function_call_output"`, `"call_id":"call_1"`, `reasoning.encrypted_content`, `You are Pi.`} {
		if !strings.Contains(s, want) {
			t.Errorf("upstream lost %s: %s", want, s)
		}
	}
	if up["stream"] != true {
		t.Errorf("not streamed: %s", s)
	}
	return up
}

// A Responses-native provider gets Pi's Responses request relayed as it is.
func TestPiResponsesRelayed(t *testing.T) {
	f := &fake{t: t, reply: piReply}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1/responses", fmt.Sprintf(piResponses, "fake/m1"))
	up := checkPiRelayed(t, f, code, body)
	if f.path != "/v1/responses" || up["model"] != "m1" || up["max_output_tokens"] != float64(32000) || up["prompt_cache_key"] != "pi-session-1" {
		t.Errorf("relayed: %s %s", f.path, f.got)
	}
}

// A ChatGPT sign-in gets it relayed too, made into the request Codex CLI
// would send: Codex's instructions, Pi's first in the input, no cap on the
// reply (the backend rejects one).
func TestPiResponsesRelayedToChatGPT(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		claims(map[string]any{"email": "me@example.com"}), claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)
	f := &fake{t: t, ctype: "none", reply: piReply}
	up := httptest.NewServer(f)
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	code, body := post(t, "/v1/responses", fmt.Sprintf(piResponses, "codex/gpt-5.5"))
	got := checkPiRelayed(t, f, code, body)
	if f.path != "/backend-api/codex/responses" || got["model"] != "gpt-5.5" {
		t.Errorf("relayed: %s %s", f.path, f.got)
	}
	if _, ok := got["max_output_tokens"]; ok {
		t.Errorf("cap sent to the backend: %s", f.got)
	}
	if ins, _ := got["instructions"].(string); !strings.HasPrefix(ins, "You are Codex") {
		t.Errorf("instructions: %q", ins)
	}
}

// DeepSeek Harness on magpie's route switched to OpenAI Responses (its
// llm-pi-ai plugin is pi-ai's openai-responses, so it asks as Pi does)
// reaches a model served on Chat Completions alone, a DeepSeek one: the
// gateway asks it in Chat, the effort as reasoning_effort, the image, tool
// call and its output carried over, and dsh gets Responses events back.
func TestDshResponsesReachChatVendor(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"a cat"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(piResponses, "fake/m1")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer magpie")
	req.Header.Set("User-Agent", "deepseek-harness/0.2.0")
	New().Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "a cat") || !strings.Contains(body, "response.completed") {
		t.Fatalf("reply: %d %s", rec.Code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %s", f.got)
	}
	if f.path != "/v1/chat/completions" || up["model"] != "m1" || up["reasoning_effort"] != "high" || up["stream"] != true {
		t.Errorf("asked: %s %s", f.path, f.got)
	}
	s := string(f.got)
	for _, want := range []string{`You are Pi.`, `data:image/png;base64,iVBORw0KGgo=`, `"tool_calls"`, `"call_1"`, `"tool_call_id"`, `"name":"read"`} {
		if !strings.Contains(s, want) {
			t.Errorf("upstream lost %s: %s", want, s)
		}
	}
}
