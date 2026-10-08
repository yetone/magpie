package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A session exported as Markdown (#1276) is its whole conversation: who
// said what, in order, under a heading that says what the session was; a
// tool's call by its name and arguments, its result, the thinking and the
// agent's own notes folded; a part far longer than the page shows is kept
// whole, and the file is left as it was.
func TestMarkdownClaude(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sess1.jsonl")
	long := strings.Repeat("y", partMax*3)
	writeLines(t, file,
		claudeUser(1, `"<command-name>/init</command-name>"`),
		claudeUser(2, `"Fix the login bug"`),
		claudeBlocks("m1", 3, `{"type":"thinking","thinking":"The user wants the login fixed."}`),
		claudeBlocks("m1", 4, `{"type":"text","text":"I'll read the file first."}`),
		claudeBlocks("m1", 5, `{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a/login.go"}}`),
		claudeUser(9, `[{"type":"tool_result","tool_use_id":"t1","content":"package login\n`+"```"+`go\nfunc x() {}\n`+"```"+`"}]`),
		claudeBlocks("m2", 12, `{"type":"text","text":"Fixed: the check was inverted."}`),
		claudeUser(20, `"`+long+`"`),
	)
	before, _ := os.ReadFile(file)
	start := time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local)
	s := Session{Agent: "claude", ID: "sess1", Path: file, Cwd: "/work/app", Title: "Fix the login bug",
		Start: start, Last: start.Add(time.Hour), Models: []Model{{Model: "claude-opus-5-5"}}}
	var b bytes.Buffer
	if err := WriteMarkdown(&b, s, "Claude Code"); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	in := func(parts ...string) {
		t.Helper()
		at := 0
		for _, p := range parts {
			i := strings.Index(md[at:], p)
			if i < 0 {
				t.Fatalf("no %q after byte %d in\n%s", p, at, md[:min(len(md), 3000)])
			}
			at += i + len(p)
		}
	}
	in("# Fix the login bug\n",
		"- **Agent:** Claude Code\n",
		"- **Session ID:** `sess1`\n",
		"- **Folder:** /work/app\n",
		"- **Started:** 2026-10-08 09:00:00",
		"- **Models:** claude-opus-5-5\n",
		"- **File:** "+file+"\n",
		"## User\n",
		"<summary>Context</summary>", "```\n<command-name>/init</command-name>\n```",
		"Fix the login bug\n",
		"## Assistant\n",
		"<summary>Thinking</summary>\n\nThe user wants the login fixed.\n",
		"I'll read the file first.\n",
		"**Tool call: Read**\n\n```json\n{\n  \"file_path\": \"/a/login.go\"\n}\n```\n",
		// the result's own fence doesn't close the one it is in
		"<summary>Tool result</summary>\n\n````\npackage login\n```go\nfunc x() {}\n```\n````\n",
		"Fixed: the check was inverted.\n",
		"## User\n",
		long+"\n",
	)
	if n := strings.Count(md, "## Assistant"); n != 1 {
		t.Fatalf("%d Assistant headings: a tool's result split the turn\n%s", n, md)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("the session file was changed")
	}
}

// Every agent whose conversation the page shows is exported the same way:
// Codex's rollout and Pi's branch.
func TestMarkdownCodexAndPi(t *testing.T) {
	dir := t.TempDir()
	codex := filepath.Join(dir, "rollout.jsonl")
	writeLines(t, codex,
		`{"type":"session_meta","payload":{"id":"x"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\"]}","call_id":"c1"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"a.go\nb.go"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Two files."}]}}`,
	)
	pi := filepath.Join(dir, "pi.jsonl")
	writeLines(t, pi,
		`{"type":"session","version":3,"id":"s","cwd":"/w"}`,
		`{"type":"message","id":"e2","parentId":null,"message":{"role":"user","content":[{"type":"text","text":"Rename foo"}]}}`,
		`{"type":"message","id":"e3","parentId":"e2","message":{"role":"assistant","content":[{"type":"text","text":"an answer left behind"}]}}`,
		`{"type":"message","id":"e4","parentId":"e2","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"grep -r foo"}}]}}`,
		`{"type":"message","id":"e5","parentId":"e4","message":{"role":"toolResult","toolCallId":"t1","toolName":"bash","content":[{"type":"text","text":"a.go: foo()"}]}}`,
		`{"type":"message","id":"e6","parentId":"e5","message":{"role":"assistant","content":[{"type":"text","text":"Renamed."}]}}`,
	)
	for _, c := range []struct {
		s    Session
		want []string
	}{
		{Session{Agent: "codex", ID: "x", Path: codex}, []string{"## User\n\nList the files\n", "## Assistant\n", "**Tool call: shell**", "<summary>Tool result</summary>\n\n```\na.go\nb.go\n```", "Two files.\n"}},
		{Session{Agent: "pi", ID: "s", Path: pi}, []string{"## User\n\nRename foo\n", "## Assistant\n", "**Tool call: bash**", "<summary>Tool result: bash</summary>", "Renamed.\n"}},
	} {
		var b bytes.Buffer
		if err := WriteMarkdown(&b, c.s, c.s.Agent); err != nil {
			t.Fatalf("%s: %v", c.s.Agent, err)
		}
		md, at := b.String(), 0
		for _, p := range c.want {
			i := strings.Index(md[at:], p)
			if i < 0 {
				t.Fatalf("%s: no %q in order in\n%s", c.s.Agent, p, md)
			}
			at += i + len(p)
		}
		if strings.Contains(md, "left behind") {
			t.Fatalf("%s: a branch left behind was exported\n%s", c.s.Agent, md)
		}
	}
}

// An agent whose conversation magpie can't read has nothing to export.
func TestMarkdownUnsupported(t *testing.T) {
	var b bytes.Buffer
	if err := WriteMarkdown(&b, Session{Agent: "opencode", ID: "x", Path: "/x"}, "OpenCode"); err != ErrNoTranscript {
		t.Fatalf("opencode: %v", err)
	}
	if b.Len() != 0 {
		t.Fatalf("wrote %q", b.String())
	}
}
