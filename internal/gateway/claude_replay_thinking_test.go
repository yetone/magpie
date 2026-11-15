package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A conversation told to a Claude Code run in words leaves the replies'
// thinking out. Written into the history as the assistant's text, earlier
// thinking reached the model as words in a user message, which Anthropic
// refused as reasoning extraction, and every run started anew after the
// refusal told it again (#6623; solllin on X). The words, tool calls and
// tool results stay.
func TestClaudeReplayLeavesThinkingOut(t *testing.T) {
	const secret = "PRIVATE-CHAIN-OF-THOUGHT-6623"
	const sealed = "U0VBTEVELVJFREFDVEVELTY2MjM="
	conv := `[` +
		`{"role":"user","content":"fix the bug"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"` + secret + ` one","signature":"sig1"},{"type":"redacted_thinking","data":"` + sealed + `"},{"type":"text","text":"reading the file"},{"type":"tool_use","id":"call_1","name":"read","input":{"path":"a.go"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"package a"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"` + secret + ` two","signature":"sig2"},{"type":"text","text":"fixed it"}]},` +
		`{"role":"user","content":"now add a test"}]`
	fields := `"tools":[{"name":"read","input_schema":{"type":"object"}}],`
	told := func(t *testing.T, blocks []map[string]any) string {
		t.Helper()
		var all strings.Builder
		for _, b := range blocks {
			s, _ := b["text"].(string)
			all.WriteString(s)
			all.WriteString("\n")
		}
		return all.String()
	}
	check := func(t *testing.T, s string, want ...string) {
		t.Helper()
		if strings.Contains(s, secret) || strings.Contains(s, sealed) {
			t.Fatalf("the replay carries the replies' thinking:\n%s", s)
		}
		for _, w := range want {
			if !strings.Contains(s, w) {
				t.Fatalf("the replay lost %q:\n%s", w, s)
			}
		}
	}
	for _, marks := range []bool{false, true} {
		req := clientCacheRequest(t, fields, conv)
		if !marks {
			req.clientCache = nil
		}
		blocks, err := renderClaudePrompt(req)
		if err != nil {
			t.Fatal(err)
		}
		check(t, told(t, blocks), "fix the bug", "reading the file", "[tool call", "call_1", "package a", "fixed it", "now add a test")
	}

	// the messages since a reply, told to a run that has the conversation
	// (the -p run's next message, and the interactive run's typed turn)
	req := clientCacheRequest(t, fields, conv)
	check(t, told(t, renderClaudeTurn(req.Messages[1:], req.Tools)), "reading the file", "[tool call", "package a", "fixed it", "now add a test")

	// the transcript a new interactive run resumes
	dir := t.TempDir()
	long := clientCacheRequest(t, fields, strings.TrimSuffix(conv, "]")+`,{"role":"assistant","content":[{"type":"thinking","thinking":"`+secret+` three","signature":"sig3"},{"type":"text","text":"added"}]},{"role":"user","content":"thanks"}]`)
	if replaySession([]string{dir}, dir, "claude-sonnet-5", long) == nil {
		t.Fatal("no transcript was written")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("%d transcripts", len(files))
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	check(t, string(b), "fix the bug", "reading the file", "package a", "fixed it", "now add a test", "added")
}
