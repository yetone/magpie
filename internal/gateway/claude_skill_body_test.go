package gateway

import (
	"os"
	"strings"
	"testing"
)

// Claude Code answers a Skill call with "Launching skill: pdf" and sends
// the skill's SKILL.md after the results, as a text block headed "Base
// directory for this skill: …". The run's model is handed it in the
// Skill call's result, and the user is not said to have sent it.
func TestSkillBodyReachesTheSkillResult(t *testing.T) {
	body := "Base directory for this skill: /home/u/.claude/skills/pdf\n\n# PDF\n\nUse pdftotext."
	msgs := []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "read a.pdf"}}},
		{Role: "assistant", Parts: []Part{
			{Kind: ToolCall, CallID: "c1", Name: "Read"},
			{Kind: ToolCall, CallID: "c2", Name: "Skill"},
		}},
		{Role: "user", Parts: []Part{
			{Kind: ToolResult, CallID: "c1", Text: "a.pdf is binary"},
			{Kind: ToolResult, CallID: "c2", Text: "Launching skill: pdf"},
			{Kind: Text, Text: body},
		}},
	}
	results := msgs[2].Parts[:2]
	got := withSkillBodies(results, msgs)
	if !strings.Contains(got[1].Text, "Use pdftotext.") || !strings.HasPrefix(got[1].Text, "Launching skill: pdf") {
		t.Fatalf("the Skill result doesn't carry the skill's body: %q", got[1].Text)
	}
	if strings.Contains(got[0].Text, "pdftotext") {
		t.Fatalf("the body went to the Read result: %q", got[0].Text)
	}
	if results[1].Text != "Launching skill: pdf" {
		t.Fatal("the request's own result was changed in place")
	}
	if said := saidWithResults(msgs); said != "" {
		t.Fatalf("the skill's body went to the run as the user's: %q", said)
	}

	// in a reminder, and with no Skill call to match: the last result
	msgs[1].Parts[1].Name = "Other"
	msgs[2].Parts[2].Text = "<system-reminder>\n" + body + "\n</system-reminder>"
	got = withSkillBodies(results, msgs)
	if !strings.HasSuffix(got[1].Text, "Use pdftotext.") {
		t.Fatalf("no Skill call: %q", got[1].Text)
	}

	// nothing after the results: the same results
	msgs[2].Parts = msgs[2].Parts[:2]
	if got := withSkillBodies(results, msgs); &got[0] != &results[0] {
		t.Fatal("no body, yet the results were copied")
	}
}

// Two skills launched at once each get their own body.
func TestSkillBodiesTwoSkills(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Parts: []Part{
			{Kind: ToolCall, CallID: "a", Name: "Skill"},
			{Kind: ToolCall, CallID: "b", Name: "Skill"},
		}},
		{Role: "user", Parts: []Part{
			{Kind: ToolResult, CallID: "a", Text: "Launching skill: pdf"},
			{Kind: ToolResult, CallID: "b", Text: "Launching skill: xlsx"},
			{Kind: Text, Text: "Base directory for this skill: /s/pdf\n\nPDF body"},
			{Kind: Text, Text: "Base directory for this skill: /s/xlsx\n\nXLSX body"},
		}},
	}
	got := withSkillBodies(msgs[1].Parts[:2], msgs)
	if !strings.HasSuffix(got[0].Text, "PDF body") || !strings.HasSuffix(got[1].Text, "XLSX body") {
		t.Fatalf("%q / %q", got[0].Text, got[1].Text)
	}
}

// The request Claude Code 2.1.316 sent after a Skill call, as it came
// (paths shortened): the body reaches the run with the Skill result.
func TestSkillBodyRealClaudeCode(t *testing.T) {
	b, err := os.ReadFile("testdata/claude-code-skill-launch.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := parseAnthropic(b)
	if err != nil {
		t.Fatal(err)
	}
	last := r.Messages[len(r.Messages)-1]
	var results []Part
	for _, p := range last.Parts {
		if p.Kind == ToolResult {
			results = append(results, p)
		}
	}
	got := withTaskNotices(withSkillBodies(results, r.Messages), r.Messages)
	if len(got) != 1 || !strings.HasPrefix(got[0].Text, "Launching skill: magpie-skills\n\nBase directory for this skill: ") ||
		!strings.Contains(got[0].Text, "/Users/u/.claude/skills/magpie-skills/.skills/greet-user/SKILL.md") {
		t.Fatalf("the run is handed: %+v", got)
	}
	if said := saidWithResults(r.Messages); said != "" {
		t.Fatalf("said as the user's: %q", said)
	}
}
