package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// pluginMarksBody is a non-stream Messages ask of the shape Claude Code's
// auto mode classifier sends a plugin account (#4571): a system block, a
// transcript of history blocks and the current operation, each marked with
// cache_control of the client's own, an hour long. The mark on the last
// history block is the boundary that moves on as the history grows.
const pluginMarksBody = `{"model":"m1","stream":false,"max_tokens":64,
"system":[{"type":"text","text":"fixed system","cache_control":{"type":"ephemeral","ttl":"1h"}}],
"messages":[{"role":"user","content":[
{"type":"text","text":"<transcript>"},
{"type":"text","text":"history one"},
{"type":"text","text":"history two","cache_control":{"type":"ephemeral","ttl":"1h"}},
{"type":"text","text":"current operation","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`

// pluginMessagesReply is an Anthropic upstream's streamed reply.
const pluginMessagesReply = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":13,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}
`

// pluginFake points a plugin account that only streams at a fake upstream,
// the shape #4571 reports: its whole-body Messages asks are translated,
// asked streamed and assembled into one Message JSON for the client.
func pluginFake(t *testing.T) (*fake, *httptest.Server) {
	t.Helper()
	f := &fake{t: t, reply: pluginMessagesReply}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	return f, up
}

func pluginTranslate(t *testing.T, f *fake, up *httptest.Server, body string) (int, string, []byte, []byte) {
	t.Helper()
	s := New()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	p := provider.Provider{ID: "fake", Name: "Factory", Key: "k", Models: []string{"m1"},
		Anthropic: up.URL, Account: &provider.Account{Agent: "plugin", User: "u", Stream: true}}
	var u Usage
	status, msg := s.translate(w, r, p, provider.Anthropic, provider.Anthropic, "m1", []byte(body), &u)
	return status, msg, w.Body.Bytes(), f.got
}

// A translation keeps the client's cache marks where it put them, ttl and
// all (#4571): the boundary it marked last turn stands at the same block
// again, so a turn whose history only grew reads that write instead of
// writing the whole history again, for the hours it asked for.
func TestPluginWholeAskedKeepsTheClientCacheMarks(t *testing.T) {
	f, up := pluginFake(t)
	code, msg, answered, asked := pluginTranslate(t, f, up, pluginMarksBody)
	if code != 200 {
		t.Fatalf("%d: %s", code, msg)
	}
	// the client asked for one JSON body and is told one
	var out struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(answered, &out); err != nil || out.Type != "message" ||
		len(out.Content) != 1 || out.Content[0].Text != "ok" {
		t.Fatalf("answer %s (%v)", answered, err)
	}
	// the upstream is asked streamed, whatever the client asked for
	if !bytes.Contains(asked, []byte(`"stream":true`)) {
		t.Fatalf("asked %s", asked)
	}
	var wire struct {
		System   []map[string]any `json:"system"`
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(asked, &wire); err != nil {
		t.Fatalf("asked %s (%v)", asked, err)
	}
	mark := map[string]any{"type": "ephemeral", "ttl": "1h"}
	if len(wire.System) != 1 || wire.System[0]["text"] != "fixed system" {
		t.Fatalf("system %v, want the client's block", wire.System)
	}
	// the client's blocks are the asked ones, in the order it sent them
	if len(wire.Messages) != 1 || len(wire.Messages[0].Content) != 4 {
		t.Fatalf("messages %v, want the client's four blocks", wire.Messages)
	}
	wantTexts := []string{"<transcript>", "history one", "history two", "current operation"}
	for i, want := range wantTexts {
		if wire.Messages[0].Content[i]["text"] != want {
			t.Errorf("block %d is %v, want %q", i, wire.Messages[0].Content[i], want)
		}
	}
	for _, i := range []int{0, 1} {
		if cc := wire.Messages[0].Content[i]["cache_control"]; cc != nil {
			t.Errorf("block %d the client left unmarked is marked %v", i, cc)
		}
	}
	if cc := wire.Messages[0].Content[2]["cache_control"]; !sameMark(cc, mark) {
		t.Errorf("the history boundary is marked %v, want %v: %s", cc, mark, asked)
	}
	if cc := wire.Messages[0].Content[3]["cache_control"]; !sameMark(cc, mark) {
		t.Errorf("the tail is marked %v, want %v", cc, mark)
	}
	if cc := wire.System[0]["cache_control"]; !sameMark(cc, mark) {
		t.Errorf("the system is marked %v, want %v", cc, mark)
	}
}

func sameMark(got any, want map[string]any) bool {
	m, ok := got.(map[string]any)
	if !ok {
		return false
	}
	return m["type"] == want["type"] && m["ttl"] == want["ttl"]
}

// A request that only mentions cache_control in its text marks nothing of
// its own: the rebuild marks it the way it marked requests before
// (#4571), rather than reading a mark set out of words.
func TestPluginWholeAskedMentioningMarksIsMarkedAsBefore(t *testing.T) {
	body := `{"model":"m1","stream":false,"max_tokens":64,
"system":[{"type":"text","text":"fixed system"}],
"messages":[{"role":"user","content":[
{"type":"text","text":"what does cache_control do?"}]}]}`
	f, up := pluginFake(t)
	code, msg, _, asked := pluginTranslate(t, f, up, body)
	if code != 200 {
		t.Fatalf("%d: %s", code, msg)
	}
	var wire struct {
		System   []map[string]any `json:"system"`
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(asked, &wire); err != nil {
		t.Fatalf("asked %s (%v)", asked, err)
	}
	last := wire.Messages[len(wire.Messages)-1].Content
	if cc, ok := last[len(last)-1]["cache_control"].(map[string]any); !ok || cc["ttl"] != nil {
		t.Errorf("the tail is %v, want magpie's five-minute mark", last[len(last)-1]["cache_control"])
	}
	if len(wire.System) != 1 {
		t.Fatalf("system %v", wire.System)
	}
	if cc, ok := wire.System[0]["cache_control"].(map[string]any); !ok || cc["ttl"] != nil {
		t.Errorf("the system is %v, want magpie's five-minute mark", wire.System[0]["cache_control"])
	}
}

// A set of marks no Messages request may carry (an hour after five
// minutes) goes the default way, its marks dropped as before, not refused
// with a 400 the old translation never gave (#3835).
func TestPluginWholeAskedServesBadMarksAsBefore(t *testing.T) {
	bad := bytes.Replace([]byte(pluginMarksBody),
		[]byte(`"system":[{"type":"text","text":"fixed system","cache_control":{"type":"ephemeral","ttl":"1h"}}]`),
		[]byte(`"system":[{"type":"text","text":"fixed system","cache_control":{"type":"ephemeral","ttl":"5m"}}]`), 1)
	if !bytes.Contains(bad, []byte(`"ttl":"5m"`)) {
		t.Fatal("fixture: no system mark turned to 5m")
	}
	f, up := pluginFake(t)
	code, msg, _, asked := pluginTranslate(t, f, up, string(bad))
	if code != 200 {
		t.Fatalf("%d: %s", code, msg)
	}
	var wire struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(asked, &wire); err != nil {
		t.Fatalf("asked %s (%v)", asked, err)
	}
	last := wire.Messages[len(wire.Messages)-1].Content
	for i, b := range last {
		if m, ok := b["cache_control"].(map[string]any); ok && m["ttl"] == "1h" {
			t.Errorf("block %d keeps a mark of a set the checks refuse: %v", i, m)
		}
	}
}

// A Messages ask read with its client's marks (parseTranslated) is
// finished like any other parse: a call id that carries a Gemini
// thought signature, from a turn a Gemini model answered, goes to the
// Anthropic upstream as the call's own id, its result answering it.
func TestTranslatedWithMarksTakesSignaturesOffCallIDs(t *testing.T) {
	id := signedID("toolu_1", "c2lnbmF0dXJl")
	if id == "toolu_1" {
		t.Fatal("signedID left the id unsigned")
	}
	q, _ := json.Marshal(id)
	body := []byte(`{"model":"m1","max_tokens":64,"messages":[
{"role":"user","content":[{"type":"text","text":"go"}]},
{"role":"assistant","content":[{"type":"tool_use","id":` + string(q) + `,"name":"Bash","input":{}}]},
{"role":"user","content":[{"type":"tool_result","tool_use_id":` + string(q) + `,"content":"ok","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
	req, err := parseTranslated(provider.Anthropic, provider.Anthropic, body, false)
	if err != nil {
		t.Fatal(err)
	}
	if req.clientCache == nil || req.clientCache.tail != "1h" {
		t.Fatalf("not read with its marks: %+v", req.clientCache)
	}
	call, result := req.Messages[1].Parts[0], req.Messages[2].Parts[0]
	if call.ID != "toolu_1" || result.CallID != "toolu_1" {
		t.Fatalf("call %q, result %q: want toolu_1 for both", call.ID, result.CallID)
	}
}
