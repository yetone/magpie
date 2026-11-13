package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Google's API takes a part's fields by their proto names as well
// (inline_data, mime_type, file_data, function_call), which agy and raw
// SDK calls send: read only as camelCase, an image or a PDF was left out
// and the model made up what it held (#934). The caller's own keys — a
// call's args, a declaration's schema — are kept as they were.
func TestGeminiSnakeCaseParts(t *testing.T) {
	body := `{"system_instruction":{"parts":[{"text":"be brief"}]},
	"generation_config":{"max_output_tokens":77,"thinking_config":{"thinking_level":"high"}},
	"tools":[{"function_declarations":[{"name":"read","parameters":{"type":"object","properties":{"file_path":{"type":"string"}}}}]}],
	"contents":[
		{"role":"user","parts":[{"text":"Transcribe this."},{"inline_data":{"mime_type":"image/jpeg","data":"SU1H"}},
			{"inline_data":{"mime_type":"application/pdf","data":"UERG"}},{"file_data":{"mime_type":"application/pdf","file_uri":"gs://b/doc.pdf"}}]},
		{"role":"model","parts":[{"function_call":{"id":"c1","name":"read","args":{"file_path":"/a_b"}}}]},
		{"role":"user","parts":[{"function_response":{"id":"c1","name":"read","response":{"line_count":3}}}]}]}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if r.System != "be brief" || r.MaxTokens != 77 || r.Effort != "high" {
		t.Fatalf("system %q, max %d, effort %q", r.System, r.MaxTokens, r.Effort)
	}
	if len(r.Tools) != 1 || !strings.Contains(string(r.Tools[0].Schema), `"file_path"`) {
		t.Fatalf("tools: %+v", r.Tools)
	}
	if len(r.Messages) != 3 {
		t.Fatalf("messages: %+v", r.Messages)
	}
	got := r.Messages[0].Parts
	if len(got) != 4 || got[1].Kind != Image || got[1].MediaType != "image/jpeg" || got[1].Data != "SU1H" ||
		got[2].Kind != File || got[2].MediaType != "application/pdf" || got[2].Data != "UERG" ||
		got[3].Kind != File || got[3].URL != "gs://b/doc.pdf" {
		t.Fatalf("the user's parts: %+v", got)
	}
	call := r.Messages[1].Parts
	if len(call) != 1 || call[0].Kind != ToolCall || call[0].Name != "read" || !strings.Contains(string(call[0].Args), `"file_path":"/a_b"`) {
		t.Fatalf("the call: %+v", call)
	}
	res := r.Messages[2].Parts
	if len(res) != 1 || res[0].Kind != ToolResult || !strings.Contains(res[0].Text, "line_count") {
		t.Fatalf("the result: %+v", res)
	}
}

// Through the gateway: an image sent as inline_data to /v1beta reaches a
// Chat upstream as the image it is.
func TestGeminiSnakeCaseImageReachesUpstream(t *testing.T) {
	setHome(t, t.TempDir())
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, out := post(t, "/v1beta/models/fake/m1:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"Transcribe this."},{"inline_data":{"mime_type":"image/png","data":"iVBORw0KGgo="}}]}]}`)
	if code != 200 || !strings.Contains(out, "OK") {
		t.Fatalf("%d %s", code, out)
	}
	if !strings.Contains(string(f.got), "data:image/png;base64,iVBORw0KGgo=") {
		t.Fatalf("the upstream was not given the image: %s", f.got)
	}
}

// A Chat Completions file part (a PDF) is the request's file: Antigravity
// (Code Assist) is given it as inline data, where it was left out and the
// model answered as if it had read it (#934).
func TestChatFilePart(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"Transcribe every page."},
		{"type":"file","file":{"filename":"excerpt.pdf","file_data":"data:application/pdf;base64,UERG"}},
		{"type":"file","file":{"filename":"notes.pdf","file_data":"QkFSRQ=="}},
		{"type":"file","file":{"file_id":"file-abc","filename":"x.pdf"}}]}]}`
	r, err := parseChat([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	parts := r.Messages[0].Parts
	if len(parts) != 4 || parts[1].Kind != File || parts[1].MediaType != "application/pdf" || parts[1].Data != "UERG" ||
		parts[2].Kind != File || parts[2].MediaType != "application/pdf" || parts[2].Data != "QkFSRQ==" ||
		parts[3].Kind != File || parts[3].URL != "file-abc" {
		t.Fatalf("parts: %+v", parts)
	}
	var sent struct {
		Request struct {
			Contents []struct {
				Parts []map[string]json.RawMessage `json:"parts"`
			} `json:"contents"`
		} `json:"request"`
	}
	if err := json.Unmarshal(buildCodeAssist(r, "gemini-3.8-flash", "antigravity"), &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Request.Contents) != 1 || len(sent.Request.Contents[0].Parts) != 4 ||
		!strings.Contains(string(sent.Request.Contents[0].Parts[1]["inlineData"]), `"application/pdf"`) {
		t.Fatalf("sent: %+v", sent)
	}
	// an upstream that can't carry a file is told of it, not left unaware
	if b := buildChat(r, "m", "x", false); !strings.Contains(string(b), "[attachment application/pdf]") {
		t.Fatalf("chat: %s", b)
	}
}

// A held Gemini stream is kept alive with nothing: agy fails on an SSE
// comment ("invalid stream chunk: : keepalive", #934), as Google's SDK
// does (geminiEncoder.keepalive). The other protocols still get theirs.
func TestHeldGeminiStreamSendsNoComment(t *testing.T) {
	for _, c := range []struct {
		proto provider.Protocol
		want  bool
	}{{provider.Gemini, false}, {provider.Anthropic, true}, {provider.Chat, true}} {
		rec := httptest.NewRecorder()
		h := newHoldWriter(rec, true)
		h.alive = &keptAlive{proto: c.proto, at: time.Now().Add(-time.Hour)}
		h.keepAlive()
		if got := strings.Contains(rec.Body.String(), ": keepalive"); got != c.want {
			t.Fatalf("%s: %q", c.proto, rec.Body.String())
		}
	}
}

// ZCode with a local WebM attached sends a Messages video block or, on
// Chat, a video_url data URL (Flandern1211's capture of ZCode 0.17.1,
// #5098). Both reach a Gemini model through Code Assist as inlineData with
// the bytes and type ZCode sent, now that magpie tells ZCode the model
// takes video (zcodeInputs).
func TestZCodeVideoReachesCodeAssist(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"messages", `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"what happens?"},{"type":"video","source":{"type":"base64","media_type":"video/webm","data":"GkXfo59ChoEB"}}]}]}`},
		{"chat", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"what happens?"},{"type":"video_url","video_url":{"url":"data:video/webm;base64,GkXfo59ChoEB"}}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parse := parseAnthropic
			if tc.name == "chat" {
				parse = parseChat
			}
			r, err := parse([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			b := string(buildCodeAssist(r, "gemini-4-argon", "antigravity"))
			if !strings.Contains(b, `"mimeType":"video/webm"`) || !strings.Contains(b, `"data":"GkXfo59ChoEB"`) {
				t.Fatalf("the video didn't reach Code Assist: %s", b)
			}
		})
	}
}
