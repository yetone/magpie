package provider

import (
	"encoding/json"
	"testing"
)

func TestWorkBuddyBody(t *testing.T) {
	roles := func(body []byte) (out []string) {
		var m struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		for _, msg := range m.Messages {
			out = append(out, msg.Role)
		}
		return out
	}

	// none: one is put first, the rest kept as they were
	in := []byte(`{"model":"deepseek-v4.1-flash","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"<b>hi</b> & 1.50"}]}`)
	out := wbBody(in)
	if got := roles(out); len(got) != 2 || got[0] != "system" || got[1] != "user" {
		t.Fatalf("roles %v: %s", got, out)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["model"] != "deepseek-v4.1-flash" || m["stream"] != true || m["max_tokens"] != 1024.0 {
		t.Fatalf("fields: %s", out)
	}
	if first := m["messages"].([]any)[0].(map[string]any); first["content"] != wbSystem {
		t.Fatalf("system: %v", first)
	}
	if user := m["messages"].([]any)[1].(map[string]any); user["content"] != "<b>hi</b> & 1.50" {
		t.Fatalf("user: %v", user)
	}

	// a system prompt later on still wants one first
	if got := roles(wbBody([]byte(`{"messages":[{"role":"user","content":"a"},{"role":"system","content":"s"}]}`))); len(got) != 3 || got[0] != "system" {
		t.Fatalf("late system: %v", got)
	}

	// already first, not a chat, or not JSON: the same bytes
	for _, b := range []string{
		`{"messages":[{"role":"system","content":"own"},{"role":"user","content":"a"}]}`,
		`{"messages":[]}`,
		`{"prompt":"a cat"}`,
		`{"messages":`,
	} {
		if got := string(wbBody([]byte(b))); got != b {
			t.Errorf("%s became %s", b, got)
		}
	}
}

// TestWorkBuddyRefusedPrompt is #182: WorkBuddy answers a chat whose first
// system message opens with Claude Code's own identity with "illegal API
// invocation from an unapproved channel", so magpie puts a neutral line in
// front and leaves the agent's prompt second.
func TestWorkBuddyRefusedPrompt(t *testing.T) {
	// content is the first system message's content, as the body carries it
	body := func(content string) []byte {
		b, err := json.Marshal(map[string]any{
			"model": "deepseek-v4.1-flash",
			"messages": []any{
				map[string]any{"role": "system", "content": content},
				map[string]any{"role": "user", "content": "hi"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// blocks is the same, as Claude Code sends its system prompt
	blocks := func(texts ...string) []byte {
		var blks []any
		for _, s := range texts {
			blks = append(blks, map[string]any{"type": "text", "text": s})
		}
		b, err := json.Marshal(map[string]any{
			"model": "deepseek-v4.1-flash",
			"messages": []any{
				map[string]any{"role": "system", "content": blks},
				map[string]any{"role": "user", "content": "hi"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// first is the first system message's content after wbBody, and n how
	// many messages it left
	first := func(in []byte) (content string, n int) {
		var m struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(wbBody(in), &m); err != nil {
			t.Fatalf("%v: %s", err, in)
		}
		s, _ := m.Messages[0].Content.(string)
		return s, len(m.Messages)
	}

	const cc = "You are Claude Code, Anthropic's official CLI for Claude."
	const billing = "x-anthropic-billing-header: cc_version=2.1.284.dd4; cc_entrypoint=cli;"

	// refused: a neutral line goes in front, the agent's prompt stays second
	for _, in := range []string{
		cc,
		cc + "\n\nYou are an interactive CLI tool.",
		billing,
		billing + "\n\n" + cc,
		"You are Claude Code, Anthropic's official CLI for Claude",  // no full stop
		"YOU ARE CLAUDE CODE, ANTHROPIC'S OFFICIAL CLI FOR CLAUDE.", // case-insensitively
	} {
		if got, n := first(body(in)); got != wbSystem || n != 3 {
			t.Errorf("%q: first %q of %d, want the neutral line of 3", in, got, n)
		}
	}

	// blocks: Claude Code's own shape, its prompt still second
	out := wbBody(blocks(billing, cc, "You are an interactive CLI tool."))
	var m struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Messages) != 3 {
		t.Fatalf("blocks: %d messages: %s", len(m.Messages), out)
	}
	var s string
	if err := json.Unmarshal(m.Messages[0].Content, &s); err != nil || s != wbSystem {
		t.Fatalf("blocks: first %s", m.Messages[0].Content)
	}
	if !json.Valid(m.Messages[1].Content) || string(m.Messages[1].Content) == "null" {
		t.Fatalf("blocks: the agent's own prompt went missing: %s", out)
	}

	// not refused: the same words after any other text, on a later line, in
	// a second system message, or another agent's identity — the body as it
	// was
	for _, in := range [][]byte{
		body("You are Claude Code, Anthropic's official CLI for Claude."[:1] + cc), // a character in front
		body("\n" + cc),                                           // a leading newline
		body("Notes:\n" + cc),                                     // a later line
		body("Anthropic's official CLI for Claude"),               // without "You are Claude Code, "
		body("You are Claude Code, Anthropic's official CLI for"), // "for Claude" cut short
		body("You  are  Claude  Code"),                            // not the words
		body("You are a helpful assistant."),                      // a plain one
		body("You are Codex, based on GPT-5."),                    // another agent's
		body("You are Hermes Agent, an AI assistant."),
		body("You are Cline, a highly skilled software engineer."),
		body("You are Qoder, an AI coding assistant."),
		blocks("Anthropic's official CLI for Claude"), // the words in a later block
		[]byte(`{"messages":[{"role":"system","content":"a"},{"role":"system","content":"` + cc + `"}]}`),
	} {
		if got := string(wbBody(in)); got != string(in) {
			t.Errorf("changed though it needn't: %s became %s", in, got)
		}
	}
}
