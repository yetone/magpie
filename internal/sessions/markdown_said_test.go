package sessions

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// An export that keeps only what was said (usrname1833 on Discord) has the
// user's words and the agent's, in order under who said them, and none of
// the work between: no thinking, tool call or result, and nothing the agent
// put in itself. The whole export of the same file still has all of it.
func TestMarkdownKeepSaid(t *testing.T) {
	file := filepath.Join(t.TempDir(), "said.jsonl")
	writeLines(t, file,
		claudeUser(1, `"<command-name>/init</command-name>"`),
		claudeUser(2, `"Fix the login bug"`),
		claudeBlocks("m1", 3, `{"type":"thinking","thinking":"SECRET-THOUGHT the user wants the login fixed."}`),
		claudeBlocks("m1", 4, `{"type":"text","text":"I'll read the file first."}`),
		claudeBlocks("m1", 5, `{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a/TOOL-ARG.go"}}`),
		claudeUser(9, `[{"type":"tool_result","tool_use_id":"t1","content":"TOOL-OUTPUT package login"}]`),
		claudeBlocks("m2", 12, `{"type":"text","text":"Fixed: the check was inverted."}`),
		claudeUser(20, `"Thanks"`),
	)
	s := Session{Agent: "claude", ID: "said", Path: file, Title: "Fix the login bug"}
	var whole, said bytes.Buffer
	if err := WriteMarkdownKept(&whole, s, "Claude Code", KeepAll); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkdownKept(&said, s, "Claude Code", KeepSaid); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"SECRET-THOUGHT", "TOOL-ARG", "TOOL-OUTPUT", "command-name", "<details>", "Tool call"} {
		if !strings.Contains(whole.String(), gone) {
			t.Fatalf("the whole export lost %q:\n%s", gone, whole.String())
		}
		if strings.Contains(said.String(), gone) {
			t.Fatalf("the said-only export kept %q:\n%s", gone, said.String())
		}
	}
	md, at := said.String(), 0
	for _, p := range []string{"# Fix the login bug\n", "*Only what the user and the agent said: thinking and tool calls are left out.*",
		"## User\n\nFix the login bug\n", "## Assistant\n\nI'll read the file first.\n", "\nFixed: the check was inverted.\n", "## User\n\nThanks\n"} {
		i := strings.Index(md[at:], p)
		if i < 0 {
			t.Fatalf("no %q in order after byte %d in\n%s", p, at, md)
		}
		at += i + len(p)
	}
	if n := strings.Count(md, "## Assistant"); n != 1 {
		t.Fatalf("%d Assistant headings, want the turn's words under one:\n%s", n, md)
	}
	if strings.Contains(whole.String(), "Only what the user and the agent said") {
		t.Fatal("the whole export says it left things out")
	}
}

// What a gateway recorded is kept the same way.
func TestGatewayMarkdownKeepSaid(t *testing.T) {
	tr := Transcript{Parts: []Part{
		{Role: "user", Kind: "text", Text: "rename foo"},
		{Role: "user", Kind: "context", Text: "SYSTEM-REMINDER"},
		{Role: "assistant", Kind: "thinking", Text: "SECRET-THOUGHT"},
		{Role: "assistant", Kind: "tool_use", Name: "bash", Text: `{"command":"TOOL-ARG"}`},
		{Role: "tool", Kind: "tool_result", Name: "bash", Text: "TOOL-OUTPUT"},
		{Role: "assistant", Kind: "text", Text: "Renamed."},
	}}
	var b bytes.Buffer
	if err := WriteGatewayMarkdownKept(&b, Session{Agent: "pi", ID: "g"}, "Pi", tr, KeepSaid); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	for _, gone := range []string{"SYSTEM-REMINDER", "SECRET-THOUGHT", "TOOL-ARG", "TOOL-OUTPUT"} {
		if strings.Contains(md, gone) {
			t.Fatalf("kept %q:\n%s", gone, md)
		}
	}
	if !strings.Contains(md, "## User\n\nrename foo\n") || !strings.Contains(md, "## Assistant\n\nRenamed.\n") {
		t.Fatalf("lost what was said:\n%s", md)
	}
}
