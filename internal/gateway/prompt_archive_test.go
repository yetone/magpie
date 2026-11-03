package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// wangbinyq on Discord: with the request archive on, the context window
// card still said a request's text was gone once it left memory. The
// archive's copy is read instead, masked as the copy in memory is: a phone
// number in it shows as its placeholder, as it would from memory.
func TestArchivedPromptTextIsMasked(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	const phone = "13800138000"
	body := []byte(`{"model":"m","system":"You are the release agent.","messages":[
		{"role":"user","content":"call me at ` + phone + ` once it ships"},
		{"role":"assistant","content":"Shipping."}]}`)
	pt, ok := ArchivedPromptText(provider.Anthropic, body, 7, PartChat, true, 0)
	if !ok || pt.From != "archive" || len(pt.Items) == 0 {
		t.Fatalf("%v %+v", ok, pt)
	}
	b, _ := json.Marshal(pt)
	if strings.Contains(string(b), phone) || !strings.Contains(string(b), "once it ships") {
		t.Fatalf("archived text %s", b)
	}
	sys, ok := ArchivedPromptText(provider.Anthropic, body, 7, PartSystem, true, 0)
	if b, _ := json.Marshal(sys); !ok || !strings.Contains(string(b), "You are the release agent.") {
		t.Fatalf("system %s", b)
	}
	// a body cut short reads as nothing
	if _, ok := ArchivedPromptText(provider.Anthropic, []byte(`{"model":`), 8, PartChat, true, 0); ok {
		t.Fatal("read a body cut short")
	}
}

// The reply beside the prompt: its text, its thinking and its calls, from
// a whole reply or a stream, masked as the prompt's text is.
func TestReplyPiecesAreMasked(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	const phone = "13800138000"
	whole := `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[
		{"type":"thinking","thinking":"The user wants a call."},
		{"type":"text","text":"I will call ` + phone + ` when it ships."},
		{"type":"tool_use","id":"c1","name":"Deploy","input":{"env":"prod","notify":"` + phone + `"}}],
		"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":9}}`
	pieces, cut, ok := ReplyPieces(provider.Anthropic, []byte(whole), false)
	if !ok || cut {
		t.Fatalf("%v %v", ok, cut)
	}
	var roles []string
	for _, p := range pieces {
		roles = append(roles, p.Role+":"+p.Name)
		if strings.Contains(p.Text, phone) {
			t.Fatalf("%s unmasked: %s", p.Role, p.Text)
		}
	}
	if got := strings.Join(roles, " "); got != "thinking: assistant: call:Deploy" {
		t.Fatalf("pieces %s", got)
	}
	if !strings.Contains(pieces[1].Text, "when it ships") || !strings.Contains(pieces[2].Text, `"prod"`) {
		t.Fatalf("%+v", pieces)
	}
	stream := sse(
		"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
		"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Reach me at `+phone+`."}}`,
		"event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":0}`,
		"event: message_stop\ndata: "+`{"type":"message_stop"}`)
	pieces, _, ok = ReplyPieces(provider.Anthropic, []byte(stream), false)
	if !ok || len(pieces) != 1 || pieces[0].Role != "assistant" || strings.Contains(pieces[0].Text, phone) || !strings.Contains(pieces[0].Text, "Reach me at") {
		t.Fatalf("stream %v %+v", ok, pieces)
	}
	if _, _, ok := ReplyPieces(provider.Anthropic, nil, false); ok {
		t.Fatal("read an empty reply")
	}
}
