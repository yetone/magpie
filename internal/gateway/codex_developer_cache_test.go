package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// codexItem is a Responses input message, as Codex sends one.
func codexItem(role, text string) string {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	return `{"type":"message","role":"` + role + `","content":[{"type":"` + kind + `","text":` + strconv.Quote(text) + `}]}`
}

// A developer message Codex adds once its conversation has gone on (its
// context changed: world state, settings) stays where it is: the system
// prompt, and so Anthropic's cached prefix, is the one the turns before
// had (#502: the turn after it wrote the whole conversation to the cache).
func TestCodexLaterDeveloperMessageKeepsTheSystem(t *testing.T) {
	head := codexItem("developer", "<permissions instructions>sandboxed</permissions instructions>") + `,` +
		codexItem("user", "<environment_context>cwd</environment_context>") + `,` + codexItem("user", "fix the bug")
	turn1 := `{"model":"claude-opus-5-5","instructions":"You are Codex.","input":[` + head + `]}`
	turn2 := `{"model":"claude-opus-5-5","instructions":"You are Codex.","input":[` + head + `,` +
		codexItem("assistant", "done") + `,` + codexItem("developer", "<skills_instructions>a new skill</skills_instructions>") + `,` +
		codexItem("user", "thanks, and the test?") + `]}`
	r1, err := parseResponses([]byte(turn1))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := parseResponses([]byte(turn2))
	if err != nil {
		t.Fatal(err)
	}
	if r1.System != r2.System || !strings.Contains(r1.System, "permissions instructions") {
		t.Fatalf("system changed:\n%q\n%q", r1.System, r2.System)
	}
	var q1, q2 struct {
		System   json.RawMessage
		Messages []json.RawMessage
	}
	json.Unmarshal(buildAnthropic(r1, "claude-opus-5-5"), &q1)
	json.Unmarshal(buildAnthropic(r2, "claude-opus-5-5"), &q2)
	if string(q1.System) != string(q2.System) {
		t.Fatalf("Anthropic's system changed:\n%s\n%s", q1.System, q2.System)
	}
	last := r2.Messages[len(r2.Messages)-1]
	if len(r2.Messages) != 3 || last.Role != "user" || !strings.Contains(text(last.Parts), "<system-reminder>\n<skills_instructions>") ||
		!strings.HasSuffix(text(last.Parts), "thanks, and the test?") {
		t.Fatalf("messages: %+v", r2.Messages)
	}
	// the user's words are what the router's classifier is asked about
	if got := userText(r2); got != "thanks, and the test?" {
		t.Fatalf("user text %q", got)
	}
	if turn, within := turnIn(r2); turn != 2 || within {
		t.Fatalf("turn %d within %v", turn, within)
	}
}

// A Codex thread on a Claude subscription goes on in the Claude Code that
// had its turn before, when a memory request of Codex's (another model,
// its own instructions, the same session) came in between and Codex then
// added a developer message to the thread.
func TestCodexThreadKeepsItsClaudeRunAcrossAMemoryCall(t *testing.T) {
	fakeClaude(t)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	said := regexp.MustCompile(`pid \d+ turn \d+`)
	ask := func(body string) string {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("session_id", "019a-codex-thread")
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, req, provider.Responses, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		got := said.FindString(rec.Body.String())
		if got == "" {
			t.Fatalf("no answer: %s", rec.Body)
		}
		return got
	}
	tools := `"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]`
	head := codexItem("developer", "<permissions instructions>sandboxed</permissions instructions>") + `,` + codexItem("user", "fix the bug")
	first := ask(`{"model":"claude-sonnet-5","instructions":"You are Codex.",` + tools + `,"input":[` + head + `]}`)
	pid, _, _ := strings.Cut(strings.TrimPrefix(first, "pid "), " ")

	// Codex's memory extraction: the same session, a prompt of its own
	if mem := ask(`{"model":"claude-sonnet-5","instructions":"Extract memories.","input":[` + codexItem("user", "rollout: …") + `]}`); strings.Contains(mem, "pid "+pid+" ") {
		t.Fatalf("the memory call took the thread's run: %q", mem)
	}

	second := ask(`{"model":"claude-sonnet-5","instructions":"You are Codex.",` + tools + `,"input":[` + head + `,` +
		codexItem("assistant", first) + `,` + codexItem("developer", "<skills_instructions>memories updated</skills_instructions>") + `,` +
		codexItem("user", "and the test?") + `]}`)
	if second != "pid "+pid+" turn 2" {
		t.Fatalf("the thread's next turn: %q, first %q", second, first)
	}
}
