package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// commandAgents puts every agent that takes slash commands in the
// sandbox, and says where each keeps one called name.
func commandAgents(t *testing.T, h, name string) map[string]string {
	t.Helper()
	write(t, filepath.Join(h, ".qwen/settings.json"), "{}\n")
	write(t, filepath.Join(h, ".factory/settings.json"), "")
	return map[string]string{
		"claude":   filepath.Join(h, ".claude/commands", name+".md"),
		"opencode": filepath.Join(h, ".config/opencode/commands", name+".md"),
		"pi":       filepath.Join(h, ".pi/agent/prompts", name+".md"),
		"gemini":   filepath.Join(h, ".gemini/commands", name+".toml"),
		"qwen":     filepath.Join(h, ".qwen/commands", name+".md"),
		"droid":    filepath.Join(h, ".factory/commands", name+".md"),
	}
}

func commandProblems(res *Result) map[string]string {
	m := map[string]string{}
	for _, p := range res.Problems {
		if strings.HasPrefix(p.What, "command:") {
			m[p.Agent] = p.Error
		}
	}
	return m
}

// Each agent gets the command as a file of its own, in its own folder
// and shape, $ARGUMENTS as it says it.
func TestCommandsEachAgentsFile(t *testing.T) {
	h := sandbox(t)
	files := commandAgents(t, h, "review")
	c := Command{Name: "review", Description: "Review \"the\" diff.\n  Bugs only.", Prompt: "Review $ARGUMENTS.\r\n\r\nBugs only.\n\n"}
	for id := range files {
		c.Agents = append(c.Agents, id)
	}
	c.Agents = append(c.Agents, "codex")
	res, err := SaveCommand("", c)
	if err != nil {
		t.Fatal(err)
	}
	desc := `"Review \"the\" diff. Bugs only."`
	md := func(body string) string { return "---\ndescription: " + desc + "\n---\n\n" + body }
	want := map[string]string{
		"claude":   md("Review $ARGUMENTS.\n\nBugs only.\n"),
		"opencode": md("Review $ARGUMENTS.\n\nBugs only.\n"),
		"pi":       md("Review $ARGUMENTS.\n\nBugs only.\n"),
		"qwen":     md("Review {{args}}.\n\nBugs only.\n"),
		"droid":    md("Review $ARGUMENTS.\n\nBugs only.\n"),
	}
	for id, f := range files {
		got := read(t, f)
		if id == "gemini" {
			var cmd map[string]any
			if err := toml.Unmarshal([]byte(got), &cmd); err != nil {
				t.Fatalf("gemini command: %v\n%s", err, got)
			}
			if cmd["description"] != `Review "the" diff. Bugs only.` || cmd["prompt"] != "Review {{args}}.\n\nBugs only.\n" || len(cmd) != 2 {
				t.Errorf("gemini command: %#v", cmd)
			}
			continue
		}
		if got != want[id] {
			t.Errorf("%s's %s:\n%s\nwant:\n%s", id, f, got, want[id])
		}
	}
	// Codex reads no custom prompts: nothing written, and why
	if p := commandProblems(res); len(p) != 1 || p["codex"] != codexNoCommands {
		t.Errorf("problems: %+v", res.Problems)
	}
	if _, err := os.Stat(filepath.Join(h, ".codex/prompts")); !os.IsNotExist(err) {
		t.Errorf("a Codex prompts folder: %v", err)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Commands) != 1 || len(v.Commands[0].Files) != len(files) || v.Commands[0].Problems["codex"] != codexNoCommands {
		t.Errorf("view: %+v", v.Commands)
	}
	var listed []string
	for _, av := range v.CommandAgents {
		listed = append(listed, av.ID)
		if av.Args != slices.Contains([]string{"claude", "opencode", "pi"}, av.ID) || (av.No != "") != (av.ID == "codex") {
			t.Errorf("agent view %+v", av)
		}
	}
	slices.Sort(listed)
	if !slices.Equal(listed, []string{"claude", "codex", "droid", "gemini", "opencode", "pi", "qwen"}) {
		t.Errorf("agents listed: %v", listed)
	}

	// taken from one agent: its file goes, the others stay
	ok(t)(CommandAgentsSet("review", []string{"claude", "opencode"}))
	for id, f := range files {
		_, err := os.Stat(f)
		if gone := os.IsNotExist(err); gone != (id != "claude" && id != "opencode") {
			t.Errorf("%s's file after untick: %v", id, err)
		}
	}
	// renamed: the old name's files go, the new one's come
	c.Name, c.Agents = "check", []string{"claude", "opencode"}
	ok(t)(SaveCommand("review", c))
	if read(t, files["claude"]) != "" || read(t, files["opencode"]) != "" {
		t.Error("the old name's files stayed")
	}
	if !strings.Contains(read(t, filepath.Join(h, ".claude/commands/check.md")), "Review $ARGUMENTS.") {
		t.Error("no check.md")
	}
	// a file the user deleted comes back on the next sync
	os.Remove(filepath.Join(h, ".claude/commands/check.md"))
	ok(t)(Sync())
	if read(t, filepath.Join(h, ".claude/commands/check.md")) == "" {
		t.Error("the deleted file wasn't written again")
	}
	ok(t)(RemoveCommands([]string{"check"}))
	for _, f := range []string{".claude/commands/check.md", ".config/opencode/commands/check.md"} {
		if _, err := os.Stat(filepath.Join(h, f)); !os.IsNotExist(err) {
			t.Errorf("%s after remove: %v", f, err)
		}
	}
}

// Words by their place: $1 is the first. Claude Code counts from 0, so
// it is $ARGUMENTS[0] there; OpenCode and Pi count from 1 as magpie does.
// An agent with no such thing gets no file, and says why; one with no
// description gets none in its file.
func TestCommandsWordsByPlace(t *testing.T) {
	h := sandbox(t)
	files := commandAgents(t, h, "fix")
	c := Command{Name: "fix", Prompt: "Fix issue #$1 with $2, costs $0 ($ARGUMENTS).", Agents: []string{"claude", "opencode", "pi", "gemini", "qwen", "droid"}}
	res, err := SaveCommand("", c)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, files["claude"]); got != "Fix issue #$ARGUMENTS[0] with $ARGUMENTS[1], costs \\$0 ($ARGUMENTS).\n" {
		t.Errorf("claude: %q", got)
	}
	for _, id := range []string{"opencode", "pi"} {
		if got := read(t, files[id]); got != "Fix issue #$1 with $2, costs $0 ($ARGUMENTS).\n" {
			t.Errorf("%s: %q", id, got)
		}
	}
	p := commandProblems(res)
	for _, id := range []string{"gemini", "qwen", "droid"} {
		if _, err := os.Stat(files[id]); !os.IsNotExist(err) {
			t.Errorf("%s was given a command it can't read: %v", id, err)
		}
		if !strings.Contains(p[id], "$1") {
			t.Errorf("%s's problem: %q", id, p[id])
		}
	}
	if len(p) != 3 {
		t.Errorf("problems: %+v", p)
	}
	// the page says so before a sync, too
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gemini", "qwen", "droid"} {
		if !strings.Contains(v.Commands[0].Problems[id], "$1") {
			t.Errorf("view's %s problem: %+v", id, v.Commands[0].Problems)
		}
	}

	// no $1 any more: written for them all, and the problems are gone
	c.Prompt = "Fix $ARGUMENTS."
	res, err = SaveCommand("fix", c)
	if err != nil {
		t.Fatal(err)
	}
	if p := commandProblems(res); len(p) != 0 {
		t.Errorf("problems: %+v", p)
	}
	if got := read(t, files["qwen"]); got != "Fix {{args}}.\n" {
		t.Errorf("qwen: %q", got)
	}
	// $1 again: magpie's Gemini file goes, for it would say the wrong thing
	c.Prompt = "Fix $1."
	if _, err := SaveCommand("fix", c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files["gemini"]); !os.IsNotExist(err) {
		t.Errorf("gemini's stale file stayed: %v", err)
	}
}

// A command file of the user's own by the same name is never written
// over, nor taken away; one magpie wrote that the user edited is theirs
// from then on; one that says what magpie would write is taken as
// magpie's.
func TestCommandsLeaveTheUsersFiles(t *testing.T) {
	h := sandbox(t)
	files := commandAgents(t, h, "review")
	mine := "---\ndescription: mine\n---\nMy own review.\n"
	write(t, files["claude"], mine)
	same := "Review $ARGUMENTS.\n"
	write(t, files["pi"], same)
	c := Command{Name: "review", Prompt: "Review $ARGUMENTS.", Agents: []string{"claude", "opencode", "pi", "droid"}}
	res, err := SaveCommand("", c)
	if err != nil {
		t.Fatal(err)
	}
	if p := commandProblems(res); len(p) != 1 || !strings.Contains(p["claude"], "has a slash command of its own at "+files["claude"]) {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if read(t, files["claude"]) != mine {
		t.Fatalf("the user's own file was written over:\n%s", read(t, files["claude"]))
	}

	// the user edits magpie's Droid file, then the command changes: the
	// edit stays, and the page is told why
	edited := read(t, files["droid"]) + "And style.\n"
	write(t, files["droid"], edited)
	c.Prompt = "Review $ARGUMENTS, twice."
	res, err = SaveCommand("review", c)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, files["droid"]) != edited {
		t.Errorf("the user's edit was written over:\n%s", read(t, files["droid"]))
	}
	if !strings.Contains(commandProblems(res)["droid"], "of its own") {
		t.Errorf("problems: %+v", res.Problems)
	}
	if read(t, files["opencode"]) != "Review $ARGUMENTS, twice.\n" || read(t, files["pi"]) != "Review $ARGUMENTS, twice.\n" {
		t.Errorf("magpie's files weren't updated: %q %q", read(t, files["opencode"]), read(t, files["pi"]))
	}

	// removed: magpie's own files go, the user's stay
	ok(t)(RemoveCommands([]string{"review"}))
	for _, id := range []string{"opencode", "pi"} {
		if _, err := os.Stat(files[id]); !os.IsNotExist(err) {
			t.Errorf("magpie's %s file stayed: %v", id, err)
		}
	}
	if read(t, files["droid"]) != edited || read(t, files["claude"]) != mine {
		t.Error("removing took the user's files")
	}
	l, _ := load()
	for id, a := range l.Applied {
		if len(a.Commands) != 0 {
			t.Errorf("%s still records %+v", id, a.Commands)
		}
	}
}

func TestCommandCheck(t *testing.T) {
	sandbox(t)
	for _, c := range []Command{
		{Name: "", Prompt: "x"},
		{Name: "Review", Prompt: "x"},
		{Name: "-review", Prompt: "x"},
		{Name: "re/view", Prompt: "x"},
		{Name: "re:view", Prompt: "x"},
		{Name: strings.Repeat("a", 65), Prompt: "x"},
		{Name: "review", Prompt: "  \n"},
		{Name: "review", Prompt: "x", Agents: []string{"cursor"}},
		{Name: "review", Prompt: "x", Description: strings.Repeat("字", maxCommandDescription+1)},
	} {
		if _, err := SaveCommand("", c); err == nil {
			t.Errorf("%+v was taken", c)
		}
	}
	for _, n := range []string{"r", "pr_review", "fix-2"} {
		if _, err := SaveCommand("", Command{Name: n, Prompt: "x"}); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, err := SaveCommand("", Command{Name: "r", Prompt: "y"}); err == nil {
		t.Error("a second command called r")
	}
}

// Carried to another computer (a backup, WebDAV sync), the commands come
// along; a bundle from a magpie before them keeps this computer's.
func TestCommandsCarried(t *testing.T) {
	h := sandbox(t)
	commandAgents(t, h, "review")
	ok(t)(SaveCommand("", Command{Name: "review", Description: "d", Prompt: "Review $ARGUMENTS.", Agents: []string{"claude"}}))
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if b.Commands == nil || len(*b.Commands) != 1 || (*b.Commands)[0].Name != "review" {
		t.Fatalf("collected: %+v", b.Commands)
	}
	old := *b
	old.Commands = nil
	ok(t)(RemoveCommands([]string{"review"}))
	ok(t)(SaveCommand("", Command{Name: "here", Prompt: "x", Agents: []string{"claude"}}))
	if _, err := Put(&old); err != nil {
		t.Fatal(err)
	}
	l, _ := load()
	if len(l.Commands) != 1 || l.Commands[0].Name != "here" {
		t.Errorf("an older bundle took this computer's commands: %+v", l.Commands)
	}
	if _, err := Put(b); err != nil {
		t.Fatal(err)
	}
	l, _ = load()
	if len(l.Commands) != 1 || l.Commands[0].Name != "review" {
		t.Errorf("after put: %+v", l.Commands)
	}
	if got := read(t, filepath.Join(h, ".claude/commands/review.md")); got != "---\ndescription: \"d\"\n---\n\nReview $ARGUMENTS.\n" {
		t.Errorf("claude's file after put: %q", got)
	}
	if _, err := os.Stat(filepath.Join(h, ".claude/commands/here.md")); !os.IsNotExist(err) {
		t.Errorf("here.md stayed: %v", err)
	}
}
