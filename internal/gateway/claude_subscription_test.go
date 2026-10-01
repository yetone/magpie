package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

// TestClaudeCLIEffortXHigh: xhigh is a level of Claude Code's own, sent as
// output_config.effort "xhigh"; turned into max it used more of the plan (#385).
func TestClaudeCLIEffortXHigh(t *testing.T) {
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		args := strings.Join(claudeCLIArgs("claude-opus-5-5", `{}`, e, false), " ")
		if !strings.Contains(args, "--effort "+e+" ") {
			t.Fatalf("effort %s: %q", e, args)
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
		body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"be brief","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
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

// A one-off ask — a lone message and no tools, as an agent's title or the
// router's classifier sends — leaves no Claude Code waiting for a next
// turn that won't come; a conversation already going on keeps its run.
func TestClaudeOneOffAskNotKept(t *testing.T) {
	fakeClaude(t)
	s := New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	ask := func(msgs string) string {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"messages":` + msgs + `}`
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
	idle := func() int {
		s.subscription.mu.Lock()
		defer s.subscription.mu.Unlock()
		return len(s.subscription.idle)
	}

	first := ask(`[` + msg("user", "a title for this") + `]`)
	if n := idle(); n != 0 {
		t.Fatalf("a one-off ask left %d runs waiting", n)
	}
	second := ask(`[` + msg("user", "a title for this") + `,` + msg("assistant", first) + `,` + msg("user", "and?") + `]`)
	if n := idle(); n != 1 {
		t.Fatalf("a conversation going on: %d runs waiting, want 1", n)
	}
	pid, _, _ := strings.Cut(strings.TrimPrefix(second, "pid "), " ")
	if third := ask(`[` + msg("user", "a title for this") + `,` + msg("assistant", first) + `,` + msg("user", "and?") + `,` + msg("assistant", second) + `,` + msg("user", "so?") + `]`); third != "pid "+pid+" turn 2" {
		t.Fatalf("third: %q, second %q", third, second)
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

func TestClaudeLimits(t *testing.T) {
	got := claudeLimits(json.RawMessage(`{"status":"allowed","resetsAt":1800000000,"rateLimitType":"five_hour","utilization":0.42,
		"unifiedWindows":{"five_hour":{"utilization":0.42,"resetsAt":1800000000},"seven_day":{"utilization":0.1,"resetsAt":1800500000}}}`))
	slices.SortFunc(got, func(a, b provider.ClaudeLimit) int { return strings.Compare(a.Kind, b.Kind) })
	want := []provider.ClaudeLimit{{Kind: "five_hour", Used: 0.42, ResetsAt: 1800000000}, {Kind: "seven_day", Used: 0.1, ResetsAt: 1800500000}}
	if !slices.Equal(got, want) {
		t.Fatalf("windows: %+v", got)
	}
	// without the windows: the one it is about, and one turned away is full
	if got := claudeLimits(json.RawMessage(`{"status":"allowed_warning","rateLimitType":"seven_day","utilization":0.9,"resetsAt":5}`)); !slices.Equal(got, []provider.ClaudeLimit{{Kind: "seven_day", Used: 0.9, ResetsAt: 5}}) {
		t.Fatalf("top level: %+v", got)
	}
	if got := claudeLimits(json.RawMessage(`{"status":"rejected","rateLimitType":"five_hour","resetsAt":7}`)); !slices.Equal(got, []provider.ClaudeLimit{{Kind: "five_hour", Used: 1, ResetsAt: 7}}) {
		t.Fatalf("rejected: %+v", got)
	}
	if got := claudeLimits(json.RawMessage(`{"status":"allowed"}`)); len(got) != 0 {
		t.Fatalf("nothing said: %+v", got)
	}
}

// A conversation switched to another model and back (KevinXC on Discord)
// has a new run carry it on: the one left waiting before the switch is
// let go then, not kept idleLongest for a turn that can't come back to it —
// a Claude Code process more with every switch.
func TestClaudeRunLetGoWhenTheConversationMovedOn(t *testing.T) {
	fakeClaude(t)
	s := New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	ask := func(model, msgs string) string {
		t.Helper()
		body := `{"model":"` + model + `","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, model, []byte(body), &u); code != 200 {
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
	waiting := func() []string {
		s.subscription.mu.Lock()
		defer s.subscription.mu.Unlock()
		var pids []string
		for _, run := range s.subscription.idle {
			pids = append(pids, strconv.Itoa(run.cmd.Process.Pid))
		}
		return pids
	}
	pidOf := func(said string) string { pid, _, _ := strings.Cut(strings.TrimPrefix(said, "pid "), " "); return pid }

	conv := msg("user", "one")
	first := ask("claude-sonnet-5", `[`+conv+`]`)
	conv += `,` + msg("assistant", first) + `,` + msg("user", "two")
	if w := waiting(); len(w) != 1 || w[0] != pidOf(first) {
		t.Fatalf("after the first turn: %v, ran %q", w, first)
	}
	// a turn answered elsewhere, then back on the subscription
	conv += `,` + msg("assistant", "an answer from another provider") + `,` + msg("user", "three")
	back := ask("claude-sonnet-5", `[`+conv+`]`)
	if pidOf(back) == pidOf(first) {
		t.Fatalf("the run before the switch answered after it: %q", back)
	}
	if w := waiting(); len(w) != 1 || w[0] != pidOf(back) {
		t.Fatalf("after switching back: %v waiting, want only %s", w, pidOf(back))
	}
	// and another Claude model of the same account, for the next turn
	conv += `,` + msg("assistant", back) + `,` + msg("user", "four")
	other := ask("claude-opus-5-5", `[`+conv+`]`)
	if w := waiting(); len(w) != 1 || w[0] != pidOf(other) {
		t.Fatalf("after another Claude model: %v waiting, want only %s", w, pidOf(other))
	}
	// a conversation of its own keeps its run beside it
	ask("claude-sonnet-5", `[`+msg("user", "elsewhere")+`,`+msg("assistant", "x")+`,`+msg("user", "y")+`]`)
	if w := waiting(); len(w) != 2 {
		t.Fatalf("two conversations: %v waiting", w)
	}
}

func TestClaudeBridgeKeepsNoSessionsOnDisk(t *testing.T) {
	args := claudeCLIArgs("m", `{}`, "", false)
	if !slices.Contains(args, "--no-session-persistence") || !slices.Contains(args, "-p") {
		t.Fatalf("bridge runs must not persist sessions: %q", args)
	}
}

func TestSweepBridgeProjectsTakesOnlyTheBridgesFolders(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real", "T")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	real = evalSymlinks(real) // what Claude Code names a folder after
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(base, "claude")
	projects := filepath.Join(claudeDir, "projects")
	gone := []string{
		claudeProjectName(filepath.Join(real, "magpie-claude-1097091858")),
		claudeProjectName(filepath.Join(link, "T", "magpie-claude-42")),
	}
	kept := []string{
		claudeProjectName(filepath.Join(real, "magpie-claude-")),
		claudeProjectName(filepath.Join(real, "magpie-claude-12-x")),
		claudeProjectName(filepath.Join(real, "other")),
		claudeProjectName("/Users/me/code/magpie-claude-7"),
	}
	for _, d := range append(append([]string{}, gone...), kept...) {
		if err := os.MkdirAll(filepath.Join(projects, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sweepBridgeProjects(claudeDir, filepath.Join(link, "T"))
	for _, d := range gone {
		if _, err := os.Stat(filepath.Join(projects, d)); !os.IsNotExist(err) {
			t.Fatalf("%s should be swept", d)
		}
	}
	for _, d := range kept {
		if _, err := os.Stat(filepath.Join(projects, d)); err != nil {
			t.Fatalf("%s should be kept: %v", d, err)
		}
	}
	sweepBridgeProjects(filepath.Join(base, "missing"), real) // no projects folder: nothing to do
}

// Out of quota, Claude Code ends the turn with an error result and no
// message_stop, then waits on its next input: the reply is a 429 at once,
// streamed or not, so another account can take over (#177).
func TestClaudeQuotaResultEndsTheReply(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
while read -r line; do
  echo '{"type":"result","subtype":"success","is_error":true,"result":"You'"'"'ve hit your limit · resets 3am"}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for _, stream := range []string{"true", "false"} {
		body := `{"model":"claude-sonnet-5","max_tokens":100,"stream":` + stream + `,"messages":[{"role":"user","content":"ping"}]}`
		done := make(chan int, 1)
		go func() {
			var u Usage
			code, _ := s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u)
			done <- code
		}()
		select {
		case code := <-done:
			if code != 429 {
				t.Fatalf("stream=%s: status %d", stream, code)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("stream=%s: the reply waited on the CLI", stream)
		}
	}
}
