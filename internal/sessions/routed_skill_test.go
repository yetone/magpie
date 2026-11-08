package sessions

import (
	"testing"
	"time"
)

// A Read of a skill's SKILL.md through magpie's skill router (the
// library's magpie-skills entry) is the use of that skill.
func TestRoutedSkill(t *testing.T) {
	for p, want := range map[string]string{
		"/Users/a/.claude/skills/magpie-skills/.skills/pdf/SKILL.md":       "pdf",
		`C:\Users\a\.codex\skills\magpie-skills\.skills\xlsx\SKILL.md`:     "xlsx",
		"/Users/a/.claude/skills/magpie-skills/SKILL.md":                   "",
		"/Users/a/.claude/skills/magpie-skills/.skills/pdf/scripts/run.sh": "",
		"/Users/a/.claude/skills/pdf/SKILL.md":                             "",
		"/Users/a/other/.skills/pdf/SKILL.md":                              "",
		"":                                                                 "",
	} {
		if got := routedSkill(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
}

// In a Claude Code session, the Skill call that opens the router counts
// as a call of the Skill tool and of no skill, and the Read of a skill
// behind it counts as that skill, on both of claudeLine's paths.
func TestClaudeRoutedSkillCounted(t *testing.T) {
	for _, fast := range []bool{false, true} {
		s := &state{}
		at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		open := `[{"type":"tool_use","id":"t1","name":"Skill","input":{"skill":"magpie-skills"}}]`
		read := `[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/Users/u/.claude/skills/magpie-skills/.skills/pdf/SKILL.md"}}]`
		claudeLine(s, claudeUsageLine(at, "a", "req-a", "m", Tokens{1, 1, 0, 0, 0}, open, fast), true)
		claudeLine(s, claudeUsageLine(at.Add(time.Second), "b", "req-b", "m", Tokens{1, 1, 0, 0, 0}, read, fast), true)
		d := s.Days.of(dateOf(at))
		if d.Tools.of("Skill") != 1 || d.Tools.of("Read") != 1 || d.Skills.of("pdf") != 1 || len(d.Skills) != 1 {
			t.Fatalf("fast=%v: tools=%v skills=%v", fast, d.Tools, d.Skills)
		}
	}
}
