package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// writtenBash is Claude's own call form, written as text in a reply whose
// review Anthropic answered.
const writtenBash = `<invoke name="Bash"><parameter name="command">echo hi</parameter></invoke>`

// reviewedTextReply is a streamed reply whose text holds writtenBash and
// whose message_delta carries the review's results, or none when review
// is "".
func reviewedTextReply(review string) string {
	delta := `{"stop_reason":"end_turn","stop_sequence":null}`
	if review != "" {
		delta = `{"stop_reason":"end_turn","stop_sequence":null,"safeguard_results":` + review + `}`
	}
	text, _ := json.Marshal("I'd run it like this: " + writtenBash)
	return sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(text)+`}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":`+delta+`,"usage":{"output_tokens":20}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
}

// A reply Anthropic reviewed for Claude Code's auto mode reaches it as it
// came: no call is made out of its text, whose id the review never
// judged and Claude Code could match to no verdict (xybio on Discord:
// "this session isn't eligible because your requests go through
// 127.0.0.1:3425"), and its stop_reason and safeguard_results are as they
// were. The same reply without results still has its written call made
// (packinggoodsllc_07668, textcalls_anthropic.go).
func TestAutoModeReviewedReplyMakesNoCall(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{t: t}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "ant", Name: "Ant", Key: "k", Anthropic: up.URL, Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}

	f.reply, f.ctype = reviewedTextReply(reviewed), ""
	rec := askReview(t, New(), "/v1/messages?beta=true", reviewAsk("ant/claude-sonnet-5"))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	raw := rec.Body.String()
	text, names, _, stop := anthropicRead(t, raw)
	if got := reviewIn(t, raw); !sameReview([]byte(got), []byte(reviewed)) || len(names) != 0 || stop != "end_turn" || text != "I'd run it like this: "+writtenBash {
		t.Errorf("reviewed stream changed: results %s names %v stop %q text %q\n%s", got, names, stop, text, raw)
	}

	// whole
	whole, _ := json.Marshal(map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
		"content":     []any{map[string]any{"type": "text", "text": "I'd run it like this: " + writtenBash}},
		"stop_reason": "end_turn", "safeguard_results": json.RawMessage(reviewed), "usage": map[string]any{"input_tokens": 7, "output_tokens": 20}})
	f.reply, f.ctype = string(whole), "application/json"
	rec = askReview(t, New(), "/v1/messages?beta=true", strings.Replace(reviewAsk("ant/claude-sonnet-5"), `"stream":true`, `"stream":false`, 1))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
		StopReason string          `json:"stop_reason"`
		Results    json.RawMessage `json:"safeguard_results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if len(got.Content) != 1 || got.Content[0].Type != "text" || got.StopReason != "end_turn" || !sameReview(got.Results, []byte(reviewed)) {
		t.Errorf("reviewed whole reply changed: %s", rec.Body)
	}

	// no review: the written call is made, as for any relayed reply
	f.reply, f.ctype = reviewedTextReply(""), ""
	rec = askReview(t, New(), "/v1/messages?beta=true", reviewAsk("ant/claude-sonnet-5"))
	if raw := rec.Body.String(); !strings.Contains(raw, `"tool_use"`) || !strings.Contains(raw, `"stop_reason":"tool_use"`) {
		t.Errorf("unreviewed stream had no call made:\n%s", raw)
	}
}
