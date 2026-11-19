package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ruleAgents puts every agent that takes rules in the sandbox, and says
// where each keeps one called name.
func ruleAgents(t *testing.T, h, name string) map[string]string {
	t.Helper()
	write(t, filepath.Join(h, ".cline/data/globalState.json"), "")
	holdCLI(t, h, "cline")
	holdCLI(t, h, "kiro-cli")
	return map[string]string{
		"claude":   filepath.Join(h, ".claude/rules", name+".md"),
		"cline":    filepath.Join(h, ".cline/rules", name+".md"),
		"kiro-cli": filepath.Join(h, ".kiro/steering", name+".md"),
	}
}

func ruleProblems(res *Result) map[string]string {
	m := map[string]string{}
	for _, p := range res.Problems {
		if strings.HasPrefix(p.What, "rule:") {
			m[p.Agent] = p.Error
		}
	}
	return m
}

// A rule with no paths is a plain Markdown file in each agent's own
// folder of rules, read in every conversation.
func TestRulesEachAgentsFile(t *testing.T) {
	h := sandbox(t)
	files := ruleAgents(t, h, "style")
	r := Rule{Name: "style", Body: "# Style\r\n\r\nTabs, not spaces.\n\n", Agents: []string{"claude", "cline", "kiro-cli"}}
	res, err := SaveRule("", r)
	if err != nil {
		t.Fatal(err)
	}
	if p := ruleProblems(res); len(p) != 0 {
		t.Fatalf("problems: %+v", p)
	}
	for id, f := range files {
		if got := read(t, f); got != "# Style\n\nTabs, not spaces.\n" {
			t.Errorf("%s's %s: %q", id, f, got)
		}
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Rules) != 1 || len(v.Rules[0].Files) != 3 || len(v.Rules[0].Problems) != 0 {
		t.Errorf("view: %+v", v.Rules)
	}
	var listed []string
	for _, av := range v.RuleAgents {
		listed = append(listed, av.ID)
		if av.Paths != (av.ID != "cline") || av.No != "" || av.Dir != filepath.Dir(files[av.ID]) {
			t.Errorf("agent view %+v", av)
		}
	}
	slices.Sort(listed)
	if !slices.Equal(listed, []string{"claude", "cline", "kiro-cli"}) {
		t.Errorf("agents listed: %v", listed)
	}

	// a body that starts with --- isn't read as a frontmatter
	r.Body = "--- a line ---\nmore"
	ok(t)(SaveRule("style", r))
	if got := read(t, files["claude"]); got != "---\n---\n\n--- a line ---\nmore\n" {
		t.Errorf("claude's: %q", got)
	}

	// unticked: its file goes; renamed: the old name's go
	ok(t)(RuleAgentsSet("style", []string{"claude", "kiro-cli"}))
	if _, err := os.Stat(files["cline"]); !os.IsNotExist(err) {
		t.Errorf("cline's file after untick: %v", err)
	}
	r.Name, r.Agents = "tabs", []string{"claude", "kiro-cli"}
	ok(t)(SaveRule("style", r))
	if read(t, files["claude"]) != "" || read(t, files["kiro-cli"]) != "" {
		t.Error("the old name's files stayed")
	}
	tabs := filepath.Join(h, ".claude/rules/tabs.md")
	if read(t, tabs) == "" {
		t.Error("no tabs.md")
	}
	// a file the user deleted comes back on the next sync
	os.Remove(tabs)
	ok(t)(Sync())
	if read(t, tabs) == "" {
		t.Error("the deleted file wasn't written again")
	}
	ok(t)(RemoveRules([]string{"tabs"}))
	for _, f := range []string{tabs, filepath.Join(h, ".kiro/steering/tabs.md")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s after remove: %v", f, err)
		}
	}
	// Kiro's instructions file is no rule's
	if _, err := os.Stat(filepath.Join(h, ".kiro/steering")); err != nil {
		t.Errorf("kiro's steering folder: %v", err)
	}
}

// A rule kept to some files: Claude Code reads paths, Kiro CLI a
// fileMatch inclusion; Cline, which reads every rule always, gets no file
// and the page says why.
func TestRulesKeptToTheirPaths(t *testing.T) {
	h := sandbox(t)
	files := ruleAgents(t, h, "api")
	r := Rule{Name: "api", Paths: []string{" src/api/**/*.ts \n\n*.md", "src/api/**/*.ts"}, Body: "Every endpoint validates its input.", Agents: []string{"claude", "cline", "kiro-cli"}}
	res, err := SaveRule("", r)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, files["claude"]); got != "---\npaths: [\"src/api/**/*.ts\", \"*.md\"]\n---\n\nEvery endpoint validates its input.\n" {
		t.Errorf("claude's: %q", got)
	}
	if got := read(t, files["kiro-cli"]); got != "---\ninclusion: fileMatch\nfileMatchPattern: [\"src/api/**/*.ts\", \"*.md\"]\n---\n\nEvery endpoint validates its input.\n" {
		t.Errorf("kiro's: %q", got)
	}
	if _, err := os.Stat(files["cline"]); !os.IsNotExist(err) {
		t.Errorf("cline was given a rule it would read always: %v", err)
	}
	if p := ruleProblems(res); len(p) != 1 || !strings.Contains(p["cline"], "every conversation") {
		t.Errorf("problems: %+v", p)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(v.Rules[0].Paths, []string{"src/api/**/*.ts", "*.md"}) || !strings.Contains(v.Rules[0].Problems["cline"], "every conversation") {
		t.Errorf("view: %+v", v.Rules[0])
	}
	// no paths any more: Cline gets it, and the problem is gone
	r.Paths = nil
	res, err = SaveRule("api", r)
	if err != nil {
		t.Fatal(err)
	}
	if p := ruleProblems(res); len(p) != 0 {
		t.Errorf("problems: %+v", p)
	}
	if got := read(t, files["cline"]); got != "Every endpoint validates its input.\n" {
		t.Errorf("cline's: %q", got)
	}
	// paths again: magpie's Cline file goes, for it would say the wrong thing
	r.Paths = []string{"*.go"}
	if _, err := SaveRule("api", r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files["cline"]); !os.IsNotExist(err) {
		t.Errorf("cline's stale file stayed: %v", err)
	}
}

// A rule file of the user's own by the same name is never written over
// nor taken away; one magpie wrote that the user edited is theirs from
// then on. Kiro CLI's AGENTS.md, magpie's instructions there, is no
// rule's to take.
func TestRulesLeaveTheUsersFiles(t *testing.T) {
	h := sandbox(t)
	files := ruleAgents(t, h, "style")
	mine := "My own style.\n"
	write(t, files["claude"], mine)
	r := Rule{Name: "style", Body: "Tabs.", Agents: []string{"claude", "cline"}}
	res, err := SaveRule("", r)
	if err != nil {
		t.Fatal(err)
	}
	if p := ruleProblems(res); len(p) != 1 || !strings.Contains(p["claude"], "has a rule of its own at "+files["claude"]) {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if read(t, files["claude"]) != mine {
		t.Fatal("the user's own file was written over")
	}
	edited := read(t, files["cline"]) + "And no trailing spaces.\n"
	write(t, files["cline"], edited)
	r.Body = "Tabs, always."
	res, err = SaveRule("style", r)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, files["cline"]) != edited {
		t.Error("the user's edit was written over")
	}
	if !strings.Contains(ruleProblems(res)["cline"], "of its own") {
		t.Errorf("problems: %+v", res.Problems)
	}
	if _, err := RemoveRules([]string{"style"}); err != nil {
		t.Fatal(err)
	}
	if read(t, files["cline"]) != edited || read(t, files["claude"]) != mine {
		t.Error("removing took the user's files")
	}
	l, _ := load()
	for id, a := range l.Applied {
		if len(a.Rules) != 0 {
			t.Errorf("%s still records %+v", id, a.Rules)
		}
	}

	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"kiro-cli"}}))
	res, err = SaveRule("", Rule{Name: "agents", Body: "Not the instructions.", Agents: []string{"kiro-cli", "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if p := ruleProblems(res); p["kiro-cli"] != kiroAgents {
		t.Errorf("problems: %+v", p)
	}
	if got := read(t, filepath.Join(h, ".kiro/steering/AGENTS.md")); !strings.Contains(got, "Be brief.") || strings.Contains(got, "Not the instructions.") {
		t.Errorf("kiro's instructions: %q", got)
	}
	if read(t, filepath.Join(h, ".claude/rules/agents.md")) != "Not the instructions.\n" {
		t.Error("claude has no agents.md")
	}
}

func TestRuleCheck(t *testing.T) {
	sandbox(t)
	for _, r := range []Rule{
		{Name: "", Body: "x"},
		{Name: "Style", Body: "x"},
		{Name: "a/b", Body: "x"},
		{Name: "style", Body: " \n"},
		{Name: "style", Body: "x", Agents: []string{"cursor"}},
		{Name: "style", Body: "x", Agents: []string{"codex"}},
		{Name: "style", Body: "x", Paths: []string{strings.Repeat("a", maxRulePath+1)}},
		{Name: "style", Body: strings.Repeat("a", maxRuleBody+1)},
	} {
		if _, err := SaveRule("", r); err == nil {
			t.Errorf("%+v was taken", r.Name)
		}
	}
	ok(t)(SaveRule("", Rule{Name: "r", Body: "x"}))
	if _, err := SaveRule("", Rule{Name: "r", Body: "y"}); err == nil {
		t.Error("a second rule called r")
	}
}

// Carried to another computer, the rules come along; a bundle from a
// magpie before them keeps this computer's.
func TestRulesCarried(t *testing.T) {
	h := sandbox(t)
	ruleAgents(t, h, "style")
	ok(t)(SaveRule("", Rule{Name: "style", Paths: []string{"*.go"}, Body: "gofmt.", Agents: []string{"claude"}}))
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if b.Rules == nil || len(*b.Rules) != 1 || (*b.Rules)[0].Name != "style" {
		t.Fatalf("collected: %+v", b.Rules)
	}
	old := *b
	old.Rules = nil
	ok(t)(RemoveRules([]string{"style"}))
	ok(t)(SaveRule("", Rule{Name: "here", Body: "x", Agents: []string{"claude"}}))
	if _, err := Put(&old); err != nil {
		t.Fatal(err)
	}
	l, _ := load()
	if len(l.Rules) != 1 || l.Rules[0].Name != "here" {
		t.Errorf("an older bundle took this computer's rules: %+v", l.Rules)
	}
	if _, err := Put(b); err != nil {
		t.Fatal(err)
	}
	l, _ = load()
	if len(l.Rules) != 1 || l.Rules[0].Name != "style" {
		t.Errorf("after put: %+v", l.Rules)
	}
	if got := read(t, filepath.Join(h, ".claude/rules/style.md")); got != "---\npaths: [\"*.go\"]\n---\n\ngofmt.\n" {
		t.Errorf("claude's file after put: %q", got)
	}
	if _, err := os.Stat(filepath.Join(h, ".claude/rules/here.md")); !os.IsNotExist(err) {
		t.Errorf("here.md stayed: %v", err)
	}
}
