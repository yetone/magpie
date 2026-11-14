package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// qingchen060106 on X (11-14): gpt-6-astra through a Codex account seemed
// to spend the plan faster from Claude Code than from Codex. A Claude Code
// tool loop through a Codex account is asked as Codex asks its own: every
// turn of the conversation under one cache key, in the body and in the
// session headers the backend keeps its cached prompt by; the effort the
// user picked, not a higher one; the step's sealed reasoning sent back
// before its call, so the model doesn't think the step through again; and
// the agent's prompt and tools once each, with nothing added to the input
// but the agent's own instructions as a developer message.
func TestClaudeCodeOnCodexSpendsAsCodex(t *testing.T) {
	codexSignedIn(t)
	var mu sync.Mutex
	var bodies [][]byte
	var heads []http.Header
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"list it"}],"encrypted_content":"gAAAAABsealed"}`
	const call = `{"type":"function_call","id":"fc_1","call_id":"call_9","name":"Bash","arguments":"{\"command\":\"ls\"}","status":"completed"}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"gpt-6-astra","display_name":"GPT-6 Astra","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"}],"context_window":272000}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		heads = append(heads, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6-astra"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
			`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_1","summary_index":0,"delta":"list it"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":`+reasoning+`}`,
			`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_9","name":"Bash","arguments":""}}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{\"command\":\"ls\"}"}`,
			`data: {"type":"response.output_item.done","output_index":1,"item":`+call+`}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[`+reasoning+`,`+call+`],"usage":{"input_tokens":5,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	// Claude Code 2.1.296's own first request of a session
	raw, err := os.ReadFile("testdata/workdir/claude-code-2.1.296.json")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	req["model"] = "codex/gpt-6-astra"
	req["output_config"] = map[string]any{"effort": "high"}
	first, _ := json.Marshal(req)
	code, out := post(t, "/v1/messages", string(first))
	if code != 200 {
		t.Fatalf("first turn: %d %s", code, out)
	}

	// the reply as Claude Code keeps it, and the call's result after it
	var blocks []map[string]any
	var cur map[string]any
	args := ""
	for _, ev := range events(out) {
		switch ev["type"] {
		case "content_block_start":
			cur, args = ev["content_block"].(map[string]any), ""
		case "content_block_delta":
			d := ev["delta"].(map[string]any)
			switch d["type"] {
			case "thinking_delta":
				cur["thinking"] = cur["thinking"].(string) + d["thinking"].(string)
			case "signature_delta":
				cur["signature"] = d["signature"]
			case "input_json_delta":
				args += d["partial_json"].(string)
			}
		case "content_block_stop":
			if cur["type"] == "tool_use" {
				cur["input"] = json.RawMessage(args)
			}
			blocks = append(blocks, cur)
		}
	}
	var useID string
	for _, b := range blocks {
		if b["type"] == "tool_use" {
			useID, _ = b["id"].(string)
		}
	}
	if useID == "" {
		t.Fatalf("no tool_use in the reply: %s", out)
	}
	req["messages"] = append(req["messages"].([]any),
		map[string]any{"role": "assistant", "content": blocks},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": useID, "content": "a b c"}}})
	second, _ := json.Marshal(req)
	if code, out := post(t, "/v1/messages", string(second)); code != 200 {
		t.Fatalf("second turn: %d %s", code, out)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("the backend was asked %d times, want 2", len(bodies))
	}
	key := gjson.GetBytes(bodies[0], "prompt_cache_key").String()
	for i, b := range bodies {
		if k := gjson.GetBytes(b, "prompt_cache_key").String(); k == "" || k != key {
			t.Errorf("turn %d: prompt_cache_key %q, the first's %q", i+1, k, key)
		}
		if s, th := heads[i].Get("session-id"), heads[i].Get("thread-id"); s != key || th != key {
			t.Errorf("turn %d: session-id %q thread-id %q, want the cache key %q", i+1, s, th, key)
		}
		if e := gjson.GetBytes(b, "reasoning.effort").String(); e != "high" {
			t.Errorf("turn %d: effort %q, want high as picked", i+1, e)
		}
		if n, want := len(gjson.GetBytes(b, "tools").Array()), len(gjson.GetBytes(raw, "tools").Array()); n != want {
			t.Errorf("turn %d: %d tools, Claude Code declared %d", i+1, n, want)
		}
		if strings.Contains(strings.ToLower(gjson.GetBytes(b, "input").Raw), "magpie") {
			t.Errorf("turn %d: the input names magpie", i+1)
		}
		// Claude Code's system blocks, billing line left out, go as one
		// developer message, and its messages as they were: nothing else
		in := gjson.GetBytes(b, "input").Array()
		dev := 0
		for _, it := range in {
			if it.Get("role").String() == "developer" && strings.HasPrefix(it.Get("content.0.text").String(), "You are a Claude agent") {
				dev++
			}
			if strings.Contains(it.Raw, "x-anthropic-billing-header") {
				t.Errorf("turn %d: the billing line went upstream", i+1)
			}
		}
		if dev != 1 {
			t.Errorf("turn %d: Claude Code's system prompt went %d times", i+1, dev)
		}
	}
	// the first turn's items are the second's start, byte for byte, so the
	// cached prefix holds
	in0, in1 := gjson.GetBytes(bodies[0], "input").Array(), gjson.GetBytes(bodies[1], "input").Array()
	if len(in1) != len(in0)+3 {
		t.Fatalf("second turn's input has %d items, want the first's %d and reasoning, call, output", len(in1), len(in0))
	}
	for i := range in0 {
		if in0[i].Raw != in1[i].Raw {
			t.Errorf("input item %d changed between turns:\n%s\n%s", i, in0[i].Raw, in1[i].Raw)
		}
	}
	if gjson.GetBytes(bodies[0], "instructions").Raw != gjson.GetBytes(bodies[1], "instructions").Raw ||
		gjson.GetBytes(bodies[0], "tools").Raw != gjson.GetBytes(bodies[1], "tools").Raw {
		t.Error("instructions or tools changed between turns")
	}
	step := in1[len(in0):]
	if step[0].Get("type").String() != "reasoning" || step[0].Get("encrypted_content").String() != "gAAAAABsealed" {
		t.Errorf("the step's sealed reasoning didn't go back before its call: %s", step[0].Raw)
	}
	if step[1].Get("type").String() != "function_call" || step[1].Get("call_id").String() != "call_9" ||
		step[2].Get("type").String() != "function_call_output" || step[2].Get("call_id").String() != "call_9" {
		t.Errorf("the call and its output went as %s %s", step[1].Raw, step[2].Raw)
	}
}
