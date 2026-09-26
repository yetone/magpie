package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// kiroMsg encodes one event-stream message, the way Kiro sends them.
func kiroMsg(headers map[string]string, payload string) []byte {
	var h bytes.Buffer
	for k, v := range headers {
		h.WriteByte(byte(len(k)))
		h.WriteString(k)
		h.WriteByte(7)
		binary.Write(&h, binary.BigEndian, uint16(len(v)))
		h.WriteString(v)
	}
	total := 12 + h.Len() + len(payload) + 4
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(total))
	binary.Write(&b, binary.BigEndian, uint32(h.Len()))
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(b.Bytes()))
	b.Write(h.Bytes())
	b.WriteString(payload)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(b.Bytes()))
	return b.Bytes()
}

func kiroEvent(kind, payload string) []byte {
	return kiroMsg(map[string]string{":message-type": "event", ":event-type": kind, ":content-type": "application/json"}, payload)
}

func kiroEvents(t *testing.T, thinking bool, frames ...[]byte) []Event {
	t.Helper()
	out := make(chan Event, 256)
	decodeKiro(context.Background(), bytes.NewReader(bytes.Join(frames, nil)), out, "claude-sonnet-4.5", 200000, thinking)
	var evs []Event
	for ev := range out {
		evs = append(evs, ev)
	}
	return evs
}

// said is the events' text, thinking and calls, in order, merged by kind.
func said(evs []Event) string {
	var b strings.Builder
	last := EventKind(-1)
	for _, ev := range evs {
		tag := map[EventKind]string{KText: "text", KThink: "think", KToolStart: "call", KToolArgs: "args", KStop: "stop", KError: "error"}[ev.Kind]
		if tag == "" {
			continue
		}
		if ev.Kind != last || ev.Kind == KToolStart {
			b.WriteString("|" + tag + ":")
		}
		last = ev.Kind
		switch ev.Kind {
		case KToolStart:
			b.WriteString(ev.ID + "/" + ev.Name)
		case KStop:
			b.WriteString(ev.Stop)
		default:
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

func TestKiroDecodesTextAndCalls(t *testing.T) {
	evs := kiroEvents(t, false,
		kiroEvent("assistantResponseEvent", `{"content":"Let me "}`),
		kiroEvent("assistantResponseEvent", `{"content":"look."}`),
		kiroEvent("toolUseEvent", `{"name":"read","toolUseId":"tooluse_1","input":"{\"pa"}`),
		kiroEvent("toolUseEvent", `{"name":"read","toolUseId":"tooluse_1","input":"th\":\"a\"}"}`),
		kiroEvent("toolUseEvent", `{"name":"read","toolUseId":"tooluse_1","stop":true}`),
		kiroEvent("toolUseEvent", `{"name":"ls","toolUseId":"tooluse_2","input":"{}"}`),
		kiroEvent("metadataEvent", `{"tokenUsage":{"uncachedInputTokens":120,"outputTokens":30,"cacheReadInputTokens":7}}`),
	)
	if got, want := said(evs), `|text:Let me look.|call:tooluse_1/read|args:{"path":"a"}|call:tooluse_2/ls|args:{}|stop:tool`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if evs[0].Kind != KStart {
		t.Fatalf("first = %+v", evs[0])
	}
	var u Usage
	for _, ev := range evs {
		if ev.Kind == KUsage {
			u = ev.Usage
		}
	}
	if u.Input != 120 || u.Output != 30 || u.CacheRead != 7 {
		t.Fatalf("usage = %+v", u)
	}
}

// With no token counts, the input is how full Kiro says the context is.
func TestKiroUsageFromContextPercentage(t *testing.T) {
	evs := kiroEvents(t, false,
		kiroEvent("assistantResponseEvent", `{"content":"12345678"}`),
		kiroEvent("contextUsageEvent", `{"contextUsagePercentage":1.5}`),
		kiroEvent("metadataEvent", `{"stopReason":"MAX_TOKENS"}`),
	)
	for _, ev := range evs {
		if ev.Kind == KUsage && (ev.Usage.Input != 3000 || ev.Usage.Output != 2) {
			t.Fatalf("usage = %+v", ev.Usage)
		}
	}
	if got := said(evs); !strings.HasSuffix(got, "|stop:length") {
		t.Fatal(got)
	}
}

// Asked to think, the reply's <thinking> is the thinking, however its tags
// fall across the pieces.
func TestKiroThinkingTags(t *testing.T) {
	evs := kiroEvents(t, true,
		kiroEvent("assistantResponseEvent", `{"content":"<thin"}`),
		kiroEvent("assistantResponseEvent", `{"content":"king>"}`),
		kiroEvent("assistantResponseEvent", `{"content":"\nHmm, a < b"}`),
		kiroEvent("assistantResponseEvent", `{"content":" so.</thi"}`),
		kiroEvent("assistantResponseEvent", `{"content":"nking>\n\nIt is."}`),
	)
	if got, want := said(evs), "|think:Hmm, a < b so.|text:It is.|stop:stop"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// a reply that doesn't open with it is all text
	evs = kiroEvents(t, true, kiroEvent("assistantResponseEvent", `{"content":"<b>hi</b>"}`))
	if got, want := said(evs), "|text:<b>hi</b>|stop:stop"; got != want {
		t.Fatal(got)
	}
	// and a call ends the thinking it cut short
	evs = kiroEvents(t, true,
		kiroEvent("assistantResponseEvent", `{"content":"<thinking>plan</th"}`),
		kiroEvent("toolUseEvent", `{"name":"ls","toolUseId":"t1","input":"{}"}`),
	)
	if got, want := said(evs), "|think:plan</th|call:t1/ls|args:{}|stop:tool"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestKiroStreamErrors(t *testing.T) {
	evs := kiroEvents(t, false,
		kiroEvent("assistantResponseEvent", `{"content":"hi"}`),
		kiroMsg(map[string]string{":message-type": "exception", ":exception-type": "ThrottlingException"}, `{"message":"slow down"}`),
	)
	if got := said(evs); got != "|text:hi|error:rate limited: slow down" {
		t.Fatal(got)
	}
	// a reply cut off mid-message
	full := kiroEvent("assistantResponseEvent", `{"content":"hi"}`)
	evs = kiroEvents(t, false, full, full[:10])
	if got := said(evs); !strings.Contains(got, "|error:the reply broke off") {
		t.Fatal(got)
	}
}

func TestReadKiroFrameHeaders(t *testing.T) {
	// a frame with headers of other types before the one read
	var h bytes.Buffer
	h.Write([]byte{4, 'f', 'l', 'a', 'g', 0})               // true
	h.Write([]byte{3, 'n', 'u', 'm', 4, 0, 0, 0, 9})        // int32
	h.Write([]byte{2, 't', 's', 8, 0, 0, 0, 0, 0, 0, 0, 1}) // timestamp
	h.Write([]byte{11, ':', 'e', 'v', 'e', 'n', 't', '-', 't', 'y', 'p', 'e', 7, 0, 1, 'x'})
	total := 12 + h.Len() + 2 + 4
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(total))
	binary.Write(&b, binary.BigEndian, uint32(h.Len()))
	b.Write([]byte{0, 0, 0, 0})
	b.Write(h.Bytes())
	b.WriteString("{}")
	b.Write([]byte{0, 0, 0, 0})
	f, err := readKiroFrame(bufio.NewReader(&b))
	if err != nil || f.headers[":event-type"] != "x" || string(f.payload) != "{}" {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := readKiroFrame(bufio.NewReader(bytes.NewReader([]byte{0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0}))); err == nil {
		t.Fatal("read a frame too short to be one")
	}
}

type kiroBody struct {
	ConversationState struct {
		CurrentMessage struct {
			UserInputMessage kiroUser `json:"userInputMessage"`
		} `json:"currentMessage"`
		History []struct {
			User *kiroUser `json:"userInputMessage"`
			Asst *kiroAsst `json:"assistantResponseMessage"`
		} `json:"history"`
		ChatTriggerType string `json:"chatTriggerType"`
	} `json:"conversationState"`
	ProfileArn string `json:"profileArn"`
}

func buildKiroBody(t *testing.T, r *Request, thinking bool) kiroBody {
	t.Helper()
	var b kiroBody
	if err := json.Unmarshal(buildKiro(r, "claude-sonnet-4.5", "arn:p", thinking), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildKiro(t *testing.T) {
	long := "call:" + strings.Repeat("x", 80)
	r := &Request{
		System: "Be brief.",
		Tools:  []Tool{{Name: "read", Description: "Read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{
			{Role: "assistant", Parts: []Part{{Kind: Text, Text: "an opening the caller made up"}}},
			{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}, {Kind: Image, MediaType: "image/png", Data: "AAA"}}},
			{Role: "assistant", Parts: []Part{{Kind: Text, Text: "reading"}, {Kind: ToolCall, ID: "c1", Name: "read", Args: json.RawMessage(`{"p":1}`)}, {Kind: ToolCall, ID: long, Name: "grep", Args: json.RawMessage(`{}`)}}},
			{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "file"}, {Kind: ToolResult, CallID: "orphan", Text: "?"}}},
			{Role: "user", Parts: []Part{{Kind: Text, Text: "and?"}, {Kind: Image, MediaType: "image/jpeg", Data: "BBB"}}},
			{Role: "assistant", Parts: []Part{{Kind: Text, Text: "done"}}},
		},
	}
	b := buildKiroBody(t, r, false)
	h := b.ConversationState.History
	if b.ProfileArn != "arn:p" || b.ConversationState.ChatTriggerType != "MANUAL" || len(h) != 4 {
		t.Fatalf("body = %+v", b)
	}
	// it opens with the user, the instructions at its head, its image gone
	// as only the latest is sent again
	if h[0].User == nil || h[0].User.Content != "Be brief.\n\nhi" || len(h[0].User.Images) != 0 || h[0].User.ModelID != "claude-sonnet-4.5" {
		t.Fatalf("first = %+v", h[0].User)
	}
	calls := h[1].Asst.ToolUses
	if h[1].Asst.Content != "reading" || len(calls) != 2 || calls[0].ToolUseID != "c1" || string(calls[0].Input) != `{"p":1}` {
		t.Fatalf("reply = %+v", h[1].Asst)
	}
	if id := calls[1].ToolUseID; id == long || !kiroToolIDRe.MatchString(id) || id != kiroToolID(long) {
		t.Fatalf("id = %q", id)
	}
	// both calls answered, the orphan result gone, the two user turns one
	u := h[2].User
	if u.Content != "and?" || len(u.Images) != 1 || u.Images[0].Format != "jpeg" || u.Context == nil || len(u.Context.ToolResults) != 2 {
		t.Fatalf("results = %+v", u)
	}
	if res := u.Context.ToolResults; res[0].ToolUseID != "c1" || res[0].Status != "success" || res[0].Content[0].Text != "file" ||
		res[1].ToolUseID != calls[1].ToolUseID || res[1].Status != "error" || res[1].Content[0].Text != kiroNoResult {
		t.Fatalf("results = %+v", res)
	}
	// ending on a reply, the message answered is an empty one
	cur := b.ConversationState.CurrentMessage.UserInputMessage
	if h[3].Asst == nil || h[3].Asst.Content != "done" || cur.Content != kiroProceed {
		t.Fatalf("current = %+v", cur)
	}
	// with the tools offered and a stand-in for one used but no longer offered
	if cur.Context == nil || len(cur.Context.Tools) != 2 || cur.Context.Tools[0].Spec.Name != "read" || cur.Context.Tools[1].Spec.Name != "grep" {
		t.Fatalf("tools = %+v", cur.Context)
	}
}

func TestBuildKiroThinkingAndOneMessage(t *testing.T) {
	r := &Request{Effort: "high", Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "why?"}}}}}
	b := buildKiroBody(t, r, true)
	cur := b.ConversationState.CurrentMessage.UserInputMessage
	if len(b.ConversationState.History) != 0 || cur.Content != "<thinking_mode>enabled</thinking_mode><max_thinking_length>30000</max_thinking_length>\n\nwhy?" {
		t.Fatalf("current = %q", cur.Content)
	}
	if !kiroThinks(&Request{Effort: "medium"}, "claude-opus-4.5") || kiroThinks(&Request{Effort: "high"}, "deepseek-3.2") || kiroThinks(&Request{}, "auto") {
		t.Fatal("kiroThinks")
	}
}

func TestKiroFailure(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   int
		msg    string
	}{
		{400, `{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`, 400, "input is too long"},
		{429, `{"message":"You've reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`, 429, "usage limit reached"},
		{500, `{"message":"busy","reason":"INSUFFICIENT_MODEL_CAPACITY"}`, 503, "busy"},
		{400, `plain`, 400, "plain"},
	} {
		if status, msg := kiroFailure(c.status, []byte(c.body)); status != c.want || !strings.Contains(msg, c.msg) {
			t.Errorf("%s: %d %q", c.body, status, msg)
		}
	}
}

// kiroUpstream stands in for Kiro: the first ask with an old token is
// turned down, as an expired one is.
func kiroUpstream(t *testing.T, status int, reply []byte) (*[]string, func()) {
	t.Helper()
	var tokens []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("Authorization")
		tokens = append(tokens, tok)
		var body kiroBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.URL.Path != "/generateAssistantResponse" || body.ProfileArn != "arn:p" {
			http.Error(w, "bad request", 400)
			return
		}
		if tok == "Bearer old" {
			http.Error(w, `{"message":"The bearer token included in the request is invalid."}`, 403)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			w.Write(reply)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Write(reply)
	}))
	auth, runtime := kiroAuth, kiroRuntime
	kiroAuth = func(ctx context.Context, key string, stale bool) (provider.KiroAuth, error) {
		tok := "old"
		if stale {
			tok = "new"
		}
		return provider.KiroAuth{Token: tok, Profile: "arn:p", Region: "us-east-1"}, nil
	}
	kiroRuntime = func(string) string { return up.URL }
	return &tokens, func() { up.Close(); kiroAuth, kiroRuntime = auth, runtime }
}

func serveKiroOnce(t *testing.T, from provider.Protocol, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := New()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	var u Usage
	s.serveKiro(w, r, from, provider.Provider{ID: "kiro"}, "claude-sonnet-4.5", []byte(body), &u)
	return w
}

func TestServeKiro(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	reply := bytes.Join([][]byte{
		kiroEvent("assistantResponseEvent", `{"content":"Hello"}`),
		kiroEvent("toolUseEvent", `{"name":"read","toolUseId":"t1","input":"{\"p\":1}"}`),
		kiroEvent("metadataEvent", `{"tokenUsage":{"uncachedInputTokens":9,"outputTokens":3}}`),
	}, nil)
	tokens, done := kiroUpstream(t, 200, reply)
	defer done()

	// Anthropic, not streamed: the token turned down is refreshed once
	w := serveKiroOnce(t, provider.Anthropic, `{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	var msg struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(w.Body.Bytes(), &msg)
	if w.Code != 200 || len(msg.Content) != 2 || msg.Content[0].Text != "Hello" || msg.Content[1].ID != "t1" || string(msg.Content[1].Input) != `{"p":1}` ||
		msg.StopReason != "tool_use" || msg.Usage.In != 9 || msg.Usage.Out != 3 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Join(*tokens, ",") != "Bearer old,Bearer new" {
		t.Fatalf("tokens = %v", *tokens)
	}

	// OpenAI chat, streamed
	w = serveKiroOnce(t, provider.Chat, `{"model":"x","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out := w.Body.String()
	if w.Code != 200 || !strings.Contains(out, `"content":"Hello"`) || !strings.Contains(out, `"name":"read"`) ||
		!strings.Contains(out, `"finish_reason":"tool_calls"`) || !strings.HasSuffix(strings.TrimSpace(out), "data: [DONE]") {
		t.Fatalf("%d %s", w.Code, out)
	}
}

func TestServeKiroUsageLimit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	_, done := kiroUpstream(t, 402, []byte(`{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`))
	defer done()
	w := serveKiroOnce(t, provider.Anthropic, `{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "usage limit reached") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
