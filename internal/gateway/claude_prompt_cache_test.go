package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// a value Claude Code doesn't take is no pick: the hour stays
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	env := cleanClaudeEnv([]string{"CLAUDE_CODE_PROMPT_CACHE_TTL=7d"})
	if !slices.Contains(env, "CLAUDE_CODE_PROMPT_CACHE_TTL=1h") || slices.Contains(env, "CLAUDE_CODE_PROMPT_CACHE_TTL=7d") {
		t.Fatalf("env: %q", env)
	}
}

// A bridge run keeps its prompt cache as long as the user picked for
// Claude Code's main conversation, in the Agents page (settings.json's
// promptCacheTtl) or a variable, and the marks magpie puts in its first
// message are as long, since Anthropic refuses a 1h mark after a 5m one.
// With nothing picked it is an hour, as before. linkaww: 5m picked, the
// run was given CLAUDE_CODE_PROMPT_CACHE_TTL=1h, and 237 of 241 relayed
// requests wrote an hour's cache.
func TestClaudeRunKeepsTheUsersCacheTTL(t *testing.T) {
	for _, c := range []struct {
		name, settings, env, force, want string
	}{
		{name: "nothing picked", want: "1h"},
		{name: "agents page 5m", settings: `{"promptCacheTtl":"5m"}`, want: "5m"},
		{name: "agents page 1h", settings: `{"promptCacheTtl":"1h"}`, want: "1h"},
		{name: "settings env", settings: `{"env":{"CLAUDE_CODE_PROMPT_CACHE_TTL":"5m"},"promptCacheTtl":"1h"}`, want: "5m"},
		{name: "magpie's env", env: "5m", want: "5m"},
		{name: "forced 5m", force: "1", settings: `{"promptCacheTtl":"1h"}`, want: "5m"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newScriptHarness(t, "ok", `echo "ttl ${CLAUDE_CODE_PROMPT_CACHE_TTL-unset} force ${FORCE_PROMPT_CACHING_5M-unset}" >> $log`)
			if c.settings != "" {
				if err := os.MkdirAll(h.config, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(h.config, "settings.json"), []byte(c.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if c.env != "" {
				t.Setenv("CLAUDE_CODE_PROMPT_CACHE_TTL", c.env)
			}
			if c.force != "" {
				t.Setenv("FORCE_PROMPT_CACHING_5M", c.force)
			}
			h.ask("rules", "hello", "next")
			runs := h.runs()
			if len(runs) != 1 {
				t.Fatalf("runs: %d", len(runs))
			}
			for _, log := range runs {
				if !strings.Contains(log, "ttl "+c.want+" force unset") {
					t.Fatalf("run's environment, want %s:\n%s", c.want, log)
				}
				var told string
				for _, line := range strings.Split(log, "\n") {
					if strings.HasPrefix(line, "told ") {
						told = line
						break
					}
				}
				other := map[string]string{"5m": "1h", "1h": "5m"}[c.want]
				if !strings.Contains(told, `"ttl":"`+c.want+`"`) || strings.Contains(told, `"ttl":"`+other+`"`) {
					t.Fatalf("first message's marks, want %s: %s", c.want, told)
				}
			}
		})
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

// A run started anew for a conversation with history marks the history's
// last whole chunk, not the instructions, and a later run told more of the
// same conversation repeats every block up to that mark: Anthropic finds
// what the first wrote 20 blocks back from the second's mark. Before, the
// history went in one block with the turn to answer, and each run started
// anew wrote all of it again (a change of tools, a turn that missed its run).
func TestClaudeHistoryCachedAcrossNewRuns(t *testing.T) {
	conversation := func(replies int) *Request {
		req := &Request{System: "rules", Tools: []Tool{{Name: "read"}}}
		for i := 0; i < replies; i++ {
			req.Messages = append(req.Messages,
				Message{Role: "user", Parts: []Part{{Kind: Text, Text: fmt.Sprintf("question %d", i)}}},
				Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: fmt.Sprintf("answer %d", i)}, {Kind: ToolCall, ID: fmt.Sprintf("c%d", i), Name: "read", Args: json.RawMessage(`{"path":"a"}`)}}},
				Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: fmt.Sprintf("c%d", i), Text: "contents"}}},
				Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: fmt.Sprintf("done %d", i)}}})
		}
		req.Messages = append(req.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: "the turn to answer"}}})
		return req
	}
	marked := func(blocks []map[string]any) []int {
		var at []int
		for i, b := range blocks {
			if cc, ok := b["cache_control"].(map[string]string); ok {
				if cc["ttl"] != "1h" {
					t.Fatalf("block %d marked %v", i, cc)
				}
				at = append(at, i)
			}
		}
		return at
	}
	earlier, err := renderClaudePrompt(conversation(10))
	if err != nil {
		t.Fatal(err)
	}
	later, err := renderClaudePrompt(conversation(25))
	if err != nil {
		t.Fatal(err)
	}
	first, second := marked(earlier), marked(later)
	if len(first) != 1 || len(second) != 1 || first[0] == 0 || second[0] <= first[0] {
		t.Fatalf("marks: earlier %v, later %v", first, second)
	}
	if second[0]-first[0] > 20 {
		t.Fatalf("the earlier mark is %d blocks back, past Anthropic's lookback", second[0]-first[0])
	}
	for i := 0; i <= first[0]; i++ {
		a, b := earlier[i]["text"], later[i]["text"]
		if a != b {
			t.Fatalf("block %d differs:\n%q\n%q", i, a, b)
		}
	}
	if strings.Contains(earlier[first[0]]["text"].(string), "the turn to answer") {
		t.Fatalf("the marked block holds the turn to answer")
	}
	// a conversation with less than a chunk of history keeps the mark on the instructions
	short, _ := renderClaudePrompt(conversation(1))
	if at := marked(short); len(at) != 1 || at[0] != 0 {
		t.Fatalf("short conversation marks %v", at)
	}
}

// A conversation with no reply that calls no tool, as a sub-agent's loop
// of tool calls that Claude Code's AgentSummary asks a summary of with its
// last results, has no history to chunk: its messages before the last are
// chunked, so a later request of the same loop, started anew, reads what
// the earlier one wrote (#1716, JinJieBeWater).
func TestClaudeToolLoopCachedAcrossNewRuns(t *testing.T) {
	loop := func(rounds int, ask string) *Request {
		req := &Request{System: "rules", Tools: []Tool{{Name: "read"}}}
		req.Messages = append(req.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: "the task"}}})
		for i := 0; i < rounds; i++ {
			last := []Part{{Kind: ToolResult, CallID: fmt.Sprintf("c%d", i), Text: "contents"}}
			if i == rounds-1 {
				last = append(last, Part{Kind: Text, Text: ask})
			}
			req.Messages = append(req.Messages,
				Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: fmt.Sprintf("c%d", i), Name: "read", Args: json.RawMessage(`{"path":"a"}`)}}},
				Message{Role: "user", Parts: last})
		}
		return req
	}
	marked := func(blocks []map[string]any) []int {
		var at []int
		for i, b := range blocks {
			if _, ok := b["cache_control"].(map[string]string); ok {
				at = append(at, i)
			}
		}
		return at
	}
	earlier, err := renderClaudePrompt(loop(20, "summarize so far"))
	if err != nil {
		t.Fatal(err)
	}
	later, err := renderClaudePrompt(loop(24, "summarize again"))
	if err != nil {
		t.Fatal(err)
	}
	if historyEnd(loop(20, "x").Messages) != 0 {
		t.Fatal("the loop has a history")
	}
	first, second := marked(earlier), marked(later)
	if len(first) != 1 || len(second) != 1 || first[0] == 0 || second[0] < first[0] || second[0]-first[0] > 20 {
		t.Fatalf("marks: earlier %v, later %v", first, second)
	}
	for i := 0; i <= first[0]; i++ {
		if earlier[i]["text"] != later[i]["text"] {
			t.Fatalf("block %d differs:\n%q\n%q", i, earlier[i]["text"], later[i]["text"])
		}
	}
	if strings.Contains(earlier[first[0]]["text"].(string), "summarize") {
		t.Fatalf("the marked block holds the last message")
	}
	// the last message is never in a chunk, when it would end one either
	even := loop(15, "summarize at the end of a chunk")
	even.Messages = append([]Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "context"}}}}, even.Messages...)
	if len(even.Messages)%historyChunk != 0 {
		t.Fatalf("%d messages", len(even.Messages))
	}
	blocks := mustRender(t, even)
	if at := marked(blocks); len(at) != 1 || strings.Contains(blocks[at[0]]["text"].(string), "summarize") {
		t.Fatalf("marks %v in %v", at, blocks)
	}
	// a loop shorter than a chunk keeps the mark on the instructions
	if at := marked(mustRender(t, loop(3, "x"))); len(at) != 1 || at[0] != 0 {
		t.Fatalf("short loop marks %v", at)
	}
}

func mustRender(t *testing.T, req *Request) []map[string]any {
	t.Helper()
	blocks, err := renderClaudePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}
