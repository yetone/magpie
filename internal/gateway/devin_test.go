package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// devinFrame is one frame of a Connect stream, gzipped when zip.
func devinFrame(flag byte, msg []byte, zip bool) []byte {
	if zip {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write(msg)
		zw.Close()
		msg, flag = b.Bytes(), flag|1
	}
	out := []byte{flag, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(len(msg)))
	return append(out, msg...)
}

func devinEvents(t *testing.T, frames ...[]byte) []Event {
	t.Helper()
	br := bufio.NewReader(bytes.NewReader(bytes.Join(frames, nil)))
	first, err := readConnectFrame(br)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan Event, 256)
	decodeDevin(context.Background(), first, br, out, "swe-2")
	var evs []Event
	for ev := range out {
		evs = append(evs, ev)
	}
	return evs
}

func TestDevinDecodesTextThinkingAndCalls(t *testing.T) {
	evs := devinEvents(t,
		devinFrame(0, pb{}.str(1, "bot-1").str(9, "Hmm."), false),
		devinFrame(0, pb{}.str(3, "Let me ").varint(4, 1), true),
		devinFrame(0, pb{}.str(3, "look."), false),
		devinFrame(0, pb{}.bytes(6, pb{}.str(1, "toolu_1").str(2, "read")), false),
		devinFrame(0, pb{}.bytes(6, pb{}.str(3, `{"pa`)), false),
		devinFrame(0, pb{}.bytes(6, pb{}.str(3, `th":"a"}`)), false),
		devinFrame(0, pb{}.str(10, "sealed.v1xyz").varint(5, 10), false),
		devinFrame(0, pb{}.bytes(7, pb{}.varint(2, 120).varint(3, 30).varint(5, 7).varint(6, 30).str(7, "id")), false),
		devinFrame(2, []byte("{}"), false),
	)
	if got, want := said(evs), `|think:Hmm.|text:Let me look.|call:toolu_1/read|args:{"path":"a"}|stop:tool`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
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

func TestDevinStopsAndFails(t *testing.T) {
	evs := devinEvents(t, devinFrame(0, pb{}.str(3, "abcd").varint(5, 3), false))
	if got := said(evs); got != "|text:abcd|stop:length" {
		t.Fatal(got)
	}
	evs = devinEvents(t,
		devinFrame(0, pb{}.str(3, "ab"), false),
		devinFrame(2, []byte(`{"error":{"code":"internal","message":"boom"}}`), false))
	if got := said(evs); got != "|text:ab|error:boom" {
		t.Fatal(got)
	}
}

// devinDecoded is a GetChatMessage request, read back.
type devinDecoded struct {
	system string
	model  string
	msgs   [][]pbField
	tools  []string
	descs  []string
	// schemas are the tools' parameters, as sent
	schemas []string
	max     uint64
}

func decodeDevinRequest(t *testing.T, b []byte) devinDecoded {
	t.Helper()
	var d devinDecoded
	for _, f := range pbFields(b) {
		switch f.num {
		case 2:
			d.system = string(f.data)
		case 21:
			d.model = string(f.data)
		case 3:
			d.msgs = append(d.msgs, pbFields(f.data))
		case 10:
			fs := pbFields(f.data)
			d.tools = append(d.tools, string(fs[0].data))
			d.descs = append(d.descs, string(fs[1].data))
			d.schemas = append(d.schemas, string(fs[2].data))
		case 8:
			for _, g := range pbFields(f.data) {
				if g.num == 2 {
					d.max = g.n
				}
			}
		}
	}
	return d
}

// devinSummary is a message as role:text[extras].
func devinSummary(m []pbField) string {
	var role uint64
	var text, extra string
	for _, f := range m {
		switch f.num {
		case 2:
			role = f.n
		case 3:
			text = string(f.data)
		case 6:
			fs := pbFields(f.data)
			extra += " call " + string(fs[0].data) + "/" + string(fs[1].data) + " " + string(fs[2].data)
		case 7:
			extra += " for " + string(f.data)
		case 10:
			extra += " image " + string(pbFields(f.data)[1].data)
		case 12:
			extra += " signed"
		}
	}
	return string(rune('0'+role)) + ":" + text + extra
}

func TestBuildDevin(t *testing.T) {
	r := &Request{
		System: "Be brief.",
		Tools:  []Tool{{Name: "read", Description: "Read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{
			{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "orphan", Text: "x"}, {Kind: Text, Text: "look at a"}}},
			{Role: "assistant", Parts: []Part{
				{Kind: Thinking, Text: "ok", Signature: "sealed.v1abc"},
				{Kind: Thinking, Text: "other", Signature: "EqQBCkY"},
				{Kind: Text, Text: "Reading."},
				{Kind: ToolCall, ID: "c1", Name: "read", Args: json.RawMessage(`{"p":"a"}`)},
				{Kind: ToolCall, ID: "c2", Name: "gone", Args: nil},
			}},
			{Role: "user", Parts: []Part{
				{Kind: ToolResult, CallID: "c1", Text: "A", IsError: true},
				{Kind: ToolResult, CallID: "c1", Text: "again"},
				{Kind: Image, MediaType: "image/jpeg", Data: "AAAA"},
				{Kind: Text, Text: "and this"},
			}},
		},
	}
	d := decodeDevinRequest(t, buildDevin(r, "swe-2-high", "k"))
	if d.system != "" || d.model != "swe-2-high" || d.max != 128000 {
		t.Fatalf("%+v", d)
	}
	var got []string
	for _, m := range d.msgs {
		got = append(got, devinSummary(m))
	}
	want := []string{
		"1:Be brief.\n\n<tool_descriptions>\n<tool name=\"read\">\nRead a file\n</tool>\n</tool_descriptions>\n\nlook at a",
		`2:Reading. call c1/read {"p":"a"} call c2/gone {} signed`,
		"4:Error: A for c1",
		"4:" + devinNoResult + " for c2",
		"1:and this image image/jpeg",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Join(d.tools, ",") != "read,gone" {
		t.Fatalf("tools = %v", d.tools)
	}
	// a tool is sent with a pointer to its description, which is in the
	// instructions: Devin answers "an internal error occurred" to some
	// agents' tools as they describe themselves (silenx on X: WorkBuddy
	// on Devin, 502 every time)
	if d.descs[0] != `Described under <tool name="read"> in <tool_descriptions>, in the instructions.` || d.descs[1] != "Tool" {
		t.Fatalf("descriptions = %q", d.descs)
	}

	// none to be called: no tools, nor their descriptions
	d = decodeDevinRequest(t, buildDevin(&Request{ToolChoice: "none", Tools: r.Tools, Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
	}}, "m", "k"))
	if len(d.tools) != 0 || devinSummary(d.msgs[0]) != "1:hi" {
		t.Fatalf("%+v", d)
	}

	// a conversation ending on a reply goes on with a nudge
	d = decodeDevinRequest(t, buildDevin(&Request{MaxTokens: 50, Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "yo"}}},
	}}, "m", "k"))
	if n := len(d.msgs); n != 3 || devinSummary(d.msgs[2]) != "1:Please proceed with the task." || d.max != 50 {
		t.Fatalf("%d %+v", n, d)
	}
}

// TestBuildDevinObjectRoot offers #1196's tool, whose parameters have a root
// oneOf, anyOf or allOf: Devin's Claude models answered 502 to every request
// with one, so each goes as a plain object; a plain object goes as it came.
func TestBuildDevinObjectRoot(t *testing.T) {
	for _, k := range []string{"oneOf", "anyOf", "allOf"} {
		schema := `{
      "type": "object",
      "properties": {"a": {"type": "string"}},
      "` + k + `": [{"properties": {"a": {"type": "string"}}}, {"properties": {"b": {"type": "string"}}}]
    }`
		plain := `{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`
		d := decodeDevinRequest(t, buildDevin(&Request{
			Tools: []Tool{
				{Name: "t", Description: "t", Schema: json.RawMessage(schema)},
				{Name: "plain", Description: "p", Schema: json.RawMessage(plain)},
			},
			Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "ok"}}}},
		}, "claude-opus-5-5", "k"))
		if len(d.schemas) != 2 {
			t.Fatalf("%s: schemas %q", k, d.schemas)
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(d.schemas[0]), &p); err != nil {
			t.Fatal(err)
		}
		props, _ := p["properties"].(map[string]any)
		if p["type"] != "object" || p["anyOf"] != nil || p["oneOf"] != nil || p["allOf"] != nil || props["a"] == nil || props["b"] == nil {
			t.Fatalf("%s: sent %s", k, d.schemas[0])
		}
		if d.schemas[1] != plain {
			t.Fatalf("%s: a plain object was changed: %s", k, d.schemas[1])
		}
	}
}

func TestBuildDevinKeepsImagesAToolReturned(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "look at it"}}},
		{Role: "assistant", Parts: []Part{
			{Kind: ToolCall, ID: "c1", Name: "read", Args: json.RawMessage(`{"p":"a.png"}`)},
			{Kind: ToolCall, ID: "c2", Name: "read", Args: json.RawMessage(`{"p":"b.png"}`)},
		}},
		{Role: "user", Parts: []Part{
			// Read gives a picture back with no text beside it
			{Kind: ToolResult, CallID: "c1", Images: []Part{
				{Kind: Image, MediaType: "image/png", Data: "AAAA"},
				{Kind: Image, URL: "https://example.com/x.png"}, // Devin takes no URL
			}},
			{Kind: ToolResult, CallID: "c2", Text: "B", Images: []Part{{Kind: Image, MediaType: "image/jpeg", Data: "BBBB"}}},
		}},
	}}
	d := decodeDevinRequest(t, buildDevin(r, "swe-2", "k"))
	var got []string
	for _, m := range d.msgs {
		got = append(got, devinSummary(m))
	}
	want := []string{
		"1:look at it",
		"2: call c1/read {\"p\":\"a.png\"} call c2/read {\"p\":\"b.png\"}",
		"4:(no output) for c1 image image/png",
		"4:B for c2 image image/jpeg",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func devinUpstream(t *testing.T, reply []byte) func() {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/exa.api_server_pb.ApiServerService/GetChatMessage" || r.Header.Get("Authorization") != "Basic devin-session-token$k" ||
			r.Header.Get("Content-Type") != "application/connect+proto" || len(body) < 5 {
			http.Error(w, `{"code":"unauthenticated","message":"no"}`, 401)
			return
		}
		if m := decodeDevinRequest(t, body[5:]).model; m != "swe-2-high" {
			t.Errorf("model = %q", m)
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.Write(reply)
	}))
	auth, variant := devinAuth, devinVariant
	devinAuth = func(string) (string, string, error) { return "devin-session-token$k", up.URL, nil }
	devinVariant = func(ctx context.Context, model, effort string) string { return model + "-high" }
	return func() { up.Close(); devinAuth, devinVariant = auth, variant }
}

func serveDevinOnce(t *testing.T, from provider.Protocol, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := New()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	var u Usage
	s.serveDevin(w, r, from, "", "swe-2", []byte(body), &u)
	return w
}

func TestServeDevin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	done := devinUpstream(t, bytes.Join([][]byte{
		devinFrame(0, pb{}.str(3, "Hello"), false),
		devinFrame(0, pb{}.bytes(6, pb{}.str(1, "t1").str(2, "read").str(3, `{"p":1}`)), false),
		devinFrame(0, pb{}.varint(5, 10).bytes(7, pb{}.varint(2, 9).varint(3, 3)), false),
		devinFrame(2, []byte("{}"), false),
	}, nil))
	defer done()

	w := serveDevinOnce(t, provider.Anthropic, `{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	var msg struct {
		Content []struct {
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

	w = serveDevinOnce(t, provider.Chat, `{"model":"x","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out := w.Body.String()
	if w.Code != 200 || !strings.Contains(out, `"content":"Hello"`) || !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("%d %s", w.Code, out)
	}
}

// An error at the stream's head is answered with its own status.
func TestServeDevinFailures(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, c := range []struct {
		end    string
		status int
	}{
		{`{"error":{"code":"resource_exhausted","message":"out of credits"}}`, 429},
		{`{"error":{"code":"permission_denied","message":"internal error"}}`, 502},
		{`{"error":{"code":"unauthenticated","message":"expired"}}`, 401},
	} {
		done := devinUpstream(t, devinFrame(2, []byte(c.end), false))
		w := serveDevinOnce(t, provider.Anthropic, `{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
		done()
		if w.Code != c.status {
			t.Errorf("%s: %d %s", c.end, w.Code, w.Body)
		}
	}
}
