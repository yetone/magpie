package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// jsq is s as the JSON string a request's content carries it as.
func jsq(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A completion call Antigravity's gemini-4-argon wrote out in its reply's
// text, and the answer it repeated after it, is history twice over once the
// client sends the turn back: shown its own wrapper, the model writes its
// next calls out as text the same way, turn after turn, and the client
// reads the call's arguments twice and sees no tool (#5091). The turn goes
// to Antigravity without the written-out call — of a tool the request
// offers, whole, closed, its result said again in the text after it — and
// with the rest of the turn as it came. A turn with a real call in it, a
// wrapper of a tool not offered, a fragment that is no call, a result the
// answer doesn't repeat, a user's turn and another agent's request all
// stay as they are.
func TestAntigravityPseudoCallOutOfHistory(t *testing.T) {
	const completion = "12 checks green, build ready"
	wrapper := `<tool_call>{"name":"attempt_completion","arguments":{"result":"` + completion + `"}}</tool_call>`
	pseudo := "I'll wrap up now.\n" + wrapper + "\nAll done: " + completion
	want := "I'll wrap up now.\n\nAll done: " + completion
	const tools = `[{"name":"attempt_completion","input_schema":{"type":"object","properties":{"result":{"type":"string"}}}},
		{"name":"bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]`

	// modelParts builds a request whose assistant turn holds content, and
	// gives the model turn the envelope holds, "call" for each call
	modelParts := func(t *testing.T, agent, content string) []string {
		t.Helper()
		body := `{"model":"gemini-4-argon-high","max_tokens":100,"tools":` + tools +
			`,"messages":[{"role":"user","content":"run the checks"},
			{"role":"assistant","content":` + content + `},
			{"role":"user","content":"go on"}]}`
		r, err := parseAnthropic([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Request struct {
				Contents []struct {
					Role  string                       `json:"role"`
					Parts []map[string]json.RawMessage `json:"parts"`
				} `json:"contents"`
			} `json:"request"`
		}
		if err := json.Unmarshal(buildCodeAssist(r, "gemini-4-argon-high", agent), &env); err != nil {
			t.Fatal(err)
		}
		for _, c := range env.Request.Contents {
			if c.Role != "model" {
				continue
			}
			var out []string
			for _, p := range c.Parts {
				if _, ok := p["functionCall"]; ok {
					out = append(out, "call")
					continue
				}
				var s string
				if err := json.Unmarshal(p["text"], &s); err != nil {
					t.Fatalf("unreadable part %q: %v", string(p["text"]), err)
				}
				out = append(out, s)
			}
			return out
		}
		t.Fatalf("no model turn in the envelope for %s", agent)
		return nil
	}

	if got := modelParts(t, "antigravity", `[{"type":"text","text":`+jsq(pseudo)+`}]`); len(got) != 1 || got[0] != want {
		t.Errorf("the pseudo call stayed in the turn sent to Antigravity: %q, want %q", got, want)
	}
	// the same turn split over two text blocks, the wrapper cut across them,
	// goes as one answer too
	split := `[{"type":"text","text":` + jsq("I'll wrap up now.\n<tool_call>{\"name\":\"attempt") +
		`},{"type":"text","text":` + jsq("_completion\",\"arguments\":{\"result\":\""+completion+"\"}}</tool_call>\nAll done: "+completion) + `}]`
	if got := modelParts(t, "antigravity", split); len(got) != 1 || got[0] != want {
		t.Errorf("the pseudo call cut across two blocks stayed: %q, want %q", got, want)
	}
	// the same wrapper in an invoke of Claude's own shape, with its thinking
	// beside it, goes the way too: what the client sends back of a thought
	// is not what goes upstream
	invoked := `[{"type":"thinking","thinking":"the work is done","signature":"sig"},` +
		`{"type":"text","text":` + jsq(`<invoke name="attempt_completion">
<parameter name="result">`+completion+`</parameter>
</invoke>
All done: `+completion) + `}]`
	if got := modelParts(t, "antigravity", invoked); len(got) != 1 || got[0] != "All done: "+completion {
		t.Errorf("a written-out invoke stayed: %q", got)
	}
	// a turn that made a real call stands, wrapper text and all
	real := `[{"type":"tool_use","id":"t1","name":"attempt_completion","input":{"result":"` + completion + `"}},` +
		`{"type":"text","text":` + jsq(pseudo) + `}]`
	if got := modelParts(t, "antigravity", real); len(got) != 2 || got[0] != "call" || got[1] != pseudo {
		t.Errorf("a turn with a real call was rewritten: %q", got)
	}

	stays := []struct {
		name     string
		agent    string
		content  string
		contains string
	}{
		{"a wrapper of a tool not offered", "antigravity",
			`[{"type":"text","text":` + jsq("Done.\n"+strings.Replace(wrapper, "attempt_completion", "hidden_tool", 1)+"\nDone: "+completion) + `}]`,
			"hidden_tool"},
		{"no result in the arguments", "antigravity",
			`[{"type":"text","text":` + jsq(`<tool_call>{"name":"bash","arguments":{"command":"ls"}}</tool_call>`+"\nls is done") + `}]`,
			"<tool_call>"},
		{"a result the answer doesn't repeat", "antigravity",
			`[{"type":"text","text":` + jsq(`<tool_call>{"name":"attempt_completion","arguments":{"result":"ABCD"}}</tool_call>`+"\nThe work is finished") + `}]`,
			"<tool_call>"},
		{"a fragment that is no call", "antigravity",
			`[{"type":"text","text":` + jsq("I'll use <tool_call> and the tool runs\n"+completion) + `}]`,
			"<tool_call>"},
		{"another agent's request keeps the wrapper", "gemini",
			`[{"type":"text","text":` + jsq(pseudo) + `}]`,
			"<tool_call>"},
	}
	for _, s := range stays {
		got := modelParts(t, s.agent, s.content)
		if len(got) != 1 || !strings.Contains(got[0], s.contains) {
			t.Errorf("%s: the turn was rewritten: %q", s.name, got)
		}
	}

	// a user's turn holding the same wrapper keeps it: only what the model
	// said is rewritten
	body := `{"model":"gemini-4-argon-high","max_tokens":100,"tools":` + tools +
		`,"messages":[{"role":"user","content":` + jsq(pseudo) + `}]}`
	r, err := parseAnthropic([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Request struct {
			Contents []struct {
				Role  string                       `json:"role"`
				Parts []map[string]json.RawMessage `json:"parts"`
			} `json:"contents"`
		} `json:"request"`
	}
	if err := json.Unmarshal(buildCodeAssist(r, "gemini-4-argon-high", "antigravity"), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Request.Contents) == 0 {
		t.Fatal("no contents")
	}
	var user string
	for _, p := range env.Request.Contents[0].Parts {
		var s string
		json.Unmarshal(p["text"], &s)
		user += s
	}
	if !strings.Contains(user, "<tool_call>") {
		t.Errorf("a user's turn was rewritten: %q", user)
	}
}
