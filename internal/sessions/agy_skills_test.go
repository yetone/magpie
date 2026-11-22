package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// testdata/agy-skills is two conversations agy 1.5.13/1.5.14 had in a
// sandbox HOME against a fake Gemini endpoint, with a skill pdf-tools in
// the project's .agents/skills (paths written /work/proj and /home/u):
//
//   - agySkillRead: the model reads pdf-tools' SKILL.md with read_file, as
//     agy's system prompt tells it to, and then the builtin
//     antigravity_guide's through run_command (cat).
//   - agySkillSlash: the user typed "/pdf-tools summarize report.pdf" in
//     agy's TUI; agy put the skill in the prompt after a note naming it.
//
// @ifelseboyxx on X: skills used often in agy, by /skill and read by the
// model, and the stats' frequent skills stayed empty.
const (
	agySkillRead  = "062d6b50-17a2-4e5c-8414-e9cd5b8f98d2"
	agySkillSlash = "af21f329-ea97-45eb-9712-67c1969ffa62"
)

func TestAgySkills(t *testing.T) {
	inZone(t, 0)
	dir := putAgy(t)
	copyTree(t, "testdata/agy-skills", dir)
	Reset()
	transcript := func(id string) file {
		f := file{agent: "agy", path: filepath.Join(dir, "brain", id, ".system_generated", "logs", "transcript_full.jsonl"), main: true}
		if !stat(&f) {
			t.Fatalf("no transcript for %s", id)
		}
		return f
	}

	d := parseAgy(transcript(agySkillRead), nil).Days.of("2026-11-22")
	if d == nil || d.Tools.of("read_file") != 1 || d.Tools.of("run_command") != 1 {
		t.Fatalf("read: tools %+v", d)
	}
	if d.Skills.of("pdf-tools") != 1 || d.Skills.of("antigravity_guide") != 1 || len(d.Skills) != 2 {
		t.Fatalf("read: skills %v", d.Skills)
	}

	d = parseAgy(transcript(agySkillSlash), nil).Days.of("2026-11-22")
	if d == nil || d.Prompts != 1 || d.Skills.of("pdf-tools") != 1 || len(d.Skills) != 1 {
		t.Fatalf("slash: %+v", d)
	}

	o := statsAt(0, time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)).Overview("agy", "", "", 10)
	if o.Skills.Calls != 3 || o.Skills.Top[0].Name != "pdf-tools" || o.Skills.Top[0].Calls != 2 || o.Skills.Top[0].Agents["agy"] != 2 {
		t.Fatalf("overview skills %+v", o.Skills)
	}
}

// What agy adds after a prompt for a skill it puts in whole: the notes for
// /<name> and a mention count; one that only gives the path doesn't (the
// model's read counts it), nor do the user's own words.
func TestAgyAskedSkills(t *testing.T) {
	for _, c := range []struct {
		content string
		want    []string
	}{
		// agy 1.5.13's print mode, byte for byte
		{"<USER_REQUEST>\n/antigravity-guide how do I add a skill\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-11-22T10:35:41+08:00.\n\nThe user has mentioned some items in the form @[ITEM]. Here is extra information about the items that were mentioned by the user, in the order that they appear:\n\n/antigravity-guide is a [Slash Command]:\n<SKILL>The user has explicitly invoked the (antigravity-guide) skill. You must strictly follow the instructions in this skill to process their request. Here are its contents:\n# Guide</SKILL>\n</ADDITIONAL_METADATA>", []string{"antigravity-guide"}},
		{"<USER_REQUEST>\nx\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\n<SKILL>The user mentioned the (pdf-tools) skill. Here are its contents:\nbody</SKILL>\n</ADDITIONAL_METADATA>", []string{"pdf-tools"}},
		{"<USER_REQUEST>\nx\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\n<SKILL>The user requested you read and use the \"pdf-tools\" skill. The path to the skill file is:\n/a/pdf-tools/SKILL.md</SKILL>\n</ADDITIONAL_METADATA>", nil},
		{"<USER_REQUEST>\n<SKILL>The user has explicitly invoked the (typed) skill.\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\n</ADDITIONAL_METADATA>", nil},
		{"<USER_REQUEST>\nplease use /pdf-tools\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-11-22T10:35:41+08:00.\n</ADDITIONAL_METADATA>", nil},
	} {
		if got := agyAskedSkills(c.content); !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.content, got, c.want)
		}
	}
}

// The Antigravity app's language server is agy's and writes the same
// transcript beside its database: its tool calls and skills count the
// same. testdata/antigravity-2.30-tools' read_file, and agy's skill
// conversations' transcripts in the place of its own.
func TestAntigravityAppSkills(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	dir := filepath.Join(home, ".gemini", "antigravity")
	copyTree(t, "testdata/antigravity-2.30-tools", dir)
	Reset()
	f := file{agent: "antigravity", path: filepath.Join(dir, "conversations", agAppTools+".db"), main: true}
	if !stat(&f) {
		t.Fatal("no database")
	}
	if d := parseAntigravity(f).Days.of("2026-11-18"); d == nil || d.Tools.of("read_file") != 1 || len(d.Skills) != 0 {
		t.Fatalf("tools: %+v", d)
	}

	logs := filepath.Join(dir, "brain", agAppTools, ".system_generated", "logs", "transcript_full.jsonl")
	for id, want := range map[string]map[string]int{
		agySkillRead:  {"pdf-tools": 1, "antigravity_guide": 1},
		agySkillSlash: {"pdf-tools": 1},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "agy-skills", "brain", id, ".system_generated", "logs", "transcript_full.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(logs, b, 0o644); err != nil {
			t.Fatal(err)
		}
		stat(&f)
		d := parseAntigravity(f).Days.of("2026-11-22")
		if d == nil || len(d.Skills) != len(want) {
			t.Fatalf("%s: %+v", id, d)
		}
		for k, n := range want {
			if d.Skills.of(k) != n {
				t.Errorf("%s: skill %s %d, want %d", id, k, d.Skills.of(k), n)
			}
		}
	}
}
