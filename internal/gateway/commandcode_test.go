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

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// cmdUpstream stands in for api.commandcode.ai: /alpha/generate answers
// with reply, the Provider API with a plain Anthropic message. It records
// every request.
type cmdUpstream struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	body  []byte
	head  http.Header
}

func (u *cmdUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.paths...)
}

func newCmdUpstream(t *testing.T, reply func(w http.ResponseWriter)) *cmdUpstream {
	t.Helper()
	u := &cmdUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.paths = append(u.paths, r.URL.Path)
		if r.URL.Path == "/alpha/generate" {
			u.body, u.head = b, r.Header.Clone()
		}
		u.mu.Unlock()
		switch r.URL.Path {
		case "/alpha/generate":
			reply(w)
		case "/provider/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_p","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"from the Provider API"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(u.srv.Close)
	old := cmdGenerate
	// the account named "go" is on Go; any other is asked as provider says
	cmdGenerate = func(ctx context.Context, p provider.Provider) (string, string, bool) {
		if p.Account != nil && p.Account.User == "go" {
			return u.srv.URL, "go-key", true
		}
		return provider.CommandCodeGenerate(ctx, p)
	}
	t.Cleanup(func() { cmdGenerate = old })
	return u
}

func cmdLines(lines ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		// as the server answers: no event-stream, a JSON object a line
		w.WriteHeader(200)
		for _, l := range lines {
			io.WriteString(w, l+"\n")
		}
	}
}

func cmdAccount(u *cmdUpstream, user string) provider.Provider {
	return provider.Provider{ID: provider.CommandCodePlanID, Name: "Command Code Plan",
		Chat: u.srv.URL + "/provider/v1", Responses: u.srv.URL + "/provider/v1", Anthropic: u.srv.URL + "/provider",
		Account: &provider.Account{Agent: provider.CommandCodePlanID, User: user}}
}

func cmdAttempt(t *testing.T, p provider.Provider, from provider.Protocol, model, body string) (int, string, string, Call) {
	t.Helper()
	s := New()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	var call Call
	status, msg := s.attempt(w, r, from, p, model, []byte(body), &call)
	return status, msg, w.Body.String(), call
}

const cmdFinish = `{"type":"finish","finishReason":"stop","rawFinishReason":"end_turn","totalUsage":{"inputTokens":120,"outputTokens":7,"inputTokenDetails":{"noCacheTokens":20,"cacheReadTokens":90,"cacheWriteTokens":10}}}`

// A Go account is asked at /alpha/generate; any other Command Code
// account on the Provider API, as before.
func TestCommandCodeGoRouting(t *testing.T) {
	u := newCmdUpstream(t, cmdLines(`{"type":"text-delta","text":"from generate"}`, cmdFinish))
	body := `{"model":"deepseek/deepseek-v4-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`

	status, msg, out, call := cmdAttempt(t, cmdAccount(u, "go"), provider.Anthropic, "deepseek/deepseek-v4-pro", body)
	if status != 200 || !strings.Contains(out, "from generate") || call.To != provider.Anthropic {
		t.Fatalf("go: %d %q %s", status, msg, out)
	}
	if call.Usage.Input != 20 || call.Usage.CacheRead != 90 || call.Usage.CacheWrite != 10 || call.Usage.Output != 7 {
		t.Fatalf("go usage: %+v", call.Usage)
	}
	status, msg, out, _ = cmdAttempt(t, cmdAccount(u, "pro"), provider.Anthropic, "claude-sonnet-5", body)
	if status != 200 || !strings.Contains(out, "from the Provider API") {
		t.Fatalf("pro: %d %q %s", status, msg, out)
	}
	if got := strings.Join(u.seen(), " "); got != "/alpha/generate /provider/v1/messages" {
		t.Fatalf("asked: %s", got)
	}
}

// The request is the CLI's: its headers, its config, the conversation in
// the AI SDK's parts, the tools with their schemas.
func TestCommandCodeGoRequest(t *testing.T) {
	u := newCmdUpstream(t, cmdLines(`{"type":"text-delta","text":"ok"}`, cmdFinish))
	body := `{"model":"moonshotai/Kimi-K3","max_tokens":2048,"temperature":0.5,"stream":true,
	 "thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},
	 "system":[{"type":"text","text":"You are helpful."}],
	 "tools":[{"name":"read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],
	 "messages":[
	  {"role":"user","content":[{"type":"text","text":"read a.txt"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0K"}}]},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"I should read it","signature":""},{"type":"text","text":"Reading."},{"type":"tool_use","id":"call_1","name":"read","input":{"path":"a.txt"}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"hello"},{"type":"text","text":"and now?"}]}]}`
	status, msg, _, _ := cmdAttempt(t, cmdAccount(u, "go"), provider.Anthropic, "moonshotai/Kimi-K3", body)
	if status != 200 {
		t.Fatalf("%d %q", status, msg)
	}
	h := u.head
	for k, want := range map[string]string{
		"Authorization": "Bearer go-key", "User-Agent": "cli", "Content-Type": "application/json",
		"X-Command-Code-Version": cmdCLIVersion, "X-Cli-Environment": "production", "X-Project-Slug": "magpie",
		"X-Taste-Learning": "false",
	} {
		if h.Get(k) != want {
			t.Errorf("%s: %q, want %q", k, h.Get(k), want)
		}
	}
	if h.Get("X-Session-Id") == "" || h.Get("X-Api-Key") != "" {
		t.Errorf("session %q, x-api-key %q", h.Get("X-Session-Id"), h.Get("X-Api-Key"))
	}
	b := gjson.ParseBytes(u.body)
	for path, want := range map[string]string{
		"permissionMode": "standard", "memory": "", "config.isGitRepo": "false", "config.environment": cmdPlatform(),
		"params.model": "moonshotai/Kimi-K3", "params.stream": "true", "params.max_tokens": "2048", "params.temperature": "0.5",
		"params.system":                            "You are helpful.",
		"params.tools.0.name":                      "read",
		"params.tools.0.input_schema.required.0":   "path",
		"params.messages.0.role":                   "user",
		"params.messages.0.content.0.type":         "text",
		"params.messages.0.content.1.type":         "image",
		"params.messages.0.content.1.image":        "data:image/png;base64,iVBORw0K",
		"params.messages.0.content.1.mimeType":     "image/png",
		"params.messages.1.role":                   "assistant",
		"params.messages.1.content.0.type":         "reasoning",
		"params.messages.1.content.0.text":         "I should read it",
		"params.messages.1.content.1.text":         "Reading.",
		"params.messages.1.content.2.type":         "tool-call",
		"params.messages.1.content.2.toolCallId":   "call_1",
		"params.messages.1.content.2.toolName":     "read",
		"params.messages.1.content.2.input.path":   "a.txt",
		"params.messages.2.role":                   "tool",
		"params.messages.2.content.0.type":         "tool-result",
		"params.messages.2.content.0.toolCallId":   "call_1",
		"params.messages.2.content.0.toolName":     "read",
		"params.messages.2.content.0.output.type":  "text",
		"params.messages.2.content.0.output.value": "hello",
		"params.messages.3.role":                   "user",
		"params.messages.3.content.0.text":         "and now?",
	} {
		if got := b.Get(path).String(); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	if !b.Get("memory").Exists() || b.Get("memory").Type != gjson.Null || !b.Get("config.date").Exists() ||
		!b.Get("params.reasoning_effort").Exists() || b.Get("params.messages.#").Int() != 4 {
		t.Errorf("body: %s", u.body)
	}
	if e := b.Get("params.reasoning_effort").String(); e != "medium" {
		t.Errorf("effort %q", e)
	}
}

// The reply's lines come back in the client's own API, streamed or not:
// text, reasoning, a tool call with its input, why it stopped and the
// usage.
func TestCommandCodeGoReply(t *testing.T) {
	lines := cmdLines(
		`{"type":"start"}`,
		`{"type":"reasoning-start","id":"r"}`,
		`{"type":"reasoning-delta","id":"r","text":"thinking it over"}`,
		`{"type":"reasoning-end","id":"r"}`,
		`{"type":"text-delta","id":"t","text":"Let me look."}`,
		`not json, skipped as the CLI skips it`,
		`{"type":"tool-call","toolCallId":"call_9","toolName":"read","input":"{\"path\":\"b.go\"}"}`,
		`{"type":"finish","finishReason":"tool-calls","rawFinishReason":"tool_use","totalUsage":{"inputTokens":50,"outputTokens":12,"inputTokenDetails":{"cacheReadTokens":30,"cacheWriteTokens":0},"outputTokenDetails":{"reasoningTokens":4}}}`,
	)
	u := newCmdUpstream(t, lines)
	p := cmdAccount(u, "go")
	msgs := `"messages":[{"role":"user","content":"look at b.go"}]`
	tools := `{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}`

	t.Run("anthropic stream", func(t *testing.T) {
		status, _, out, call := cmdAttempt(t, p, provider.Anthropic, "zai-org/GLM-5.3",
			`{"model":"zai-org/GLM-5.3","max_tokens":100,"stream":true,`+msgs+`,"tools":[{"name":"read","input_schema":{"type":"object"}}]}`)
		if status != 200 || !strings.Contains(out, "event: message_start") || !strings.Contains(out, `"thinking":"thinking it over"`) ||
			!strings.Contains(out, `"text":"Let me look."`) || !strings.Contains(out, `"id":"call_9","input":{},"name":"read","type":"tool_use"`) ||
			!strings.Contains(out, `\"path\":\"b.go\"`) || !strings.Contains(out, `"stop_reason":"tool_use"`) {
			t.Fatalf("%d %s", status, out)
		}
		if call.Usage.Input != 20 || call.Usage.CacheRead != 30 || call.Usage.Output != 12 {
			t.Fatalf("usage %+v", call.Usage)
		}
	})
	t.Run("chat", func(t *testing.T) {
		status, _, out, _ := cmdAttempt(t, p, provider.Chat, "zai-org/GLM-5.3", `{"model":"zai-org/GLM-5.3",`+msgs+`,"tools":[`+tools+`]}`)
		r := gjson.Parse(out)
		if status != 200 || r.Get("choices.0.message.content").String() != "Let me look." ||
			r.Get("choices.0.message.tool_calls.0.id").String() != "call_9" ||
			r.Get("choices.0.message.tool_calls.0.function.name").String() != "read" ||
			gjson.Get(r.Get("choices.0.message.tool_calls.0.function.arguments").String(), "path").String() != "b.go" ||
			r.Get("choices.0.finish_reason").String() != "tool_calls" || r.Get("usage.prompt_tokens").Int() != 50 ||
			r.Get("usage.completion_tokens").Int() != 12 {
			t.Fatalf("%d %s", status, out)
		}
	})
	t.Run("responses stream", func(t *testing.T) {
		status, _, out, _ := cmdAttempt(t, p, provider.Responses, "zai-org/GLM-5.3",
			`{"model":"zai-org/GLM-5.3","stream":true,"input":"look at b.go","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`)
		if status != 200 || !strings.Contains(out, "response.output_text.delta") || !strings.Contains(out, `"call_id":"call_9"`) ||
			!strings.Contains(out, "response.completed") {
			t.Fatalf("%d %s", status, out)
		}
	})
	t.Run("responses", func(t *testing.T) {
		status, _, out, _ := cmdAttempt(t, p, provider.Responses, "zai-org/GLM-5.3",
			`{"model":"zai-org/GLM-5.3","input":"look at b.go","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`)
		r := gjson.Parse(out)
		call := r.Get(`output.#(type=="function_call")`)
		if status != 200 || call.Get("name").String() != "read" || call.Get("call_id").String() != "call_9" ||
			gjson.Get(call.Get("arguments").String(), "path").String() != "b.go" {
			t.Fatalf("%d %s", status, out)
		}
	})
}

// Command Code's failures keep what they mean: a model not in the plan is
// a 403 in the CLI's words, which another account may serve; an error
// line before the answer a status too; a reply with no finish an error.
func TestCommandCodeGoFailures(t *testing.T) {
	body := `{"model":"claude-opus-5-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	for _, c := range []struct {
		name   string
		reply  func(w http.ResponseWriter)
		status int
		msg    string
	}{
		{"not in plan", func(w http.ResponseWriter) {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"type":"forbidden","message":"MODEL_NOT_IN_PLAN: Claude Opus 5.5 needs Max or higher."}}`)
		}, 403, "Command Code: Model not in plan: Claude Opus 5.5 needs Max or higher."},
		{"not in plan, in the stream", cmdLines(`{"type":"error","error":{"message":"403 {\"error\":{\"type\":\"forbidden\",\"message\":\"MODEL_NOT_IN_PLAN: needs Pro\"}}"}}`),
			403, "Command Code: Model not in plan: needs Pro"},
		{"credits", func(w http.ResponseWriter) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"type":"billing","message":"PREMIUM_CREDITS_EXHAUSTED: you have used this month's credits"}}`)
		}, 402, "Command Code: you have used this month's credits"},
		{"stream error", cmdLines(`{"type":"error","error":{"message":"upstream overloaded","statusCode":529,"isRetryable":true}}`),
			529, "Command Code: upstream overloaded"},
		{"cut short", cmdLines(`{"type":"text-delta","text":"half"}`), 502, "Command Code: the reply ended before it was complete"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newCmdUpstream(t, c.reply)
			status, msg, out, _ := cmdAttempt(t, cmdAccount(u, "go"), provider.Anthropic, "claude-opus-5-5", body)
			if c.name == "cut short" {
				// the answer had begun: the stream carries the error
				if !strings.Contains(out, "half") || !strings.Contains(out, "event: error") || msg != c.msg {
					t.Fatalf("%d %q %s", status, msg, out)
				}
				return
			}
			if status != c.status || msg != c.msg || !strings.Contains(out, c.msg) {
				t.Fatalf("%d %q %s", status, msg, out)
			}
		})
	}
}

// A reply is asked for within Command Code's 200000 tokens (Discord: every
// model failed, "Too big: expected number to be <=200000 at
// params.max_tokens"): an agent told a model gives more asks for more, and
// /alpha/generate refused the whole request. Less is asked as it was.
func TestCommandCodeGoMaxTokens(t *testing.T) {
	for asked, want := range map[int]string{256000: "200000", 2048: "2048"} {
		u := newCmdUpstream(t, cmdLines(`{"type":"text-delta","text":"ok"}`, cmdFinish))
		body := fmt.Sprintf(`{"model":"zz/unlisted-model","max_tokens":%d,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, asked)
		status, msg, _, _ := cmdAttempt(t, cmdAccount(u, "go"), provider.Anthropic, "zz/unlisted-model", body)
		if status != 200 {
			t.Fatalf("%d: %d %q", asked, status, msg)
		}
		if got := gjson.GetBytes(u.body, "params.max_tokens").String(); got != want {
			t.Errorf("max_tokens %d reached Command Code as %s, want %s", asked, got, want)
		}
	}
}
