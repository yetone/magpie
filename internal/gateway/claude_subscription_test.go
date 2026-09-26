package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestClaudeSubscriptionPromptKeepsForeignHarnessOutOfSystem(t *testing.T) {
	r := &Request{
		System:   "You are an expert coding assistant operating inside pi\nsee docs/custom-provider.md and docs/packages.md",
		Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hello"}}}},
	}
	blocks, err := renderClaudePrompt(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(blocks)
	s := string(b)
	if !strings.Contains(s, "external_system_instructions") || !strings.Contains(s, "operating inside pi") || !strings.Contains(s, "Human: hello") {
		t.Fatalf("prompt lost content: %s", b)
	}
	// renderClaudePrompt is the user content passed to the genuine CLI. The
	// foreign harness is never supplied through --system-prompt, where
	// Anthropic's subscription classifier rejects it.
	args := strings.Join(claudeCLIArgs("claude-sonnet-5", `{}`, "medium", false), " ")
	if strings.Contains(args, "system-prompt") {
		t.Fatal("Claude bridge must retain the genuine Claude Code preset")
	}
	for _, want := range []string{"--input-format stream-json", "--include-partial-messages", "--strict-mcp-config", "--effort medium"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing CLI contract %q in %q", want, args)
		}
	}
}

func TestCleanClaudeEnvRemovesGatewayOverrides(t *testing.T) {
	got := cleanClaudeEnv([]string{
		"PATH=/bin", "ANTHROPIC_BASE_URL=http://127.0.0.1:3425",
		"ANTHROPIC_API_KEY=x", "ANTHROPIC_AUTH_TOKEN=y", "CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_SSE_PORT=9999", "KEEP=yes",
	})
	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{
		"ANTHROPIC_BASE_URL=", "ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CLAUDECODE=",
		"CLAUDE_CODE_ENTRYPOINT=", "CLAUDE_CODE_SSE_PORT=",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("kept %s in %q", forbidden, joined)
		}
	}
	for _, want := range []string{"PATH=/bin", "KEEP=yes", "ENABLE_CLAUDEAI_MCP_SERVERS=0", "DISABLE_AUTO_COMPACT=1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %q", want, joined)
		}
	}
}

// fakeClaude answers each line it is given with the process it runs in and
// how many turns that process has had, as Claude Code's stream-json does.
func fakeClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
n=0
while read -r line; do
  n=$((n+1))
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pid '$$' turn '$n'"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A conversation's next turn goes to the Claude Code that had its last,
// told only what the user said since; another conversation, or the same one
// with its reply changed, gets a Claude Code of its own.
func TestClaudeRunKeptForTheNextTurn(t *testing.T) {
	fakeClaude(t)
	s := New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	ask := func(msgs string) string {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"be brief","messages":` + msgs + `}`
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

	first := ask(`[` + msg("user", "hi") + `]`)
	pid, _, _ := strings.Cut(strings.TrimPrefix(first, "pid "), " ")
	if !strings.HasSuffix(first, "turn 1") {
		t.Fatalf("first: %q", first)
	}
	second := ask(`[` + msg("user", "hi") + `,` + msg("assistant", " "+first+"\n") + `,` + msg("user", "and?") + `]`)
	if second != "pid "+pid+" turn 2" {
		t.Fatalf("second: %q, first %q", second, first)
	}
	if other := ask(`[` + msg("user", "bye") + `,` + msg("assistant", second) + `,` + msg("user", "and?") + `]`); strings.Contains(other, "pid "+pid) {
		t.Fatalf("another conversation: %q", other)
	}
	third := ask(`[` + msg("user", "hi") + `,` + msg("assistant", first) + `,` + msg("user", "and?") + `,` + msg("assistant", "edited") + `,` + msg("user", "so?") + `]`)
	if strings.Contains(third, "pid "+pid) {
		t.Fatalf("an edited reply: %q", third)
	}
}

// An agent's run that fails after it began answering ends the stream with
// the error alone, not with a stop that reads as a finished reply.
func TestSubscriptionStreamErrorIsTheEnd(t *testing.T) {
	s := New()
	for _, from := range []provider.Protocol{provider.Anthropic, provider.Chat, provider.Responses} {
		start := func(ctx context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			ch := make(chan Event, 3)
			ch <- Event{Kind: KStart}
			ch <- Event{Kind: KText, Text: "half"}
			ch <- Event{Kind: KError, Text: "it died"}
			close(ch)
			return &subscriptionRun{bridge: s.subscription}, ch, nil
		}
		body := `{"model":"m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}],"input":"hi"}`
		rec := httptest.NewRecorder()
		var u Usage
		code, failed := s.serveSubscription(rec, httptest.NewRequest("POST", "/", strings.NewReader(body)), from, "Agent", "m", []byte(body), &u, start)
		out := rec.Body.String()
		if code != 200 || failed != "it died" || !strings.Contains(out, "it died") {
			t.Fatalf("%s: %d %q\n%s", from, code, failed, out)
		}
		for _, end := range []string{"message_stop", "[DONE]", `"stop"`, "response.completed"} {
			if strings.Contains(out, end) {
				t.Fatalf("%s: %s after the error:\n%s", from, end, out)
			}
		}
	}
}

// Claude Code's own WebSearch runs inside the turn: the client hears one
// message, with no call it did not offer, and the args ask for WebSearch
// only when the client offered a web search.
func TestClaudeOwnWebSearchStaysInside(t *testing.T) {
	body := `{"model":"claude-sonnet-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}]}`
	req, err := parseAnthropic([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !req.WebSearch || len(req.Tools) != 0 {
		t.Fatalf("web search not noted: %+v", req)
	}
	on := strings.Join(claudeCLIArgs("m", `{}`, "", true), "\x00")
	off := strings.Join(claudeCLIArgs("m", `{}`, "", false), "\x00")
	if !strings.Contains(on, "--tools\x00WebSearch\x00") || !strings.Contains(off, "--tools\x00\x00") {
		t.Fatalf("tools: %q / %q", on, off)
	}

	ev := func(e string) string { return `{"type":"stream_event","event":` + e + `}` }
	lines := []string{
		ev(`{"type":"message_start","message":{"id":"m1","model":"x","usage":{"input_tokens":10}}}`),
		ev(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look. "}}`),
		ev(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"WebSearch"}}`),
		ev(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"go\"}"}}`),
		ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		ev(`{"type":"message_stop"}`),
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"results"}]}}`,
		ev(`{"type":"message_start","message":{"id":"m2","model":"x","usage":{"input_tokens":30}}}`),
		ev(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Go 1.27.1"}}`),
		ev(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`),
		ev(`{"type":"message_stop"}`),
	}
	run := &subscriptionRun{}
	seg := run.attach()
	go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var got []string
	var usage Usage
	for e := range seg {
		switch e.Kind {
		case KStart:
			got = append(got, "start:"+e.MsgID)
			usage.add(e.Usage)
		case KText:
			got = append(got, "text:"+e.Text)
		case KToolStart, KToolArgs:
			got = append(got, "tool:"+e.Name+e.Text)
		case KStop:
			got = append(got, "stop:"+e.Stop)
		case KUsage:
			usage.add(e.Usage)
		}
	}
	if s := strings.Join(got, "|"); s != "start:m1|text:Let me look. |text:Go 1.27.1|stop:stop" {
		t.Fatalf("events: %s", s)
	}
	if usage.Input != 40 || usage.Output != 12 {
		t.Fatalf("usage: %+v", usage)
	}
}
