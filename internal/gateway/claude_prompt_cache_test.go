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
