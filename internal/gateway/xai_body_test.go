package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #3392's second half, as kongkongklng reported it after merging the
// custom-tool fix: through xAI's own key the first Codex turn went
// through and every one after it 400ed "Could not decode the compaction
// blob. Ensure it is unmodified from the compact response." (Codex hands
// the sealed reasoning item back with "content": null), and every Claude
// call 400ed "Invalid request content: Invalid message role." (Claude
// Code's system turn mid-conversation).
const (
	xaiCompactionRefusal = `{"code":"invalid-argument","error":"Could not decode the compaction blob. Ensure it is unmodified from the compact response."}`
	xaiUnknownCustom     = `Failed to deserialize the JSON body into the target type: tools[6].type: unknown variant ` + "`custom`" + `, expected one of ` + "`function`" + `, ` + "`web_search`" + `, ` + "`shell`" + `, ` + "`tool_search`"
	xaiInvalidRole       = `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid request content: Invalid message role."}}`
	xaiNullRequired      = `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid request content: Schema validation failed: [standard_violation] /required: null is not of type \"array\""}}`
)

// The reporter's second turn: Codex's replay of the sealed reasoning item
// its first turn got, null content and all, beside the answer and the next
// ask (the texts cut short).
const xaiSecondTurn = `{"model":"fake/grok-4.7","stream":true,"store":false,"include":["reasoning.encrypted_content"],
	"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}},
		{"type":"custom","name":"apply_patch","description":"Apply a patch.","format":{"type":"text"}}],
	"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"The user said hi."}],"content":null,"encrypted_content":"gAABblob"},
		{"type":"message","role":"assistant","id":"msg_1","content":[{"type":"output_text","text":"Hello!"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"and now?"}]}]}`

// atXAI points magpie's fake provider at api.x.ai and its client at up,
// as xAI's own API is gated on the host.
func atXAI(t *testing.T, up *httptest.Server) *Server {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"grok-4.7"}, Responses: "http://api.x.ai/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	return s
}

// xaiResponsesUpstream refuses what xAI's API does (#3392): a custom tool
// with its 422 serde wording, a reasoning item's null content with the
// compaction-blob 400.
func xaiResponsesUpstream(f *fake) {
	f.refuse = func(body []byte) (int, string) {
		var q struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
			Input []struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		json.Unmarshal(body, &q)
		for _, t := range q.Tools {
			if t.Type == "custom" {
				return 422, xaiUnknownCustom
			}
		}
		for _, it := range q.Input {
			if it.Type == "reasoning" && bytes.Equal(bytes.TrimSpace(it.Content), []byte("null")) {
				return 400, xaiCompactionRefusal
			}
		}
		return 0, ""
	}
}

// Codex's turns to xAI's own key, the reporter's session: the first is
// 422ed over its custom tool, translated on the same API; the second, the
// sealed reasoning item handed back with "content": null beside its
// encrypted_content, was 400ed "Could not decode the compaction blob" —
// the null goes, on the translated path the custom tool had put it on and
// on the relay alike.
func TestXAITakesCodexsSealedReasoningBack(t *testing.T) {
	for _, turn := range []string{xaiSecondTurn,
		// the relay alone: no custom tool to turn away, the replayed item
		// shaped where the request is passed through as sent
		strings.Replace(xaiSecondTurn, `,{"type":"custom","name":"apply_patch","description":"Apply a patch.","format":{"type":"text"}}`, "", 1)} {
		f := &fake{t: t, reply: sse(
			`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":9,"output_tokens":1}}}`)}
		xaiResponsesUpstream(f)
		up := setup(t, provider.Responses, f)
		s := atXAI(t, up)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(turn)))
		if rec.Code != 200 {
			t.Fatalf("%d %s\nxAI got %s", rec.Code, rec.Body, f.got)
		}
		if bytes.Contains(f.got, []byte(`"content":null`)) {
			t.Errorf("xAI got the null content: %s", f.got)
		}
		if !bytes.Contains(f.got, []byte(`"encrypted_content":"gAABblob"`)) || !bytes.Contains(f.got, []byte(`"rs_1"`)) {
			t.Errorf("sealed reasoning not replayed as it came: %s", f.got)
		}
	}
}

// The custom tool's 422 is learned from (custom_tool.go) and the turn
// answered translated, so the second turn above reaches xAI through the
// translation: both halves of the reporter's session in one.
func TestXAILearnsCustomToolsThenTakesTheReplay(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":9,"output_tokens":1}}}`)}
	xaiResponsesUpstream(f)
	up := setup(t, provider.Responses, f)
	s := atXAI(t, up)
	first := `{"model":"fake/grok-4.7","stream":true,"store":false,"include":["reasoning.encrypted_content"],
	"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}},
		{"type":"custom","name":"apply_patch","description":"Apply a patch.","format":{"type":"text"}}],
	"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(first)))
	if rec.Code != 200 {
		t.Fatalf("first: %d %s\nxAI got %s", rec.Code, rec.Body, f.got)
	}
	if bytes.Contains(f.got, []byte(`"type":"custom"`)) || !bytes.Contains(f.got, []byte(`"name":"apply_patch"`)) {
		t.Errorf("first answered translated, but xAI got %s", f.got)
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(xaiSecondTurn)))
	if rec.Code != 200 {
		t.Fatalf("second: %d %s\nxAI got %s", rec.Code, rec.Body, f.got)
	}
	if bytes.Contains(f.got, []byte(`"content":null`)) || !bytes.Contains(f.got, []byte(`"encrypted_content":"gAABblob"`)) {
		t.Errorf("second turn: xAI got %s", f.got)
	}
}

// Claude Code's messages carry a system turn mid-conversation (Anthropic
// made them GA); xAI's Messages answers the role "Invalid message role",
// so each folds into the top-level system, the only place it takes the
// role (#3392: every claude call 400ed).
func TestXAIFoldsClaudesSystemTurn(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"grok-4.7","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	f.refuse = func(body []byte) (int, string) {
		var q struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		json.Unmarshal(body, &q)
		for _, m := range q.Messages {
			if m.Role == "system" {
				return 400, xaiInvalidRole
			}
		}
		return 0, ""
	}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"grok-4.7"}, Anthropic: "http://api.x.ai"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	// the reporter's shape: the billing system prompt top-level, the
	// environment one a system turn after the user's hello (texts cut)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"fake/grok-4.7","stream":true,"max_tokens":128000,
	"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.292.c59;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],
	"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},
		{"role":"system","content":[{"type":"text","text":"# Environment\nYou are powered by the model xai/grok-4.7.","cache_control":{"type":"ephemeral"}}]}],
	"thinking":{"type":"adaptive","display":"updates"}}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s\nxAI got %s", rec.Code, rec.Body, f.got)
	}
	if strings.Contains(string(f.got), `"role":"system"`) {
		t.Errorf("system turn left in messages: %s", f.got)
	}
	for _, said := range []string{"# Environment", "You are Claude Code", `"role":"user"`, `"cache_control":{"type":"ephemeral"}`} {
		if !strings.Contains(string(f.got), said) {
			t.Errorf("xAI's system lost %q: %s", said, f.got)
		}
	}
}

// #3392's third round, as kongkongklng reported it after the system-turn
// fix: codex went through, every Claude call still 400ed xAI's schema
// validation "Invalid request content: Schema validation failed:
// [standard_violation] /required: null is not of type \"array\"" — a
// tool's schema said "required": null where none were (Claude Code's own
// tools or an MCP server's; Anthropic's API takes the null, xAI validates
// the schema). The null required goes, root or nested; a real array of
// names stays.
func TestXAITakesANullRequiredToolSchema(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"grok-4.7","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	f.refuse = func(body []byte) (int, string) {
		if bytes.Contains(body, []byte(`"required":null`)) {
			return 400, xaiNullRequired
		}
		return 0, ""
	}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"grok-4.7"}, Anthropic: "http://api.x.ai"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	// the reporter's shape: Bash's schema with its real required array
	// beside an MCP-flavoured tool whose schema says required: null at the
	// root, one whose nested property does, and two whose null sits under
	// an array — a zod union's anyOf branch and a tuple items (texts cut)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"fake/grok-4.7","stream":true,"max_tokens":128000,
	"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
		{"name":"mcp__server__search","description":"Search","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":null}},
		{"name":"mcp__server__nested","description":"Nested","input_schema":{"type":"object","properties":{"opts":{"type":"object","properties":{"a":{"type":"string"}},"required":null},"b":{"type":"number"}}}},
		{"name":"mcp__server__union","description":"Union","input_schema":{"type":"object","properties":{"filter":{"anyOf":[{"type":"object","properties":{"id":{"type":"string"}},"required":null},{"type":"string"}]}}}},
		{"name":"mcp__server__pair","description":"Pair","input_schema":{"type":"object","items":[{"type":"object","properties":{"first":{"type":"string"}},"required":null}],"type":"object"}}],
	"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s\nxAI got %s", rec.Code, rec.Body, f.got)
	}
	if bytes.Contains(f.got, []byte(`"required":null`)) {
		t.Errorf("null required left in a tool schema: %s", f.got)
	}
	for _, said := range []string{`"required":["command"]`, `"query"`, `"opts"`, `"mcp__server__nested"`, `"anyOf"`, `"first"`} {
		if !bytes.Contains(f.got, []byte(said)) {
			t.Errorf("xAI's schemas lost %q: %s", said, f.got)
		}
	}
}

// The other two shapes a tool schema leaves magpie in get the same null
// out: Responses' parameters and chat's function.parameters.
func TestXaiBodyDropsNullRequiredInEveryShape(t *testing.T) {
	p := provider.Provider{ID: "fake", Chat: "https://api.x.ai/v1", Responses: "http://api.x.ai/v1"}
	chat := `{"model":"fake/grok-4.7","tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":null}}},{"type":"function","function":{"name":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],"messages":[]}`
	got := xaiBody(p, provider.Chat, []byte(chat))
	if bytes.Contains(got, []byte(`"required":null`)) {
		t.Errorf("chat shape kept the null: %s", got)
	}
	if !bytes.Contains(got, []byte(`"required":["path"]`)) || !bytes.Contains(got, []byte(`"command"`)) {
		t.Errorf("chat shape lost a real schema: %s", got)
	}
	responses := `{"model":"fake/grok-4.7","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":null}}],"input":[]}`
	got = xaiBody(p, provider.Responses, []byte(responses))
	if bytes.Contains(got, []byte(`"required":null`)) {
		t.Errorf("responses shape kept the null: %s", got)
	}
	if !bytes.Contains(got, []byte(`"cmd"`)) {
		t.Errorf("responses shape lost a real schema: %s", got)
	}
}

// A body xAI takes is passed through untouched: the shapers change
// nothing else about it, and another host's own quirks stay its own.
func TestXaiBodyLeavesWhatItTakes(t *testing.T) {
	plain := `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"reasoning","id":"rs_1","encrypted_content":"gAABblob"}],"messages":[{"role":"user","content":"hi"}],"system":"prompt"}`
	if got := withoutNullReasoningContent([]byte(plain)); !bytes.Equal(got, []byte(plain)) {
		t.Errorf("reasoning without content changed: %s", got)
	}
	if got := systemTurnsFolded([]byte(plain)); !bytes.Equal(got, []byte(plain)) {
		t.Errorf("no system turn changed: %s", got)
	}
	// a reasoning item's null content alone goes
	got := withoutNullReasoningContent([]byte(xaiSecondTurn))
	if bytes.Contains(got, []byte(`"content":null`)) || !bytes.Contains(got, []byte(`"input_text"`)) {
		t.Errorf("null content not gone: %s", got)
	}
	// a system turn with a string content folds beside a string system
	got = systemTurnsFolded([]byte(`{"model":"m","max_tokens":10,"system":"prompt","messages":[{"role":"user","content":"hi"},{"role":"system","content":"mid"}]}`))
	var q struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.System) != 2 || q.System[0].Text != "prompt" || q.System[1].Text != "mid" {
		t.Errorf("system folded to %s", got)
	}
	if len(q.Messages) != 1 || q.Messages[0].Role != "user" {
		t.Errorf("messages left %s", got)
	}
	// another host's body is untouched
	p := provider.Provider{ID: "other", Chat: "https://api.other/v1"}
	if got := xaiBody(p, provider.Responses, []byte(xaiSecondTurn)); !bytes.Equal(got, []byte(xaiSecondTurn)) {
		t.Errorf("other host shaped: %s", got)
	}
	// a schema whose required are all real arrays keeps its bytes
	real := `{"model":"fake/grok-4.7","tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}],"messages":[]}`
	xp := provider.Provider{ID: "fake", Chat: "https://api.x.ai/v1"}
	if got := xaiBody(xp, provider.Chat, []byte(real)); !bytes.Equal(got, []byte(real)) {
		t.Errorf("real required arrays changed: %s", got)
	}
}

// xaiRefusesRequiredlessObjects refuses as xAI does past the null
// (#3392, TwinsDestiny on v0.1.2557 and guqing on 0.1.4344, straight
// against api.x.ai): an object schema node whose required is left out is
// taken for null — {"type":"object","properties":{}} is turned away, the
// same with "required":[] goes through.
func xaiRefusesRequiredlessObjects(body []byte) (int, string) {
	var q struct {
		Tools []struct {
			InputSchema json.RawMessage `json:"input_schema"`
			Parameters  json.RawMessage `json:"parameters"`
			Function    json.RawMessage `json:"function"`
		} `json:"tools"`
	}
	json.Unmarshal(body, &q)
	for _, t := range q.Tools {
		for _, s := range []json.RawMessage{t.InputSchema, t.Parameters, t.Function} {
			var v any
			if json.Unmarshal(s, &v) == nil && objectWithoutRequired(v) {
				return 400, xaiNullRequired
			}
		}
	}
	return 0, ""
}

// objectWithoutRequired says whether v holds a node of type object whose
// required is no array — left out or null.
func objectWithoutRequired(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if t["type"] == "object" {
			if _, ok := t["required"].([]any); !ok {
				return true
			}
		}
		for k, child := range t {
			if k == "default" || k == "enum" || k == "const" || k == "examples" {
				continue // data, not a schema
			}
			if objectWithoutRequired(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if objectWithoutRequired(child) {
				return true
			}
		}
	}
	return false
}

// xaiMessagesOK is a Messages stream xAI answers a turn with.
func xaiMessagesOK(t *testing.T) *fake {
	return &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"grok-4.7","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
}

// anthropicAt is a server whose fake provider's Messages base is base, its
// requests dialled to up whatever host base names.
func anthropicAt(t *testing.T, up *httptest.Server, base string) *Server {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"grok-4.7"}, Anthropic: base}); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	return s
}

// claudeCode2327Turn is a turn of Claude Code 2.1.327 with its tools as it
// sent them, byte for byte (testdata, captured from the real binary
// against a recording upstream): CronList's schema is
// {"type":"object","properties":{},"additionalProperties":false}, and
// TaskStop, ListAgents, ScheduleWakeup, Workflow and EnterWorktree leave
// required out at their root too.
func claudeCode2327Turn(t *testing.T) (body, tools []byte) {
	t.Helper()
	tools, err := os.ReadFile(filepath.Join("testdata", "claude-code-2.1.327-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(`{"model":"fake/grok-4.7","messages":[{"role":"user","content":[{"type":"text","text":"remove the old dir"}]}],"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"tools":` + string(tools) + `,"max_tokens":64000,"stream":true}`)
	return body, tools
}

const cronListSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{},"additionalProperties":false}`

// #3392's round after the null fix: Claude Code to xAI still 400ed
// "/required: null is not of type \"array\"" with no null in the body,
// for its no-argument tools leave required out and xAI takes that as the
// null. Every such object goes out with "required": []; a real array
// stays as it came, and the rest of the body keeps its bytes.
func TestXAIFillsClaudeCodesLeftOutRequired(t *testing.T) {
	f := xaiMessagesOK(t)
	f.refuse = xaiRefusesRequiredlessObjects
	up := setup(t, provider.Anthropic, f)
	s := anthropicAt(t, up, "http://api.x.ai")
	body, _ := claudeCode2327Turn(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var q struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Required json.RawMessage `json:"required"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(f.got, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Tools) != 21 {
		t.Fatalf("xAI got %d tools, Claude Code sent 21", len(q.Tools))
	}
	for _, tl := range q.Tools {
		want := `[]`
		if tl.Name == "Bash" {
			want = `["command"]`
		}
		if tl.Name == "Bash" || tl.Name == "CronList" || tl.Name == "TaskStop" || tl.Name == "Workflow" {
			if got := string(tl.InputSchema.Required); got != want {
				t.Errorf("%s's required went as %s, want %s", tl.Name, got, want)
			}
		}
	}
	if !bytes.Contains(f.got, []byte(`"messages":[{"role":"user","content":[{"type":"text","text":"remove the old dir"}]}]`)) {
		t.Errorf("the messages didn't keep their bytes: %.400s", f.got)
	}
}

// The objects an MCP server's schema nests: in a property, a union's
// branch, an items schema, a tuple's, $defs — left out or null, each gets
// its []. A null required on a node that is no object goes, and a
// default or an enum holding an object's shape is data, left as it came.
func TestXAIFillsNestedObjectsRequired(t *testing.T) {
	f := xaiMessagesOK(t)
	f.refuse = xaiRefusesRequiredlessObjects
	up := setup(t, provider.Anthropic, f)
	s := anthropicAt(t, up, "http://api.x.ai")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"fake/grok-4.7","stream":true,"max_tokens":128000,
	"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
		{"name":"mcp__s__plan","description":"Plan","input_schema":{"type":"object","properties":{"plan":{"type":"object","properties":{}}},"required":null}},
		{"name":"mcp__s__find","description":"Find","input_schema":{"type":"object","properties":{"filter":{"anyOf":[{"type":"object","properties":{"id":{"type":"string"}}},{"type":"string","required":null}]},"rows":{"type":"array","items":{"type":["object","null"],"properties":{"k":{"type":"string"}}}},"pair":{"type":"array","prefixItems":[{"$ref":"#/$defs/pt"}]}},"$defs":{"pt":{"type":"object","properties":{"x":{"type":"number"}}}},"required":["filter"]}},
		{"name":"mcp__s__opts","description":"Opts","input_schema":{"type":"object","properties":{"opts":{"type":"object","default":{"type":"object"},"enum":[{"type":"object"}]}},"required":[]}}],
	"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s\nxAI got %s", rec.Code, rec.Body, f.got)
	}
	if bytes.Contains(f.got, []byte(`"required":null`)) {
		t.Errorf("null required left: %s", f.got)
	}
	// plan's root and its plan, filter's object branch, rows' items, $defs' pt, opts
	if n := bytes.Count(f.got, []byte(`"required":[]`)); n != 7 {
		t.Errorf("%d required:[] where 6 nodes left theirs out and opts' root had one: %s", n, f.got)
	}
	for _, said := range []string{`"required":["command"]`, `"required":["filter"]`, `"default":{"type":"object"}`, `"enum":[{"type":"object"}]`} {
		if !bytes.Contains(f.got, []byte(said)) {
			t.Errorf("xAI's schemas lost %s: %s", said, f.got)
		}
	}
}

// The other two shapes a tool schema leaves magpie in are filled alike:
// chat's function.parameters and Responses' parameters.
func TestXaiBodyFillsALeftOutRequiredInEveryShape(t *testing.T) {
	p := provider.Provider{ID: "fake", Chat: "https://api.x.ai/v1", Responses: "http://api.x.ai/v1"}
	chat := `{"model":"fake/grok-4.7","tools":[{"type":"function","function":{"name":"CronList","parameters":` + cronListSchema + `}}],"messages":[]}`
	if got := xaiBody(p, provider.Chat, []byte(chat)); !bytes.Contains(got, []byte(`"required":[]`)) {
		t.Errorf("chat shape kept its left-out required: %s", got)
	}
	responses := `{"model":"fake/grok-4.7","tools":[{"type":"function","name":"TaskStop","parameters":{"type":"object"}}],"input":[]}`
	if got := xaiBody(p, provider.Responses, []byte(responses)); !bytes.Contains(got, []byte(`"required":[]`)) {
		t.Errorf("responses shape kept its left-out required: %s", got)
	}
}

// Another upstream gets Claude Code's tools as it sent them, CronList's
// left-out required too: the filling is xAI's alone.
func TestOtherUpstreamGetsClaudeCodesToolsAsSent(t *testing.T) {
	f := xaiMessagesOK(t)
	up := setup(t, provider.Anthropic, f)
	s := anthropicAt(t, up, "http://api.anthropic-like.example")
	body, tools := claudeCode2327Turn(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !bytes.Contains(f.got, tools) {
		t.Errorf("another upstream's tools were changed: %.600s", f.got)
	}
	if !bytes.Contains(f.got, []byte(cronListSchema)) {
		t.Errorf("CronList's schema changed for another upstream: %.600s", f.got)
	}
}

// Claude Code counts a turn's tokens with the same tools, and xAI's
// count_tokens is sent them filled as the turn is.
func TestXAICountTokensFillsRequired(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"input_tokens":5}`}
	f.refuse = xaiRefusesRequiredlessObjects
	up := setup(t, provider.Anthropic, f)
	s := anthropicAt(t, up, "http://api.x.ai")
	body, _ := claudeCode2327Turn(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens", bytes.NewReader(body)))
	if rec.Code != 200 || f.path != "/v1/messages/count_tokens" {
		t.Fatalf("%d %s at %q", rec.Code, rec.Body, f.path)
	}
	if !strings.Contains(rec.Body.String(), `"input_tokens":5`) {
		t.Errorf("count not xAI's: %s", rec.Body)
	}
	if bytes.Contains(f.got, []byte(cronListSchema)) {
		t.Errorf("CronList went to xAI's count without its required: %.600s", f.got)
	}
}

// xaiRefusesStop answers as xAI's Messages and chat answered guqing's
// Claude Code (#3392): a stop sequence there is turned away by name.
func xaiRefusesStop(body []byte) (int, string) {
	var q struct {
		Stop          json.RawMessage `json:"stop"`
		StopSequences json.RawMessage `json:"stop_sequences"`
	}
	json.Unmarshal(body, &q)
	for _, s := range []json.RawMessage{q.Stop, q.StopSequences} {
		if t := string(bytes.TrimSpace(s)); t != "" && t != "null" && t != "[]" && t != `""` {
			return 400, `{"code":"Client specified an invalid argument","error":"Model grok-4.7 does not support parameter stop."}`
		}
	}
	return 0, ""
}

// providerAt is a server whose fake provider is p, its requests dialled
// to up whatever host p's bases name.
func providerAt(t *testing.T, up *httptest.Server, p provider.Provider) *Server {
	t.Helper()
	p.ID, p.Name, p.Key, p.Models = "fake", "Fake", "k", []string{"grok-4.7"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	return s
}

func xaiChatOK(t *testing.T) *fake {
	return &fake{t: t, reply: sse(
		`data: {"id":"c1","object":"chat.completion.chunk","model":"grok-4.7","choices":[{"index":0,"delta":{"role":"assistant","content":"<block>no"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"grok-4.7","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`,
		`data: [DONE]`)}
}

// Claude Code's auto-mode classifier asks with stop_sequences
// ["</block>"], which xAI's Messages turns away (#3392, guqing): relayed
// to api.x.ai the list goes, and the ask is answered.
func TestXAIDropsClaudeCodesStopSequences(t *testing.T) {
	f := xaiMessagesOK(t)
	f.refuse = xaiRefusesStop
	up := setup(t, provider.Anthropic, f)
	s := providerAt(t, up, provider.Provider{Anthropic: "http://api.x.ai"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(autoModeAsk("fake/grok-4.7"))))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if bytes.Contains(f.got, []byte(`stop_sequences`)) {
		t.Errorf("stop_sequences went to xAI: %s", f.got)
	}
	if !bytes.Contains(f.got, []byte(`Action: Bash rm -rf build/`)) || !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("the ask or its answer lost on the way: %s\n%s", f.got, rec.Body)
	}
}

// The same ask translated to xAI's chat goes without its stop, and a chat
// client's own stop, a string or a list, goes too.
func TestXAIChatGoesWithoutStop(t *testing.T) {
	f := xaiChatOK(t)
	f.refuse = xaiRefusesStop
	up := setup(t, provider.Chat, f)
	s := providerAt(t, up, provider.Provider{Chat: "http://api.x.ai/v1"})
	for name, req := range map[string]struct{ path, body string }{
		"translated":  {"/v1/messages", autoModeAsk("fake/grok-4.7")},
		"chat list":   {"/v1/chat/completions", `{"model":"fake/grok-4.7","stream":true,"stop":["</block>"],"messages":[{"role":"user","content":"hi"}]}`},
		"chat string": {"/v1/chat/completions", `{"model":"fake/grok-4.7","stream":true,"stop":"</block>","messages":[{"role":"user","content":"hi"}]}`},
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", req.path, strings.NewReader(req.body)))
		if rec.Code != 200 || f.path != "/v1/chat/completions" {
			t.Errorf("%s: %d %s at %q", name, rec.Code, rec.Body, f.path)
			continue
		}
		if bytes.Contains(f.got, []byte(`"stop"`)) || bytes.Contains(f.got, []byte(`</block>`)) && !bytes.Contains(f.got, []byte(`Answer <block>`)) {
			t.Errorf("%s: a stop went to xAI: %s", name, f.got)
		}
	}
}

// Stop sequences are xAI's to lose alone: another upstream gets the
// classifier's ["</block>"], and xaiBody leaves an empty list and
// Responses' body as they came.
func TestStopKeptOffXAI(t *testing.T) {
	f := xaiMessagesOK(t)
	up := setup(t, provider.Anthropic, f)
	s := providerAt(t, up, provider.Provider{Anthropic: "http://api.anthropic-like.example"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(autoModeAsk("fake/grok-4.7"))))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !bytes.Contains(f.got, []byte(`"stop_sequences":["</block>"]`)) {
		t.Errorf("another upstream lost the stop sequences: %s", f.got)
	}
	x := provider.Provider{ID: "fake", Anthropic: "https://api.x.ai", Chat: "https://api.x.ai/v1"}
	for _, b := range []string{`{"model":"grok-4.7","stop_sequences":[],"messages":[]}`, `{"model":"grok-4.7","stop":"","messages":[]}`} {
		proto := provider.Anthropic
		if strings.Contains(b, `"stop":`) {
			proto = provider.Chat
		}
		if got := xaiBody(x, proto, []byte(b)); string(got) != b {
			t.Errorf("an empty stop was written over: %s", got)
		}
	}
}
