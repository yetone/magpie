package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A reply on /v1/messages has an Anthropic id, msg_…, whoever served it: a
// plugin's provider (OpenAI-shaped, chatcmpl-…) as a built-in does.
func TestAnthropicReplyID(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"chatcmpl-741847094e3edb3348a12c45", "msg_741847094e3edb3348a12c45"},
		{"msg_01abc", "msg_01abc"},
		{"c", "msg_c"},
	} {
		var out struct {
			ID string `json:"id"`
		}
		json.Unmarshal(renderAnthropic(Result{ID: c.in}, "m"), &out)
		if out.ID != c.want {
			t.Fatalf("%q replied as %q, want %q", c.in, out.ID, c.want)
		}
		rec := httptest.NewRecorder()
		e := &anthropicEncoder{w: newSSEWriter(rec), model: "m"}
		e.event(Event{Kind: KStart, MsgID: c.in, Model: "m"})
		e.finish()
		if !strings.Contains(rec.Body.String(), `"id":"`+c.want+`"`) {
			t.Fatalf("%q streamed as %s", c.in, rec.Body.String())
		}
	}
	var out struct {
		ID string `json:"id"`
	}
	json.Unmarshal(renderAnthropic(Result{}, "m"), &out)
	if !strings.HasPrefix(out.ID, "msg_") {
		t.Fatalf("no id replied as %q", out.ID)
	}
}
