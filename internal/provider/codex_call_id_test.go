package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexBodyShortensForeignCallIDs(t *testing.T) {
	for _, kind := range []string{"function", "custom_tool", "local_shell"} {
		t.Run(kind, func(t *testing.T) {
			long := "call-" + strings.Repeat("a", 36) + "-46__fc_" + strings.Repeat("b", 36) + "_0"
			other := long[:len(long)-1] + "1"
			input := []any{
				map[string]any{"type": kind + "_call", "call_id": long, "name": "shell", "arguments": "{}"},
				map[string]any{"type": kind + "_call_output", "call_id": long, "output": "ok"},
				map[string]any{"type": kind + "_call", "call_id": other, "name": "shell", "arguments": "{}"},
				map[string]any{"type": kind + "_call_output", "call_id": other, "output": "ok"},
				map[string]any{"type": kind + "_call", "call_id": strings.Repeat("x", 64), "arguments": "{}"},
				map[string]any{"type": kind + "_call_output", "call_id": strings.Repeat("x", 64), "output": "ok"},
			}
			body, _ := json.Marshal(map[string]any{"model": "gpt-6-luna", "input": input})
			var got struct {
				Input []map[string]any `json:"input"`
			}
			if err := json.Unmarshal(codexBody(body), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Input) != len(input) {
				t.Fatalf("input: %+v", got.Input)
			}
			id, _ := got.Input[0]["call_id"].(string)
			if len(id) == 0 || len(id) > 64 || id != got.Input[1]["call_id"] {
				t.Fatalf("call/result IDs: %+v", got.Input[:2])
			}
			if id == got.Input[2]["call_id"] || got.Input[2]["call_id"] != got.Input[3]["call_id"] {
				t.Fatalf("distinct call/result IDs: %+v", got.Input[:4])
			}
			if got.Input[4]["call_id"] != strings.Repeat("x", 64) || got.Input[5]["call_id"] != strings.Repeat("x", 64) {
				t.Fatalf("64-character IDs changed: %+v", got.Input[4:])
			}
			var again struct {
				Input []map[string]any `json:"input"`
			}
			json.Unmarshal(codexBody(body), &again)
			if id != again.Input[0]["call_id"] {
				t.Fatal("mapping changed between requests")
			}
		})
	}
}
