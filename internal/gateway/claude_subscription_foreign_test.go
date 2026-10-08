package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A tool the client's own framework ran after the reply, put in the
// conversation as a call and its result (Pi Team Bright's team_sync), is
// the rest of the turn: the Claude Code that replied goes on, told it,
// where a run started anew wrote the whole conversation to the cache again.
// A result for a call before the reply is not the run's next turn.
func TestClaudeRunKeptAfterTheClientsOwnToolCall(t *testing.T) {
	fakeClaude(t)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	ask := func(msgs string) string {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":[{"name":"team_sync","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		var res struct {
			Content []struct{ Text string } `json:"content"`
		}
		json.Unmarshal(rec.Body.Bytes(), &res)
		if len(res.Content) == 0 {
			t.Fatalf("no answer: %s", rec.Body)
		}
		return res.Content[0].Text
	}
	msg := func(role, text string) string { return `{"role":"` + role + `","content":` + strconv.Quote(text) + `}` }
	sync := func(id string) string {
		return `{"role":"assistant","content":[{"type":"text","text":"Framework sync."},{"type":"tool_use","id":"` + id + `","name":"team_sync","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"no changes"}]}`
	}
	pidOf := func(said string) string { pid, _, _ := strings.Cut(strings.TrimPrefix(said, "pid "), " "); return pid }

	conv := msg("user", "hi")
	first := ask(`[` + conv + `]`)
	// the framework's call and result end the request
	conv += `,` + msg("assistant", first) + `,` + sync("framework-team-sync-1")
	second := ask(`[` + conv + `]`)
	if second != "pid "+pidOf(first)+" turn 2" {
		t.Fatalf("after the framework's call: %q, first %q", second, first)
	}
	// and the user's words after them
	conv += `,` + msg("assistant", second) + `,` + sync("framework-team-sync-2") + `,` + msg("user", "and?")
	if third := ask(`[` + conv + `]`); third != "pid "+pidOf(first)+" turn 3" {
		t.Fatalf("after the framework's call and the user's words: %q, first %q", third, first)
	}
	// a result for a call before the run's last reply is not its next turn
	other := msg("user", "bye")
	bye := ask(`[` + other + `]`)
	other = msg("user", "bye") + `,` + `{"role":"assistant","content":[{"type":"tool_use","id":"early","name":"team_sync","input":{}}]},` +
		msg("assistant", bye) + `,` + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"early","content":"late"}]}`
	if late := ask(`[` + other + `]`); pidOf(late) == pidOf(bye) {
		t.Fatalf("a result for a call before the reply went to the run: %q", late)
	}
}

// The messages since a reply go as they are when the user's alone; with
// the client's own call among them, each says who said it.
func TestClaudeTurnLabelsTheClientsOwnCall(t *testing.T) {
	text := func(msgs []Message) string {
		var b strings.Builder
		for _, block := range renderClaudeTurn(msgs, nil) {
			s, _ := block["text"].(string)
			b.WriteString(s)
		}
		return b.String()
	}
	user := Message{Role: "user", Parts: []Part{{Kind: Text, Text: "and?"}}}
	if got := text([]Message{user}); got != "and?" {
		t.Fatalf("the user's words alone: %q", got)
	}
	got := text([]Message{
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "Framework sync."}, {Kind: ToolCall, ID: "c1", Name: "team_sync", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "no changes"}}},
		user,
	})
	for _, want := range []string{"Assistant: Framework sync.", "[tool call team_sync id=c1", "Human: \n[tool result id=c1]\nno changes", "Human: and?"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q not in %q", want, got)
		}
	}
}
