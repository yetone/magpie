package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

const (
	pngA = "aGVsbG8="     // what a screenshot tool returns, inline
	pngB = "d29ybGQhIQ==" // a second one
)

// toolImageUp is a provider whose one model sees, speaking Chat or
// Responses; it keeps the last request it was sent.
func toolImageUp(t *testing.T, proto provider.Protocol) func() string {
	t.Helper()
	fresh(t)
	var mu sync.Mutex
	var sent string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = string(body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if proto == provider.Responses {
			io.WriteString(w, sse(
				`data: {"type":"response.created","response":{"id":"r1"}}`,
				`data: {"type":"response.output_text.delta","delta":"ok"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":1,"output_tokens":1}}}`))
			return
		}
		io.WriteString(w, sse(
			`data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "probe", Key: "key", Models: []string{"eye"}}
	if proto == provider.Responses {
		p.Responses = up.URL + "/v1"
	} else {
		p.Chat = up.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{{ID: "eye", Images: true, ImageInput: imageInputBool(true)}}); err != nil {
		t.Fatal(err)
	}
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return sent
	}
}

func postToolImages(t *testing.T, path, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
}

// chatMsg is a message as a Chat upstream was sent it.
type chatMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID string `json:"id"`
	} `json:"tool_calls"`
}

// chatPart is a part of such a message's content, when it is a list.
type chatPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

func chatMsgs(t *testing.T, sent string) []chatMsg {
	t.Helper()
	var q struct {
		Messages []chatMsg `json:"messages"`
	}
	if err := json.Unmarshal([]byte(sent), &q); err != nil {
		t.Fatalf("%v: %s", err, sent)
	}
	return q.Messages
}

func (m chatMsg) text() string {
	var s string
	json.Unmarshal(m.Content, &s)
	return s
}

func (m chatMsg) parts() []chatPart {
	var ps []chatPart
	json.Unmarshal(m.Content, &ps)
	return ps
}

func roles(msgs []chatMsg) string {
	var rs []string
	for _, m := range msgs {
		rs = append(rs, m.Role)
	}
	return strings.Join(rs, ",")
}

// imagesIn is the image URLs of a message's parts.
func imagesIn(m chatMsg) []string {
	var us []string
	for _, p := range m.parts() {
		if p.Type == "image_url" {
			us = append(us, p.ImageURL.URL)
		}
	}
	return us
}

// Claude Code's Read on a .png, and its screenshot tools, answer with a
// tool_result holding an image. A Chat tool message holds text only, so the
// image goes in the user message after it, not lost.
func TestToolResultImageReachesChatModel(t *testing.T) {
	sent := toolImageUp(t, provider.Chat)
	for _, tc := range []struct {
		name, after string
		wantRoles   string
	}{
		// Claude Code sends a system reminder beside the result: the
		// images open that user message
		{"with the user's text", `,{"type":"text","text":"<system-reminder>keep going</system-reminder>"}`, "user,assistant,tool,user"},
		{"alone", ``, "user,assistant,tool,user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			postToolImages(t, "/v1/messages", `{"model":"probe/eye","max_tokens":64,"stream":true,"messages":[
				{"role":"user","content":"look at shot.png"},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"shot.png"}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"Read shot.png"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+pngA+`"}}]}`+tc.after+`]}]}`)
			msgs := chatMsgs(t, sent())
			if roles(msgs) != tc.wantRoles {
				t.Fatalf("roles %s, want %s: %s", roles(msgs), tc.wantRoles, sent())
			}
			tool := msgs[2]
			if tool.ToolCallID != "toolu_1" || !strings.Contains(tool.text(), "Read shot.png") || !strings.Contains(tool.text(), "[The tool returned an image; it follows in the next message.]") {
				t.Fatalf("tool message: %s", tool.Content)
			}
			user := msgs[3]
			ps := user.parts()
			if len(ps) < 2 || ps[0].Type != "text" || !strings.Contains(ps[0].Text, "Read (tool call toolu_1)") {
				t.Fatalf("user message doesn't say where its image is from: %s", user.Content)
			}
			if got := imagesIn(user); len(got) != 1 || got[0] != "data:image/png;base64,"+pngA {
				t.Fatalf("images %v: %s", got, user.Content)
			}
			if tc.after != "" && !strings.Contains(ps[len(ps)-1].Text, "keep going") {
				t.Fatalf("the user's own text isn't after the image: %s", user.Content)
			}
		})
	}
}

// Parallel tool calls each answered with an image: every tool message comes
// right after the call, and one user message after them all has the images.
func TestParallelToolResultImagesFollowAllToolMessages(t *testing.T) {
	sent := toolImageUp(t, provider.Chat)
	postToolImages(t, "/v1/messages", `{"model":"probe/eye","max_tokens":64,"messages":[
		{"role":"user","content":"compare the two"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_a","name":"screenshot","input":{}},{"type":"tool_use","id":"toolu_b","name":"Read","input":{"file_path":"b.png"}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_a","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+pngA+`"}}]},
			{"type":"tool_result","tool_use_id":"toolu_b","content":[{"type":"text","text":"b.png"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"`+pngB+`"}},{"type":"image","source":{"type":"url","url":"https://example.com/b2.png"}}]}]}]}`)
	msgs := chatMsgs(t, sent())
	if roles(msgs) != "user,assistant,tool,tool,user" {
		t.Fatalf("roles %s: %s", roles(msgs), sent())
	}
	if msgs[2].ToolCallID != "toolu_a" || msgs[2].text() != "[The tool returned an image; it follows in the next message.]" {
		t.Fatalf("first tool message: %s", msgs[2].Content)
	}
	if msgs[3].ToolCallID != "toolu_b" || !strings.HasPrefix(msgs[3].text(), "b.png\n\n[The tool returned 2 images;") {
		t.Fatalf("second tool message: %s", msgs[3].Content)
	}
	want := []string{"data:image/png;base64," + pngA, "data:image/jpeg;base64," + pngB, "https://example.com/b2.png"}
	if got := imagesIn(msgs[4]); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("images %v, want %v", got, want)
	}
	var labels []string
	for _, p := range msgs[4].parts() {
		if p.Type == "text" {
			labels = append(labels, p.Text)
		}
	}
	if len(labels) != 2 || !strings.Contains(labels[0], "screenshot (tool call toolu_a)") || !strings.Contains(labels[1], "Read (tool call toolu_b)") {
		t.Fatalf("labels %q", labels)
	}
}

// A screenshot from an earlier turn stays in the conversation, its user
// message before the assistant's answer to it.
func TestOlderTurnToolResultImageKept(t *testing.T) {
	sent := toolImageUp(t, provider.Chat)
	postToolImages(t, "/v1/messages", `{"model":"probe/eye","max_tokens":64,"messages":[
		{"role":"user","content":"take a screenshot"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"screenshot","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+pngA+`"}}]}]},
		{"role":"assistant","content":[{"type":"text","text":"It shows a login page."}]},
		{"role":"user","content":"what colour was the button?"}]}`)
	msgs := chatMsgs(t, sent())
	if roles(msgs) != "user,assistant,tool,user,assistant,user" {
		t.Fatalf("roles %s: %s", roles(msgs), sent())
	}
	if got := imagesIn(msgs[3]); len(got) != 1 || got[0] != "data:image/png;base64,"+pngA {
		t.Fatalf("older screenshot: %s", msgs[3].Content)
	}
	if msgs[5].text() != "what colour was the button?" {
		t.Fatalf("latest turn: %s", msgs[5].Content)
	}
}

// Codex's function_call_output can hold input_image parts too.
func TestResponsesToolOutputImageReachesChatModel(t *testing.T) {
	sent := toolImageUp(t, provider.Chat)
	postToolImages(t, "/v1/responses", `{"model":"probe/eye","input":[
		{"role":"user","content":[{"type":"input_text","text":"look"}]},
		{"type":"function_call","call_id":"call_1","name":"view_image","arguments":"{\"path\":\"a.png\"}"},
		{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"a.png"},{"type":"input_image","image_url":"data:image/png;base64,`+pngA+`"}]}]}`)
	msgs := chatMsgs(t, sent())
	if roles(msgs) != "user,assistant,tool,user" {
		t.Fatalf("roles %s: %s", roles(msgs), sent())
	}
	if !strings.Contains(msgs[2].text(), "a.png") || !strings.Contains(msgs[2].text(), "it follows in the next message") {
		t.Fatalf("tool message: %s", msgs[2].Content)
	}
	if got := imagesIn(msgs[3]); len(got) != 1 || got[0] != "data:image/png;base64,"+pngA {
		t.Fatalf("image: %s", msgs[3].Content)
	}
	if !strings.Contains(msgs[3].parts()[0].Text, "view_image (tool call call_1)") {
		t.Fatalf("label: %s", msgs[3].Content)
	}
}

// Claude Code to a Responses provider: an output can hold images itself.
func TestToolResultImageReachesResponsesModel(t *testing.T) {
	sent := toolImageUp(t, provider.Responses)
	postToolImages(t, "/v1/messages", `{"model":"probe/eye","max_tokens":64,"messages":[
		{"role":"user","content":"look"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"shot"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+pngA+`"}}]}]}]}`)
	var q struct {
		Input []struct {
			Type   string          `json:"type"`
			Output json.RawMessage `json:"output"`
		} `json:"input"`
	}
	json.Unmarshal([]byte(sent()), &q)
	for _, it := range q.Input {
		if it.Type != "function_call_output" {
			continue
		}
		var out []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
		}
		if json.Unmarshal(it.Output, &out) != nil || len(out) != 2 || out[0].Text != "shot" || out[1].Type != "input_image" || out[1].ImageURL != "data:image/png;base64,"+pngA {
			t.Fatalf("output: %s", it.Output)
		}
		return
	}
	t.Fatalf("no function_call_output: %s", sent())
}

// A model that can't see is given the image's description in the tool
// message, as before: nothing follows it.
func TestTextOnlyModelGetsToolImageDescribed(t *testing.T) {
	s, u := eyed(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"probe/text","max_tokens":64,"messages":[
		{"role":"user","content":"look"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"shot"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+pngA+`"}}]}]}]}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := u.sent("text")
	if len(got) != 1 {
		t.Fatalf("text model sent %d requests", len(got))
	}
	msgs := chatMsgs(t, got[0])
	if roles(msgs) != "user,assistant,tool" {
		t.Fatalf("roles %s: %s", roles(msgs), got[0])
	}
	if tool := msgs[2].text(); !strings.Contains(tool, "HELLO") || !strings.Contains(tool, "shot") || strings.Contains(tool, "follows in the next message") || strings.Contains(got[0], pngA) {
		t.Fatalf("tool message: %s", got[0])
	}
}
