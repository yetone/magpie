package gateway

import (
	"strings"
	"testing"
)

// #5403 (miroku9): Claude Desktop appends its own annotations to what the
// run reads as tool results — the image-scaling note a >2000px read gets,
// a skill's base directory, ToolSearch's "Tool loaded." — and magpie took
// each for words the user sent mid-turn: the model was told "The user sent
// a new message while you were working: [Image: original 2200x680 …]"
// though the user had sent nothing, and answered the phantom message. The
// annotations are the agent's own (the image itself is already in the
// result), so they are agent notices, and go where the rest go.
func TestDesktopAnnotationsAreAgentNotices(t *testing.T) {
	for _, s := range []string{
		"[Image: original 2200x680, displayed at 2000x618. Multiply coordinates by 1.10 to map to original image.]",
		"Base directory for this skill: /home/user/.claude/skills/pdf",
		"Tool loaded.",
	} {
		if !agentNotice(s) {
			t.Errorf("%q was taken for the user's words", s)
		}
	}
}

// and they neither reach the run as a mid-turn message, nor split a tool
// result off as the user's own words.
func TestDesktopAnnotationsStayWithTheResult(t *testing.T) {
	note := "[Image: original 2200x680, displayed at 2000x618. Multiply coordinates by 1.10 to map to original image.]"
	msgs := []Message{
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, CallID: "c1", Name: "Read"}}},
		{Role: "user", Parts: []Part{
			{Kind: ToolResult, CallID: "c1", Text: "the png's bytes as text"},
			{Kind: Text, Text: note},
		}},
	}
	if said := saidWithResults(msgs); said != "" {
		t.Errorf("the annotation went to the run as the user's: %q", said)
	}
	var sb strings.Builder
	renderParts(nil, &sb, msgs[len(msgs)-1].Parts, nil)
	if got := sb.String(); !strings.Contains(got, "[end of tool result id=c1]") || strings.Contains(got, "[user message]") {
		t.Errorf("the result should close with the annotation after it, not as the user's words; got %q", got)
	}
}

// a real mid-turn word still rides along, annotations or not.
func TestDesktopAnnotationsLeaveTheUsersWords(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, CallID: "c1", Name: "Read"}}},
		{Role: "user", Parts: []Part{
			{Kind: ToolResult, CallID: "c1", Text: "the png's bytes as text"},
			{Kind: Text, Text: "[Image: original 2200x680, displayed at 2000x618. Multiply coordinates by 1.10 to map to original image.]"},
			{Kind: Text, Text: "now zoom into the top left corner please"},
		}},
	}
	said := saidWithResults(msgs)
	if said != "now zoom into the top left corner please" {
		t.Errorf("said %q, want the user's own words only", said)
	}
}

// #5403 (miroku9): wide images read in parallel bring a note each, and
// they arrive as separate parts or as one text joined by blank lines
// (verbatim from the inner session's queue); neither is the user's.
func TestParallelImageNotesAreAgentNotices(t *testing.T) {
	note := "[Image: original 2400x1350, displayed at 2000x1125. Multiply coordinates by 1.20 to map to original image.]"
	joined := note + "\n\n\n" + note + "\n\n\n" + note
	if !agentNotice(joined) {
		t.Errorf("three joined notes were taken for the user's words")
	}
	if agentNotice(note + "\n\n\nwhy is the second one blurry?") {
		t.Errorf("the user's words after a note were taken for the agent's")
	}
	for name, parts := range map[string][]Part{
		"one text": {{Kind: Text, Text: joined}},
		"parts":    {{Kind: Text, Text: note}, {Kind: Text, Text: note}, {Kind: Text, Text: note}},
	} {
		msgs := []Message{
			{Role: "assistant", Parts: []Part{
				{Kind: ToolCall, CallID: "c1", Name: "Read"},
				{Kind: ToolCall, CallID: "c2", Name: "Read"},
				{Kind: ToolCall, CallID: "c3", Name: "Read"},
			}},
			{Role: "user", Parts: append([]Part{
				{Kind: ToolResult, CallID: "c1", Text: "a"},
				{Kind: ToolResult, CallID: "c2", Text: "b"},
				{Kind: ToolResult, CallID: "c3", Text: "c"},
			}, parts...)},
		}
		if said := saidWithResults(msgs); said != "" {
			t.Errorf("%s: the notes went to the run as the user's: %q", name, said)
		}
		var sb strings.Builder
		renderParts(nil, &sb, msgs[1].Parts, nil)
		if strings.Contains(sb.String(), "[user message]") {
			t.Errorf("%s: the notes were marked as the user's: %q", name, sb.String())
		}
	}
}
