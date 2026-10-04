package gateway

import (
	"slices"
	"strings"
	"testing"
)

// The caller's instructions are a block of their own, marked for an hour's
// cache, ahead of the messages: two requests with the same instructions
// and different messages share them in the cache. Claude Code writes an
// hour's cache too, as a 1h mark may not follow a 5m one.
func TestClaudeInstructionsCachedApart(t *testing.T) {
	for _, said := range []string{"one", "two"} {
		blocks, err := renderClaudePrompt(&Request{System: "rules", Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: said}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(blocks) != 2 || blocks[0]["text"] != "<external_system_instructions>\nrules\n</external_system_instructions>\n\n" {
			t.Fatalf("blocks: %v", blocks)
		}
		if cc, _ := blocks[0]["cache_control"].(map[string]string); cc["ttl"] != "1h" || blocks[1]["cache_control"] != nil {
			t.Fatalf("cache marks: %v", blocks)
		}
		if !strings.Contains(blocks[1]["text"].(string), "Human: "+said) {
			t.Fatalf("messages: %v", blocks[1])
		}
	}
	blocks, _ := renderClaudePrompt(&Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}})
	if len(blocks) != 1 || blocks[0]["cache_control"] != nil {
		t.Fatalf("no instructions: %v", blocks)
	}
	env := cleanClaudeEnv([]string{"CLAUDE_CODE_PROMPT_CACHE_TTL=5m"})
	if !slices.Contains(env, "CLAUDE_CODE_PROMPT_CACHE_TTL=1h") || slices.Contains(env, "CLAUDE_CODE_PROMPT_CACHE_TTL=5m") {
		t.Fatalf("env: %q", env)
	}
}

// Claude Code's system prompt begins with a billing line whose hash follows
// the request's first user message. Its auto mode classifier sends the same
// policy with another transcript each time: with the line kept, no check's
// instructions were ever read from the cache (X, AncientTwo). Two checks
// now share them.
func TestClaudeInstructionsWithoutBillingHeader(t *testing.T) {
	var first []any
	for i, hash := range []string{"8c9", "4ff"} {
		body := `{"model":"m","max_tokens":64,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.289.` + hash + `; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are a security monitor.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"transcript ` + hash + `"}]}`
		req, err := parseAnthropic([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := renderClaudePrompt(req)
		if err != nil {
			t.Fatal(err)
		}
		if blocks[0]["text"] != "<external_system_instructions>\nYou are a security monitor.\n</external_system_instructions>\n\n" {
			t.Fatalf("check %d: %q", i, blocks[0]["text"])
		}
		first = append(first, blocks[0]["text"])
	}
	if first[0] != first[1] {
		t.Fatalf("instructions differ: %q", first)
	}
	if got := withoutBillingHeader("Rules mention x-anthropic-billing-header: here"); got != "Rules mention x-anthropic-billing-header: here" {
		t.Fatalf("a mention later on: %q", got)
	}
}
