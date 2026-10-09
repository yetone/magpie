package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// devinRawID is the call id SWE-2 gave in #1304 (StarMoonCity's capture).
// Claude Code takes a tool_use id only of [A-Za-z0-9_-] and dropped the
// call: "The model's tool call could not be parsed (retry also failed)".
const devinRawID = "Bash:0#a65b6a5e02194b87bbc796c4428c946d"

var anthropicSafeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// devinCallReply is Devin's reply making that call.
func devinCallReply() []byte {
	return bytes.Join([][]byte{
		devinFrame(0, pb{}.bytes(6, pb{}.str(1, devinRawID).str(2, "Bash").str(3, `{"command":"date"}`)), false),
		devinFrame(0, pb{}.varint(5, 10), false),
		devinFrame(2, []byte("{}"), false),
	}, nil)
}

// devinIDsSent is the ids of the calls and of the results the request for r
// gives Devin.
func devinIDsSent(t *testing.T, r *Request) (calls, answers []string) {
	t.Helper()
	for _, m := range decodeDevinRequest(t, buildDevin(r, "swe-2-high", "k")).msgs {
		for _, f := range m {
			switch f.num {
			case 6:
				calls = append(calls, string(pbFields(f.data)[0].data))
			case 7:
				answers = append(answers, string(f.data))
			}
		}
	}
	return
}

// On each protocol the call's id reaches the agent of [A-Za-z0-9_-] only,
// and the agent's next request, answering it under that id, gives Devin
// the call and its result under the id Devin gave.
func TestDevinCallIDsSafeBothWays(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	done := devinUpstream(t, devinCallReply())
	defer done()

	for _, c := range []struct {
		from  provider.Protocol
		ask   string
		id    func(body []byte) string
		next  func(id string) string
		parse func([]byte) (*Request, error)
	}{
		{provider.Anthropic, `{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"用 Bash 跑一下 date"}]}`,
			func(b []byte) string {
				var m struct{ Content []struct{ Type, ID string } }
				json.Unmarshal(b, &m)
				for _, p := range m.Content {
					if p.Type == "tool_use" {
						return p.ID
					}
				}
				return ""
			},
			func(id string) string {
				return `{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"用 Bash 跑一下 date"},
 {"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":"date"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"Thu Oct  8 10:00:00 CST 2026"}]}]}`
			}, parseAnthropic},
		{provider.Chat, `{"model":"x","messages":[{"role":"user","content":"用 Bash 跑一下 date"}]}`,
			func(b []byte) string {
				var m struct {
					Choices []struct {
						Message struct {
							ToolCalls []struct{ ID string } `json:"tool_calls"`
						}
					}
				}
				json.Unmarshal(b, &m)
				if len(m.Choices) == 0 || len(m.Choices[0].Message.ToolCalls) == 0 {
					return ""
				}
				return m.Choices[0].Message.ToolCalls[0].ID
			},
			func(id string) string {
				return `{"model":"x","messages":[{"role":"user","content":"用 Bash 跑一下 date"},
 {"role":"assistant","content":null,"tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"date\"}"}}]},
 {"role":"tool","tool_call_id":"` + id + `","content":"Thu Oct  8 10:00:00 CST 2026"}]}`
			}, parseChat},
		{provider.Responses, `{"model":"x","input":"用 Bash 跑一下 date"}`,
			func(b []byte) string {
				var m struct {
					Output []struct {
						Type   string
						CallID string `json:"call_id"`
					}
				}
				json.Unmarshal(b, &m)
				for _, o := range m.Output {
					if o.Type == "function_call" {
						return o.CallID
					}
				}
				return ""
			},
			func(id string) string {
				return `{"model":"x","input":[{"role":"user","content":"用 Bash 跑一下 date"},
 {"type":"function_call","call_id":"` + id + `","name":"Bash","arguments":"{\"command\":\"date\"}"},
 {"type":"function_call_output","call_id":"` + id + `","output":"Thu Oct  8 10:00:00 CST 2026"}]}`
			}, parseResponses},
	} {
		w := serveDevinOnce(t, c.from, c.ask)
		id := c.id(w.Body.Bytes())
		if w.Code != 200 || !anthropicSafeID.MatchString(id) {
			t.Fatalf("%s: call id %q, want one of [A-Za-z0-9_-] only (%d %s)", c.from, id, w.Code, w.Body)
		}
		r, err := c.parse([]byte(c.next(id)))
		if err != nil {
			t.Fatalf("%s: %v", c.from, err)
		}
		calls, answers := devinIDsSent(t, r)
		if strings.Join(calls, ",") != devinRawID || strings.Join(answers, ",") != devinRawID {
			t.Fatalf("%s: Devin got calls %q, results for %q, want both %q", c.from, calls, answers, devinRawID)
		}
	}

	// streamed to Claude Code too
	w := serveDevinOnce(t, provider.Anthropic, `{"model":"x","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if out := w.Body.String(); strings.Contains(out, devinRawID) || !strings.Contains(out, `"id":"`+devinOutID(devinRawID)+`"`) {
		t.Fatalf("streamed: %s", out)
	}
}

// An id that is already safe goes both ways as it came; one of the
// caller's own that begins dv_ (no id devinOutID makes) does too.
func TestDevinSafeCallIDsUnchanged(t *testing.T) {
	for _, id := range []string{"toolu_01AbC", "call_abc-123", "t1", "dv_", "dv_abc"} {
		if got := devinInID(devinOutID(id)); got != id {
			t.Errorf("%q round-trips to %q", id, got)
		}
		if got := devinInID(id); got != id {
			t.Errorf("%q from the agent reaches Devin as %q", id, got)
		}
	}
	for _, id := range []string{"toolu_01AbC", "call_abc-123"} {
		if got := devinOutID(id); got != id {
			t.Errorf("%q goes out as %q", id, got)
		}
	}
}
