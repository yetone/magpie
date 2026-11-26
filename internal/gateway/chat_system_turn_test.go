package gateway

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #7204 (Asher-S-Wu; ported from ggbdpq's patch): ZCode, on
// /v1/chat/completions, appends a {role: "system"} reminder after the
// user's turn or a tool round. readChat folded every system message into
// the system prompt, so each reminder rewrote the top-level system an
// Anthropic upstream caches before the conversation, and every turn was a
// full cache write with no read. A system message after a reply or a tool
// result stays where it came, said as a <system-reminder> in that turn, as
// a Responses (#502) or Anthropic client's already is.
//
// The requests are the reporter's own (their 120-record prefix, request A,
// then request B with the reply and the reminder), then a tool round that
// repeats the reminder, as ZCode does.
func chatReminderTurns(model string) []string {
	var recs []string
	for i := range 120 {
		recs = append(recs, fmt.Sprintf("Record %03d: the sample catalog lists blue folders, empty notebooks, and blank receipt paper.", i))
	}
	prefix := "Reply with OK. The following records are synthetic.\n" + strings.Join(recs, "\n")
	first := []any{
		map[string]any{"role": "system", "content": prefix},
		map[string]any{"role": "user", "content": "Reply with OK."},
	}
	second := append(slices.Clone(first),
		map[string]any{"role": "assistant", "content": "OK"},
		map[string]any{"role": "user", "content": "Reply with OK once more."},
		map[string]any{"role": "system", "content": "Reminder for this turn: use a one-word reply."},
	)
	third := append(slices.Clone(second),
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
			"id": "call_1", "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"README.md"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "file contents"},
		map[string]any{"role": "system", "content": "Reminder for this turn: use a one-word reply."},
	)
	var out []string
	for _, msgs := range [][]any{first, second, third} {
		b, _ := json.Marshal(map[string]any{
			"model": model, "stream": true, "stream_options": map[string]any{"include_usage": true},
			"max_completion_tokens": 32, "messages": msgs,
			"tools": []any{map[string]any{"type": "function", "function": map[string]any{
				"name": "read", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}},
		})
		out = append(out, string(b))
	}
	return out
}

// withoutMarks is a JSON value with every cache_control taken out: the
// breakpoints on the newest blocks move with each turn by design.
func withoutMarks(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "cache_control")
		for k, e := range x {
			x[k] = withoutMarks(e)
		}
	case []any:
		for i, e := range x {
			x[i] = withoutMarks(e)
		}
	}
	return v
}

func TestChatLateSystemKeepsTheCachedPrefix(t *testing.T) {
	srv, seen := relayOfEvery(t)
	fresh(t)
	if err := provider.Save(provider.Provider{
		ID: "anth-relay", Name: "Anthropic relay", Key: "k",
		Anthropic: srv.URL, Models: []string{"claude-fable-5.2"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{
		ID: "responses-relay", Name: "Responses relay", Key: "k",
		Responses: srv.URL + "/v1", Models: []string{"example-model"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model, path, system, turns string
	}{
		{"anth-relay/claude-fable-5.2", "/v1/messages", "system", "messages"},
		{"responses-relay/example-model", "/v1/responses", "instructions", "input"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			before, _ := seen()
			for i, body := range chatReminderTurns(tc.model) {
				if code, out := post(t, "/v1/chat/completions", body); code != 200 {
					t.Fatalf("request %d: %d %s", i, code, out)
				}
			}
			paths, bodies := seen()
			paths, bodies = paths[len(before):], bodies[len(before):]
			if len(paths) != 3 || paths[0] != tc.path || paths[2] != tc.path {
				t.Fatalf("upstream paths %v, want three to %s", paths, tc.path)
			}
			var sent [3]map[string]any
			for i := range sent {
				if err := json.Unmarshal([]byte(bodies[i]), &sent[i]); err != nil {
					t.Fatalf("body %d: %v", i, err)
				}
			}
			sys := func(i int) string { b, _ := json.Marshal(withoutMarks(sent[i][tc.system])); return string(b) }
			for i := 1; i < 3; i++ {
				if sys(i) != sys(0) {
					t.Errorf("request %d rewrote the cached %s:\n first: %.300s…\n now:   %.300s…\n tail now: …%s", i, tc.system, sys(0), sys(i), sys(i)[max(0, len(sys(i))-200):])
				}
			}
			if tc.path == "/v1/messages" {
				tools := func(i int) string { b, _ := json.Marshal(withoutMarks(sent[i]["tools"])); return string(b) }
				if tools(1) != tools(0) || tools(2) != tools(0) {
					t.Errorf("the tools, cached before the system, changed between turns:\n %s\n %s\n %s", tools(0), tools(1), tools(2))
				}
			}
			if strings.Contains(sys(2), "Reminder for this turn") {
				t.Errorf("the reminder went into the %s: %s", tc.system, sys(2))
			}
			// each turn's conversation is the last one's with the new
			// turn after it, so the cached prefix is read again
			turns := func(i int) []any { l, _ := withoutMarks(sent[i][tc.turns]).([]any); return l }
			for i := 1; i < 3; i++ {
				prev, cur := turns(i-1), turns(i)
				if len(cur) <= len(prev) || !reflect.DeepEqual(cur[:len(prev)], prev) {
					t.Errorf("request %d's %s doesn't begin with request %d's:\n %s\n %s", i, tc.turns, i-1, bodies[i-1], bodies[i])
				}
			}
			for i := 1; i < 3; i++ {
				if n := strings.Count(bodies[i], "Reminder for this turn: use a one-word reply."); n != i {
					t.Errorf("request %d says the reminder %d times, want %d, in place: %s", i, n, i, bodies[i])
				}
				if !strings.Contains(bodies[i], `\u003csystem-reminder\u003e`) && !strings.Contains(bodies[i], "<system-reminder>") {
					t.Errorf("request %d: the late system message isn't told as a reminder: %s", i, bodies[i])
				}
			}
			if tc.path == "/v1/messages" {
				// the tool round's reminder follows its result in that
				// user turn: a Messages upstream takes results first
				l := turns(2)
				last, _ := l[len(l)-1].(map[string]any)
				blocks, _ := last["content"].([]any)
				if last["role"] != "user" || len(blocks) < 2 {
					t.Fatalf("last message: %v", last)
				}
				b0, _ := blocks[0].(map[string]any)
				b1, _ := blocks[1].(map[string]any)
				if b0["type"] != "tool_result" || b1["type"] != "text" || !strings.Contains(fmt.Sprint(b1["text"]), "Reminder for this turn") {
					t.Errorf("the tool round's turn is %v, want its result, then the reminder", blocks)
				}
			}
		})
	}
}

// The Gemini builds (Code Assist, Gemini, Vertex) fold the same request
// (responseInstructions) through buildCodeAssistSent: their
// systemInstruction stays what the first turn's was. A routing rule's
// system condition still reads the late message (systemTexts), and the
// user's last words are still theirs, not the reminder (lastUserTexts).
func TestChatLateSystemOnGeminiAndRules(t *testing.T) {
	turns := chatReminderTurns("gemini-3.8-flash")
	var first string
	for i, body := range turns {
		r, err := parseChat([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		b, err := buildGemini(r, "gemini-3.8-flash")
		if err != nil {
			t.Fatal(err)
		}
		var g struct {
			SystemInstruction json.RawMessage `json:"systemInstruction"`
		}
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = string(g.SystemInstruction)
		} else if string(g.SystemInstruction) != first {
			t.Errorf("turn %d's systemInstruction isn't the first turn's: …%s", i, g.SystemInstruction[max(0, len(g.SystemInstruction)-200):])
		}
		if i == 0 {
			continue
		}
		if !strings.Contains(string(b), "Reminder for this turn") {
			t.Errorf("turn %d lost the reminder: %s", i, b)
		}
		if !slices.ContainsFunc(r.systemTexts(), func(s string) bool { return strings.Contains(s, "Reminder for this turn") }) {
			t.Errorf("turn %d: a rule's system condition no longer reads the late system message: %q", i, r.systemTexts())
		}
		if i == 1 {
			if got := lastUserTexts(r); len(got) != 1 || got[0] != "Reply with OK once more." {
				t.Errorf("the user's last words are %q", got)
			}
		}
	}
}
