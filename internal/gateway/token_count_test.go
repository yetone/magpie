package gateway

import (
	"encoding/json"
	"testing"
)

func TestLocalRequestTokenCountIncludesToolsAndResults(t *testing.T) {
	req := &Request{
		System:   "system",
		Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hello"}, {Kind: ToolResult, CallID: "c1", Text: "done"}}}},
		Tools:    []Tool{{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
	if got := localRequestTokenCount(req); got <= localTokenCount("system") {
		t.Fatalf("request count %d did not include message/tool content", got)
	}
}

func TestAnthropicUsageCarriesProvisionalState(t *testing.T) {
	got := (Usage{Input: 4, Output: 2, State: "provisional"}).anthropic()
	if got.MagpieUsageState != "provisional" || got.InputTokens != 4 || got.OutputTokens != 2 {
		t.Fatalf("usage = %+v", got)
	}
	final := (Usage{Input: 4, Output: 2, State: "final"}).anthropic()
	if final.MagpieUsageState != "final" {
		t.Fatalf("final usage state = %q", final.MagpieUsageState)
	}
}
