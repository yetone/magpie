package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// piPrompts puts Pi's own prompt files (testdata/pi-prompts, from Pi's
// repository .pi/prompts) in the sandbox's ~/.pi/agent/prompts, and says
// each one's bytes by path.
func piPrompts(t *testing.T, h string) map[string]string {
	t.Helper()
	ents, err := os.ReadDir("testdata/pi-prompts")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("testdata/pi-prompts", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(h, ".pi/agent/prompts", e.Name())
		write(t, f, string(b))
		m[f] = string(b)
	}
	return m
}

func probeCommands(t *testing.T) *CommandProbe {
	t.Helper()
	p, err := ProbeCommands()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func candidate(p *CommandProbe, name string) []CommandCandidate {
	var r []CommandCandidate
	for _, c := range p.Candidates {
		if c.Name == name {
			r = append(r, c)
		}
	}
	return r
}

func candidatePaths(p *CommandProbe) []string {
	var r []string
	for _, c := range p.Candidates {
		r = append(r, c.Path)
	}
	return r
}

// The prompts the user made in Pi are taken into the library as they
// are: Pi keeps its files byte for byte, magpie takes them as its own,
// nothing is doubled, and Pi's $@ reads as $ARGUMENTS.
func TestCommandImportPiPrompts(t *testing.T) {
	h := sandbox(t)
	files := commandAgents(t, h, "pr")
	pi := piPrompts(t, h)
	p := probeCommands(t)
	if len(p.Candidates) != len(pi) || len(p.Left) != 0 {
		t.Fatalf("found: %+v", p)
	}
	pr := candidate(p, "pr")
	if len(pr) != 1 {
		t.Fatalf("pr: %+v", pr)
	}
	if pr[0].Description != "Review PRs from URLs with structured issue and code analysis" ||
		!strings.HasPrefix(pr[0].Prompt, "You are given one or more GitHub PR URLs: $ARGUMENTS\n") ||
		strings.Contains(pr[0].Prompt, "$@") || pr[0].State != "" || pr[0].Clash ||
		!slices.Equal(pr[0].Agents, []string{"pi"}) || pr[0].Files["pi"] != files["pi"] {
		t.Fatalf("pr: %+v", pr[0])
	}
	res := ok(t)(ImportCommands(candidatePaths(p)))
	if res.Commands == nil || len(res.Commands.Added) != len(pi) || len(res.Commands.Joined) != 0 {
		t.Fatalf("imported: %+v", res.Commands)
	}
	for f, b := range pi {
		if got := read(t, f); got != b {
			t.Errorf("%s was rewritten:\n%s", f, got)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(h, ".pi/agent/prompts"))
	if len(ents) != len(pi) {
		t.Errorf("Pi's prompts after the import: %d files, want %d", len(ents), len(pi))
	}
	for id, f := range files {
		if id != "pi" {
			if _, err := os.Stat(f); !os.IsNotExist(err) {
				t.Errorf("%s was given pr: %v", id, err)
			}
		}
	}
	l, _ := load()
	c := l.command("pr")
	if c == nil || !slices.Equal(c.Agents, []string{"pi"}) || !strings.Contains(c.Prompt, "$ARGUMENTS") {
		t.Fatalf("library's pr: %+v", c)
	}
	if !slices.ContainsFunc(l.Applied["pi"].Commands, func(f SubagentFile) bool { return f.Name == "pr" && f.Path == files["pi"] }) {
		t.Errorf("Pi's pr isn't magpie's: %+v", l.Applied["pi"].Commands)
	}
	// found again: they are magpie's now
	if p := probeCommands(t); len(p.Candidates) != 0 {
		t.Errorf("found again: %+v", p.Candidates)
	}

	// ticked for Claude Code too: Claude's file says it as Claude does,
	// Pi's stays the user's bytes
	ok(t)(CommandAgentsSet("pr", []string{"claude", "pi"}))
	if got := read(t, files["claude"]); !strings.Contains(got, "PR URLs: $ARGUMENTS\n") {
		t.Errorf("claude's pr:\n%s", got)
	}
	if read(t, files["pi"]) != pi[files["pi"]] {
		t.Errorf("Pi's pr was rewritten:\n%s", read(t, files["pi"]))
	}

	// edited here: Pi's file says the new prompt, in Pi's place
	c.Prompt = "Review $ARGUMENTS."
	c.Agents = []string{"claude", "pi"}
	ok(t)(SaveCommand("pr", *c))
	if got := read(t, files["pi"]); got != "---\ndescription: \"Review PRs from URLs with structured issue and code analysis\"\n---\n\nReview $ARGUMENTS.\n" {
		t.Errorf("Pi's pr after an edit:\n%s", got)
	}
	if v, _ := load(); v.command("pr").Taken["pi"] == nil {
		t.Error("an edit lost where pr came from")
	}

	// renamed: the new name's file is written in Pi's folder, the old goes
	c.Name = "review"
	ok(t)(SaveCommand("pr", *c))
	if v, _ := load(); v.command("review").Taken != nil {
		t.Errorf("a rename kept the old file: %+v", v.command("review").Taken)
	}
	if _, err := os.Stat(files["pi"]); !os.IsNotExist(err) {
		t.Errorf("pr.md stayed after the rename: %v", err)
	}
	if got := read(t, filepath.Join(h, ".pi/agent/prompts/review.md")); !strings.Contains(got, "Review $ARGUMENTS.") {
		t.Errorf("review.md: %q", got)
	}
}

// Each agent's way of saying the words typed in reads back as magpie's.
func TestCommandImportArgs(t *testing.T) {
	h := sandbox(t)
	commandAgents(t, h, "x")
	write(t, filepath.Join(h, ".claude/commands/fix.md"), "---\ndescription: Fix it\nallowed-tools: Bash\n---\nFix issue $ARGUMENTS[0] in $1, not \\$0 or \\$2.\n")
	write(t, filepath.Join(h, ".claude/commands/all.md"), "Do $ARGUMENTS and $0.\r\n")
	write(t, filepath.Join(h, ".gemini/commands/gem.toml"), "description = \"Gem\"\nprompt = \"\"\"\nRun {{args}} now.\n\"\"\"\n")
	write(t, filepath.Join(h, ".qwen/commands/qw.toml"), "prompt = \"Q {{args}}\"\n")
	write(t, filepath.Join(h, ".qwen/commands/qm.md"), "---\ndescription: QM\n---\nQ {{args}} md\n")
	write(t, filepath.Join(h, ".config/opencode/command/oc.md"), "---\ndescription: OC\nagent: build\n---\nOC $ARGUMENTS\n")
	write(t, filepath.Join(h, ".factory/commands/dr.md"), "Droid $ARGUMENTS\n")
	write(t, filepath.Join(h, ".pi/agent/prompts/sub.md"), "Pi ${@:2} and $1\n")
	want := map[string]string{
		"fix": "Fix issue $1 in $2, not $0 or \\$2.",
		"all": "Do $ARGUMENTS and $1.",
		"gem": "Run $ARGUMENTS now.",
		"qw":  "Q $ARGUMENTS",
		"qm":  "Q $ARGUMENTS md",
		"oc":  "OC $ARGUMENTS",
		"dr":  "Droid $ARGUMENTS",
		"sub": "Pi ${@:2} and $1",
	}
	p := probeCommands(t)
	if len(p.Candidates) != len(want) {
		t.Fatalf("found: %+v", p.Candidates)
	}
	for _, c := range p.Candidates {
		if c.Prompt != want[c.Name] {
			t.Errorf("%s: %q, want %q", c.Name, c.Prompt, want[c.Name])
		}
	}
	if c := candidate(p, "gem"); len(c) != 1 || c[0].Description != "Gem" {
		t.Errorf("gem: %+v", c)
	}
	// every one taken, every file as it was
	before := map[string]string{}
	for _, c := range p.Candidates {
		for _, f := range c.Files {
			before[f] = read(t, f)
		}
	}
	ok(t)(ImportCommands(candidatePaths(p)))
	for f, b := range before {
		if got := read(t, f); got != b {
			t.Errorf("%s was rewritten:\n%q\nwas\n%q", f, got, b)
		}
	}
	// OpenCode's command/ file stays where it is: no second one in commands/
	if _, err := os.Stat(filepath.Join(h, ".config/opencode/commands/oc.md")); !os.IsNotExist(err) {
		t.Errorf("oc doubled in commands/: %v", err)
	}
	if p := probeCommands(t); len(p.Candidates) != 0 {
		t.Errorf("found again: %+v", p.Candidates)
	}
}

// One name in several agents: saying the same, one command for them all;
// saying different things, one of them to pick. Against the library: the
// same command joins, another one is left alone.
func TestCommandImportByName(t *testing.T) {
	h := sandbox(t)
	files := commandAgents(t, h, "rv")
	write(t, files["claude"], "---\ndescription: Review\n---\nReview $ARGUMENTS.\n")
	write(t, files["pi"], "---\ndescription: Review\n---\n\nReview $@.\n")
	write(t, files["droid"], "Something else.\n")
	write(t, filepath.Join(h, ".claude/commands/lib.md"), "Library's\n")
	write(t, filepath.Join(h, ".claude/commands/own.md"), "Not the library's\n")
	write(t, filepath.Join(h, ".claude/commands/Bad.md"), "x\n")
	write(t, filepath.Join(h, ".claude/commands/empty.md"), "---\ndescription: d\n---\n")
	ok(t)(SaveCommand("", Command{Name: "lib", Prompt: "Library's", Agents: []string{"opencode"}}))
	ok(t)(SaveCommand("", Command{Name: "own", Prompt: "The library's", Agents: []string{"opencode"}}))

	p := probeCommands(t)
	rv := candidate(p, "rv")
	if len(rv) != 2 || !slices.Equal(rv[0].Agents, []string{"claude", "pi"}) || rv[0].Files["pi"] != files["pi"] ||
		!slices.Equal(rv[1].Agents, []string{"droid"}) || !rv[0].Clash || !rv[1].Clash {
		t.Fatalf("rv: %+v", rv)
	}
	if c := candidate(p, "lib"); len(c) != 1 || c[0].State != "same" {
		t.Errorf("lib: %+v", c)
	}
	if c := candidate(p, "own"); len(c) != 1 || c[0].State != "taken" {
		t.Errorf("own: %+v", c)
	}
	if len(p.Left) != 2 || p.Left[0].Path != filepath.Join(h, ".claude/commands/Bad.md") || p.Left[1].Path != filepath.Join(h, ".claude/commands/empty.md") {
		t.Errorf("left: %+v", p.Left)
	}

	if _, err := ImportCommands([]string{rv[0].Path, rv[1].Path}); err == nil {
		t.Error("two rv taken")
	}
	if _, err := ImportCommands([]string{candidate(p, "own")[0].Path}); err == nil {
		t.Error("own taken over the library's")
	}
	if _, err := ImportCommands([]string{filepath.Join(h, "nothing.md")}); err == nil {
		t.Error("a path with no command taken")
	}
	res := ok(t)(ImportCommands([]string{rv[0].Path, candidate(p, "lib")[0].Path}))
	if !slices.Equal(res.Commands.Added, []string{"rv"}) || !slices.Equal(res.Commands.Joined, []string{"lib"}) {
		t.Errorf("imported: %+v", res.Commands)
	}
	l, _ := load()
	if c := l.command("rv"); c == nil || !slices.Equal(c.Agents, []string{"claude", "pi"}) || c.Prompt != "Review $ARGUMENTS." {
		t.Errorf("rv: %+v", c)
	}
	if c := l.command("lib"); c == nil || !slices.Equal(c.Agents, []string{"claude", "opencode"}) {
		t.Errorf("lib: %+v", c)
	}
	if read(t, files["droid"]) != "Something else.\n" || read(t, filepath.Join(h, ".claude/commands/own.md")) != "Not the library's\n" {
		t.Error("a file not imported was written over")
	}
	if read(t, files["pi"]) != "---\ndescription: Review\n---\n\nReview $@.\n" || read(t, filepath.Join(h, ".claude/commands/lib.md")) != "Library's\n" {
		t.Error("an imported file was rewritten")
	}
	// the library's view never shows where they came from
	for _, c := range View0(t).Commands {
		if c.Taken != nil {
			t.Errorf("%s shows %+v", c.Name, c.Taken)
		}
	}
}

// Where a command came from is this computer's: never carried, and a
// bundle put here keeps it.
func TestCommandImportNotCarried(t *testing.T) {
	h := sandbox(t)
	files := commandAgents(t, h, "pr")
	pi := piPrompts(t, h)
	ok(t)(ImportCommands([]string{files["pi"]}))
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range *b.Commands {
		if c.Taken != nil {
			t.Fatalf("carried: %+v", c.Taken)
		}
	}
	ok(t)(Put(b))
	l, _ := load()
	if c := l.command("pr"); c == nil || c.Taken["pi"] == nil {
		t.Fatalf("put lost where pr came from: %+v", c)
	}
	if read(t, files["pi"]) != pi[files["pi"]] {
		t.Errorf("Pi's pr after put:\n%s", read(t, files["pi"]))
	}
}
