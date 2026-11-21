package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A relay that lists a model once per effort, the effort in the id
// (elis_pina on X), is offered the model once; the gateway asks it for
// the id of the effort the request goes at, on every API a client
// speaks, and an id a client kept from before goes as it is.
func TestRelayEffortFamilySendsTheEffortsID(t *testing.T) {
	s, up := fasted(t)
	var ms []catalog.Model
	for _, id := range []string{"gemini-4-argon-high", "gemini-4-argon-low", "gemini-4-argon-medium", "gemini-4-hydrogen-low", "gemini-4-hydrogen-xhigh", "kimi-k2.7-code-highspeed"} {
		ms = append(ms, catalog.Model{ID: id})
	}
	if err := catalog.SaveLive("rl", "https://relay.example/v1", ms); err != nil {
		t.Fatal(err)
	}
	chat := func(model, extra string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]` + extra + `}`
	}
	for _, c := range []struct{ name, path, body, want string }{
		{"chat, none asked", "/v1/chat/completions", chat("rl/gemini-4-argon", ""), "gemini-4-argon-medium"},
		{"chat, low", "/v1/chat/completions", chat("rl/gemini-4-argon", `,"reasoning_effort":"low"`), "gemini-4-argon-low"},
		{"chat, high, streamed", "/v1/chat/completions", chat("rl/gemini-4-argon", `,"reasoning_effort":"high","stream":true`), "gemini-4-argon-high"},
		{"chat, xhigh on a family to high", "/v1/chat/completions", chat("rl/gemini-4-argon", `,"reasoning_effort":"xhigh"`), "gemini-4-argon-high"},
		{"chat, none asked, no medium", "/v1/chat/completions", chat("rl/gemini-4-hydrogen", ""), "gemini-4-hydrogen-low"},
		{"responses, low", "/v1/responses", `{"model":"rl/gemini-4-argon","input":"hi","reasoning":{"effort":"low"}}`, "gemini-4-argon-low"},
		{"responses, none asked", "/v1/responses", `{"model":"rl/gemini-4-argon","input":"hi"}`, "gemini-4-argon-medium"},
		{"messages, thinking", "/v1/messages", `{"model":"rl/gemini-4-argon","max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":30000},"messages":[{"role":"user","content":"hi"}]}`, "gemini-4-argon-high"},
		{"messages, plain", "/v1/messages", `{"model":"rl/gemini-4-argon","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, "gemini-4-argon-medium"},
		// an id kept from before goes as it is, at any effort
		{"old id, low asked", "/v1/chat/completions", chat("rl/gemini-4-argon-high", `,"reasoning_effort":"low"`), "gemini-4-argon-high"},
		{"old id, responses", "/v1/responses", `{"model":"rl/gemini-4-argon-low","input":"hi","reasoning":{"effort":"high"}}`, "gemini-4-argon-low"},
		{"not an effort word", "/v1/chat/completions", chat("rl/kimi-k2.7-code-highspeed", `,"reasoning_effort":"low"`), "kimi-k2.7-code-highspeed"},
	} {
		rt := postFrom(t, s, "opencode", c.path, c.body)
		if host, _, _, body := up.last(t); host != "relay.example" || body["model"] != c.want {
			t.Errorf("%s: sent %v to %s, want %s", c.name, body["model"], host, c.want)
		}
		// the call's record says the id it went out under
		if len(rt.Tries) == 0 {
			t.Fatalf("%s: no try recorded", c.name)
		}
		want := c.want
		if last := rt.Tries[len(rt.Tries)-1]; want == last.Model {
			want = ""
		}
		if w := rt.Tries[len(rt.Tries)-1].Wire; w != want {
			t.Errorf("%s: recorded wire %q, want %q", c.name, w, want)
		}
	}
}
