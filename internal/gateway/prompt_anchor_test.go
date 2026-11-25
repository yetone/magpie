package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// gptAgentBody is a Responses request as Alma sends one on a GPT model
// with store off: its instructions and tools, a user turn, then steps steps
// of an agent turn, each a reasoning item sealed in encrypted_content (its
// size as Codex's rollouts show it, about 3.2 KB and 13 bytes a reasoning
// token), a function call and its output. reasoning is the reasoning
// tokens the sealed items stand for in all.
func gptAgentBody(t *testing.T, steps int) (body []byte, reasoning int) {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	sealed := func(tokens int) string {
		raw := make([]byte, (3200+13*tokens)*3/4)
		rng.Read(raw)
		return base64.StdEncoding.EncodeToString(raw)
	}
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "Go through the repository and fix every failing test, one package at a time."}}},
	}
	for i := range steps {
		r := 40 + rng.Intn(200) // 40–240 reasoning tokens a step, as most of the rollouts' are
		reasoning += r
		call := fmt.Sprintf("call_%03d", i)
		input = append(input,
			map[string]any{"type": "reasoning", "id": fmt.Sprintf("rs_%03d", i), "summary": []any{}, "encrypted_content": sealed(r)},
			map[string]any{"type": "function_call", "call_id": call, "name": "Bash",
				"arguments": fmt.Sprintf(`{"command":"go test ./internal/pkg%d/..."}`, i)},
			map[string]any{"type": "function_call_output", "call_id": call,
				"output": strings.Repeat(fmt.Sprintf("ok  \tgithub.com/example/pkg%d\t0.%03ds\n", i, i), 40)},
		)
	}
	req := map[string]any{
		"model":        "auto/gpt-6.2-astra",
		"instructions": strings.Repeat("You are Alma, a helpful assistant on the user's computer. ", 120),
		"tools": []any{
			map[string]any{"type": "function", "name": "Bash", "description": "Run a shell command.",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}},
		},
		"input":   input,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
		"stream":  true,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return b, reasoning
}

// A sealed reasoning item is taken for the reasoning it stands for, not its
// base64 over four: a long agent turn on a GPT model, an item a step, was
// read as nearly twice what the vendor counted (tangcuyu on Discord: 294K
// live against 164K). The rest of the body is the same either way.
func TestPromptSealedReasoningIsItsTokens(t *testing.T) {
	const steps = 180
	body, reasoning := gptAgentBody(t, steps)
	p := promptOf(provider.Responses, body)
	if p == nil {
		t.Fatal("no prompt")
	}
	// the same body with the items' seals gone is the rest of it
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	sealedB64 := 0
	for _, it := range req["input"].([]any) {
		m := it.(map[string]any)
		if e, ok := m["encrypted_content"].(string); ok {
			sealedB64 += len(e)
			m["encrypted_content"] = ""
		}
	}
	bare, _ := json.Marshal(req)
	rest := promptOf(provider.Responses, bare).Tokens

	got := p.Tokens - rest
	if got < reasoning*9/10 || got > reasoning*11/10 {
		t.Fatalf("the %d sealed items were taken for %d tokens, want about the %d reasoning tokens they stand for", steps, got, reasoning)
	}
	if old := sealedB64 * 3 / 4 / 4; rest+old < (rest+reasoning)*3/2 {
		t.Fatalf("the body does not show the old overcount: %d by base64 against %d", rest+old, rest+reasoning)
	}
	if sealedTokens(0) != 0 || sealedTokens(3000) != 0 {
		t.Fatal("an item with no seal, or less than its envelope, stands for no tokens")
	}
}

// A request still answering is shown on the prompt the vendor counted for
// the step before it in the same session, with only what it adds
// estimated; a title call between them in the session, on the same model,
// is not taken for that step. Another session, a held previous response,
// and a step before it the vendor didn't count leave the estimate as it is.
func TestPromptLiveAnchoredToLastCounted(t *testing.T) {
	prevBody, _ := gptAgentBody(t, 120)
	curBody, _ := gptAgentBody(t, 123)
	prevEst := promptOf(provider.Responses, prevBody)
	const counted = 164_000

	route := func(session string, done bool, p *Prompt) *Route {
		return &Route{Agent: "alma", Session: session, Model: "auto/gpt-6.2-astra", Done: done, Prompt: p}
	}
	prev := route("chat-1", true, prevEst.calibrated(counted, 272_000))
	titleEst := promptOf(provider.Responses, []byte(`{"model":"auto/gpt-6.2-astra","input":"Name this chat in a few words."}`))
	title := route("chat-1", true, titleEst.calibrated(30, 272_000))

	cur := route("chat-1", false, nil)
	p := promptOf(provider.Responses, curBody)
	got := anchored([]*Route{prev, title, cur}, cur, p)
	want := counted + p.est - prevEst.est
	if got.Tokens != want {
		t.Fatalf("live prompt %d, want the %d counted before and the %d added: %d", got.Tokens, counted, p.est-prevEst.est, want)
	}
	if got.Counted {
		t.Fatal("an anchored prompt is not counted")
	}
	sum := 0
	for _, part := range got.Parts {
		sum += part.Tokens
	}
	if sum != got.Tokens {
		t.Fatalf("parts add up to %d, the prompt is %d", sum, got.Tokens)
	}
	if p.Tokens != p.est || got.est != p.est {
		t.Fatal("anchoring changed the estimate it was given, or lost it")
	}
	// the route's end scales the anchored prompt to what was counted
	if done := got.calibrated(counted+9_000, 272_000); done.Tokens != counted+9_000 || !done.Counted || done.est != p.est {
		t.Fatalf("counted at the end: %+v", done)
	}

	other := route("chat-2", false, nil)
	if q := anchored([]*Route{prev, other}, other, p); q.Tokens != p.est {
		t.Fatalf("another session's count was taken: %d", q.Tokens)
	}
	uncounted := route("chat-1", true, prevEst.calibrated(0, 272_000))
	if q := anchored([]*Route{uncounted, cur}, cur, p); q.Tokens != p.est {
		t.Fatalf("a step not counted was taken: %d", q.Tokens)
	}
	held := *p
	held.Held = true
	if q := anchored([]*Route{prev, cur}, cur, &held); q.Tokens != p.est {
		t.Fatalf("a held prompt was anchored: %d", q.Tokens)
	}
	// a step longer than this one (the chat compacted since) is no anchor
	if q := anchored([]*Route{prev, cur}, cur, titleEst); q.Tokens != titleEst.est {
		t.Fatalf("a shorter request was anchored to a longer one: %d", q.Tokens)
	}
}

// Through inspectPrompt, as a request goes out: the route shows the
// anchored prompt live, and the end is handed the same.
func TestInspectPromptAnchorsLive(t *testing.T) {
	t.Setenv(testSandbox, "1")
	prevBody, _ := gptAgentBody(t, 60)
	curBody, _ := gptAgentBody(t, 61)
	s := &Server{}
	prev := s.trace.begin(Route{Agent: "alma", Session: "chat-1", Model: "auto/gpt-6.2-astra"})
	prevEst := promptOf(provider.Responses, prevBody)
	s.trace.update(prev, func(r *Route) { r.Done, r.Prompt = true, prevEst.calibrated(80_000, 272_000) })

	cur := s.trace.begin(Route{Agent: "alma", Session: "chat-1", Model: "auto/gpt-6.2-astra"})
	p := s.inspectPrompt(cur, provider.Responses, curBody, curBody, true)()
	want := 80_000 + promptOf(provider.Responses, curBody).est - prevEst.est
	if p.Tokens != want {
		t.Fatalf("handed %d, want %d", p.Tokens, want)
	}
	s.trace.mu.Lock()
	live := cur.Prompt
	s.trace.mu.Unlock()
	if live == nil || live.Tokens != want {
		t.Fatalf("live prompt %+v, want %d tokens", live, want)
	}
}
