package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// subagentAgents puts every agent that takes subagents in the sandbox.
func subagentAgents(t *testing.T, h string) map[string]string {
	t.Helper()
	write(t, filepath.Join(h, ".qwen/settings.json"), "{}\n")
	write(t, filepath.Join(h, ".factory/settings.json"), "")
	write(t, filepath.Join(h, ".grok/config.toml"), "")
	return map[string]string{
		"claude":   filepath.Join(h, ".claude/agents/reviewer.md"),
		"codex":    filepath.Join(h, ".codex/agents/reviewer.toml"),
		"opencode": filepath.Join(h, ".config/opencode/agents/reviewer.md"),
		"gemini":   filepath.Join(h, ".gemini/agents/reviewer.md"),
		"qwen":     filepath.Join(h, ".qwen/agents/reviewer.md"),
		"droid":    filepath.Join(h, ".factory/droids/reviewer.md"),
		"grok":     filepath.Join(h, ".grok/agents/reviewer.md"),
		"cursor":   filepath.Join(h, ".cursor/agents/reviewer.md"),
		"copilot":  filepath.Join(h, ".copilot/agents/reviewer.agent.md"),
	}
}

var reviewer = Subagent{
	Name:        "reviewer",
	Description: "Reviews a diff: \"bugs\" only.\n  Use after edits.",
	Prompt:      "You review code.\r\n\r\nReport bugs only.\n\n",
	MCP:         []string{"github", "gone"},
	Skills:      []string{"tdd", "gone"},
}

// Each agent gets the subagent as a file of its own, in its own folder
// and shape: the skills and servers only where the agent's file can name
// them, and of them only those the library has.
func TestSubagentsEachAgentsFile(t *testing.T) {
	h := sandbox(t)
	files := subagentAgents(t, h)
	ok(t)(SaveServer("", Server{Name: "github", Transport: "stdio", Command: "npx", Agents: []string{"claude", "droid"}}))
	ok(t)(change(func(l *Library) error {
		l.Skills = append(l.Skills, &Skill{Name: "tdd", Agents: []string{}})
		return nil
	}))
	s := reviewer
	for id := range files {
		s.Agents = append(s.Agents, id)
	}
	ok(t)(SaveSubagent("", s))

	desc := `"Reviews a diff: \"bugs\" only. Use after edits."`
	body := "---\n\nYou review code.\n\nReport bugs only.\n"
	want := map[string]string{
		"claude":   "---\nname: \"reviewer\"\ndescription: " + desc + "\nskills: [\"tdd\"]\nmcpServers: [\"github\"]\n" + body,
		"opencode": "---\ndescription: " + desc + "\nmode: subagent\n" + body,
		"droid":    "---\nname: \"reviewer\"\ndescription: " + desc + "\nmcpServers: [\"github\"]\n" + body,
	}
	for _, id := range []string{"gemini", "qwen", "grok", "cursor", "copilot"} {
		want[id] = "---\nname: \"reviewer\"\ndescription: " + desc + "\n" + body
	}
	for id, f := range files {
		got := read(t, f)
		if id == "codex" {
			var role map[string]any
			if err := toml.Unmarshal([]byte(got), &role); err != nil {
				t.Fatalf("codex role: %v\n%s", err, got)
			}
			if role["name"] != "reviewer" || role["description"] != `Reviews a diff: "bugs" only. Use after edits.` ||
				role["developer_instructions"] != "You review code.\n\nReport bugs only.\n" || len(role) != 3 {
				t.Errorf("codex role: %#v", role)
			}
			continue
		}
		if got != want[id] {
			t.Errorf("%s's %s:\n%s\nwant:\n%s", id, f, got, want[id])
		}
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Subagents) != 1 || len(v.Subagents[0].Files) != len(files) || len(v.Subagents[0].Problems) != 0 {
		t.Errorf("view: %+v", v.Subagents)
	}
	for _, av := range v.SubagentAgents {
		if (av.Skills != (av.ID == "claude")) || (av.MCP != (av.ID == "claude" || av.ID == "droid")) || av.No != "" {
			t.Errorf("agent view %+v", av)
		}
	}

	// taken from one agent: its file goes, the others stay
	ok(t)(SubagentAgentsSet("reviewer", []string{"claude", "codex"}))
	for id, f := range files {
		_, err := os.Stat(f)
		if gone := os.IsNotExist(err); gone != (id != "claude" && id != "codex") {
			t.Errorf("%s's file after untick: %v", id, err)
		}
	}
	// renamed: the old name's files go, the new one's come
	s.Name, s.Agents = "critic", []string{"claude", "codex"}
	ok(t)(SaveSubagent("reviewer", s))
	if read(t, files["claude"]) != "" || read(t, files["codex"]) != "" {
		t.Error("the old name's files stayed")
	}
	if !strings.Contains(read(t, filepath.Join(h, ".claude/agents/critic.md")), "name: \"critic\"") {
		t.Error("no critic.md")
	}
	// a file the user deleted comes back on the next sync
	os.Remove(filepath.Join(h, ".claude/agents/critic.md"))
	ok(t)(Sync())
	if read(t, filepath.Join(h, ".claude/agents/critic.md")) == "" {
		t.Error("the deleted file wasn't written again")
	}
	ok(t)(RemoveSubagents([]string{"critic"}))
	for _, f := range []string{".claude/agents/critic.md", ".codex/agents/critic.toml"} {
		if _, err := os.Stat(filepath.Join(h, f)); !os.IsNotExist(err) {
			t.Errorf("%s after remove: %v", f, err)
		}
	}
}

// A subagent file of the user's own by the same name is never written
// over, nor taken away; one magpie wrote that the user edited is theirs
// from then on: a change in the library leaves it as the user has it.
func TestSubagentsLeaveTheUsersFiles(t *testing.T) {
	h := sandbox(t)
	own := filepath.Join(h, ".claude/agents/reviewer.md")
	write(t, own, "---\nname: reviewer\ndescription: mine\n---\nMy own reviewer.\n")
	s := reviewer
	s.Agents = []string{"claude", "codex", "opencode"}
	res, err := SaveSubagent("", s)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || res.Problems[0].Agent != "claude" || res.Problems[0].What != "subagent:reviewer" {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if read(t, own) != "---\nname: reviewer\ndescription: mine\n---\nMy own reviewer.\n" {
		t.Fatalf("the user's own file was written over:\n%s", read(t, own))
	}

	// the user edits magpie's Codex file, then the library's subagent
	// changes: the edit stays, and the page is told why
	codex := filepath.Join(h, ".codex/agents/reviewer.toml")
	oc := filepath.Join(h, ".config/opencode/agents/reviewer.md")
	edited := read(t, codex) + "model = \"gpt-5.5\"\n"
	write(t, codex, edited)
	s.Prompt = "You review code, twice."
	res, err = SaveSubagent("reviewer", s)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, codex) != edited {
		t.Errorf("the user's edit was written over:\n%s", read(t, codex))
	}
	if !strings.Contains(read(t, oc), "You review code, twice.") {
		t.Errorf("magpie's untouched OpenCode file wasn't updated:\n%s", read(t, oc))
	}
	var codexSaid bool
	for _, p := range res.Problems {
		codexSaid = codexSaid || p.Agent == "codex" && strings.Contains(p.Error, "of its own")
	}
	if !codexSaid {
		t.Errorf("problems: %+v", res.Problems)
	}

	// removed from the library: magpie's own file goes, the user's stay
	if _, err := RemoveSubagents([]string{"reviewer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oc); !os.IsNotExist(err) {
		t.Errorf("magpie's OpenCode file stayed: %v", err)
	}
	if read(t, codex) != edited || !strings.Contains(read(t, own), "My own reviewer.") {
		t.Error("removing took the user's files")
	}
	l, _ := load()
	for id, a := range l.Applied {
		if len(a.Subagents) != 0 {
			t.Errorf("%s still records %+v", id, a.Subagents)
		}
	}
}

func TestSubagentCheck(t *testing.T) {
	sandbox(t)
	for _, s := range []Subagent{
		{Name: "Reviewer", Description: "d", Prompt: "p"},
		{Name: "r", Description: "d", Prompt: "p"},
		{Name: "-r-", Description: "d", Prompt: "p"},
		{Name: "default", Description: "d", Prompt: "p"},
		{Name: "rev", Description: " ", Prompt: "p"},
		{Name: "rev", Description: "d", Prompt: " \n"},
		{Name: "rev", Description: "d", Prompt: "p", Agents: []string{"crush"}},
	} {
		if _, err := SaveSubagent("", s); err == nil {
			t.Errorf("%+v saved", s)
		}
	}
}

// A subagent's model (luckymanjc on Discord: a routing group set for a
// subagent, which Codex ran on its lead's model) goes into Codex's role
// file, whose model Codex lays over the lead's pick; no other agent's file
// is written one, and the page is told only Codex takes it. Changed in the
// library, the role file magpie wrote changes with it; taken away, the
// role is as one written without a model.
func TestSubagentModelGoesToCodex(t *testing.T) {
	h := sandbox(t)
	files := subagentAgents(t, h)
	s := reviewer
	for id := range files {
		s.Agents = append(s.Agents, id)
	}
	ok(t)(SaveSubagent("", s))
	plain := map[string]string{}
	for id, f := range files {
		plain[id] = read(t, f)
	}
	role := func() map[string]any {
		t.Helper()
		var r map[string]any
		if err := toml.Unmarshal([]byte(read(t, files["codex"])), &r); err != nil {
			t.Fatalf("codex role: %v\n%s", err, read(t, files["codex"]))
		}
		return r
	}

	s.Model = " group/review "
	ok(t)(SaveSubagent("reviewer", s))
	if r := role(); r["model"] != "group/review" || r["developer_instructions"] != "You review code.\n\nReport bugs only.\n" || len(r) != 4 {
		t.Errorf("codex role with a model: %#v", r)
	}
	for id, f := range files {
		if id != "codex" && read(t, f) != plain[id] {
			t.Errorf("%s's file changed with the model:\n%s", id, read(t, f))
		}
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Subagents[0].Model != "group/review" {
		t.Errorf("view's model: %q", v.Subagents[0].Model)
	}
	for _, av := range v.SubagentAgents {
		if av.Model != (av.ID == "codex") {
			t.Errorf("agent view %+v", av)
		}
	}

	s.Model = "gpt-6-sol"
	ok(t)(SaveSubagent("reviewer", s))
	if r := role(); r["model"] != "gpt-6-sol" {
		t.Errorf("codex role after the model changed: %#v", r)
	}
	s.Model = ""
	ok(t)(SaveSubagent("reviewer", s))
	if got := read(t, files["codex"]); got != plain["codex"] {
		t.Errorf("codex role with the model taken away:\n%s\nwant:\n%s", got, plain["codex"])
	}

	for _, m := range []string{"group/a b", "x\ny", strings.Repeat("m", 201)} {
		s.Model = m
		if _, err := SaveSubagent("reviewer", s); err == nil {
			t.Errorf("model %q saved", m)
		}
	}
}
