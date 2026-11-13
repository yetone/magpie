package library

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// agencyRepo serves testdata/agency-agents, a few of
// msitarzewski/agency-agents' own files as they are there, as GitHub's
// tarball of it; edit changes a file's bytes before the next download.
func agencyRepo(t *testing.T) (edit func(path, from, to string)) {
	t.Helper()
	files := map[string]string{}
	root := filepath.Join("testdata", "agency-agents")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		files[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tar/msitarzewski/agency-agents" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Write(tarball(t, files))
	}))
	t.Cleanup(srv.Close)
	old := tarballURL
	tarballURL = func(repo, ref string) string { return srv.URL + "/tar/" + repo }
	t.Cleanup(func() { tarballURL = old })
	return func(path, from, to string) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.Contains(files[path], from) {
			t.Fatalf("%s has no %q", path, from)
		}
		files[path] = strings.Replace(files[path], from, to, 1)
	}
}

const agency = "https://github.com/msitarzewski/agency-agents"

func probeSubagents(t *testing.T, input string) *SubagentProbe {
	t.Helper()
	p, err := ProbeSubagents(input)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func candidateNames(p *SubagentProbe) map[string]SubagentCandidate {
	m := map[string]SubagentCandidate{}
	for _, c := range p.Candidates {
		m[c.Name] = c
	}
	return m
}

// Every agent file of agency-agents is a subagent by the slug of its
// name, its description and its body; the README and the strategy notes,
// no front matter, are none, and the second Backend Architect is left
// out by its name. The keys magpie doesn't model (color, emoji, vibe,
// tools, author) go nowhere.
func TestSubagentsFromAgencyAgents(t *testing.T) {
	h := sandbox(t)
	agencyRepo(t)
	p := probeSubagents(t, agency)
	got := candidateNames(p)
	want := map[string]string{
		"ai-data-remediation-engineer": "engineering/engineering-ai-data-remediation-engineer.md",
		"backend-architect":            "engineering/engineering-backend-architect.md",
		"paid-media-auditor":           "paid-media/paid-media-auditor.md",
		"unity-architect":              "game-development/unity/unity-architect.md",
	}
	if len(got) != len(want) {
		t.Fatalf("candidates %+v", p.Candidates)
	}
	for n, path := range want {
		if c := got[n]; c.Path != path || c.State != "" || c.As != "" || c.Description == "" {
			t.Errorf("%s: %+v", n, c)
		}
	}
	if len(p.Left) != 1 || p.Left[0].Path != "integrations/mcp-memory/backend-architect-with-memory.md" || !strings.Contains(p.Left[0].Error, "engineering/engineering-backend-architect.md") {
		t.Errorf("left %+v", p.Left)
	}

	r := ok(t)(ImportSubagents(agency, []string{want["paid-media-auditor"], want["unity-architect"]}, []string{"claude", "nobody"}))
	if r.Subagents == nil || !slices.Equal(r.Subagents.Added, []string{"paid-media-auditor", "unity-architect"}) {
		t.Fatalf("imported %+v", r.Subagents)
	}
	l := loaded(t)
	s := l.subagent("paid-media-auditor")
	if s == nil || !slices.Equal(s.Agents, []string{"claude"}) || !strings.HasPrefix(s.Prompt, "# Paid Media Auditor Agent") ||
		!strings.HasPrefix(s.Description, "Comprehensive paid media auditor") {
		t.Fatalf("%+v", s)
	}
	if s.From == nil || s.From.Kind != "github" || s.From.Repo != "msitarzewski/agency-agents" || s.From.Path != want["paid-media-auditor"] {
		t.Errorf("from %+v", s.From)
	}
	if l.subagent("backend-architect") != nil {
		t.Error("one not picked was imported")
	}
	b := read(t, filepath.Join(h, ".claude/agents/paid-media-auditor.md"))
	head, _, _ := strings.Cut(strings.TrimPrefix(b, "---\n"), "\n---")
	for _, k := range []string{"color", "tools", "emoji", "vibe", "author"} {
		if strings.Contains(head, k+":") {
			t.Errorf("%s went to Claude Code:\n%s", k, head)
		}
	}
	if !strings.Contains(head, `name: "paid-media-auditor"`) {
		t.Errorf("claude's file:\n%s", head)
	}
}

// A name the library has for a subagent of the user's own is never
// written over: the import takes the next free one.
func TestSubagentImportLeavesTheUsersOwn(t *testing.T) {
	sandbox(t)
	agencyRepo(t)
	mine := Subagent{Name: "backend-architect", Description: "Mine", Prompt: "My own prompt.", Agents: []string{"claude"}}
	ok(t)(SaveSubagent("", mine))
	mine.Name = "backend-architect-2"
	ok(t)(SaveSubagent("", mine))
	c := candidateNames(probeSubagents(t, agency))["backend-architect"]
	if c.State != "" || c.As != "backend-architect-3" {
		t.Fatalf("%+v", c)
	}
	r := ok(t)(ImportSubagents(agency, []string{c.Path}, nil))
	if !slices.Equal(r.Subagents.Added, []string{"backend-architect-3"}) {
		t.Fatalf("%+v", r.Subagents)
	}
	l := loaded(t)
	if s := l.subagent("backend-architect"); s.Prompt != "My own prompt." || s.From != nil {
		t.Errorf("the user's own was changed: %+v", s)
	}
	if s := l.subagent("backend-architect-3"); s == nil || !strings.Contains(s.Prompt, "Backend Architect") {
		t.Errorf("%+v", s)
	}
}

// Importing the same repository again adds none twice: one still as it
// came is updated to what it says now, keeping the agents it was given,
// one the same is had, and one changed here is left as it is, from the
// whole repository's link or its folder's alike.
func TestSubagentImportAgainUpdates(t *testing.T) {
	sandbox(t)
	edit := agencyRepo(t)
	probeSubagents(t, agency)
	ok(t)(ImportSubagents(agency, nil, []string{"claude"}))
	ok(t)(SubagentAgentsSet("unity-architect", []string{"codex"}))
	s := *loaded(t).subagent("paid-media-auditor")
	s.Prompt += "\n\nAlways in euros."
	ok(t)(SaveSubagent("paid-media-auditor", s))

	edit("game-development/unity/unity-architect.md", "# Unity Architect", "# Unity Architect (v2)")
	edit("paid-media/paid-media-auditor.md", "# Paid Media Auditor Agent", "# Paid Media Auditor Agent (v2)")
	states := map[string]string{}
	for n, c := range candidateNames(probeSubagents(t, agency)) {
		states[n] = c.State
	}
	if states["unity-architect"] != "update" || states["paid-media-auditor"] != "edited" || states["backend-architect"] != "have" {
		t.Fatalf("states %v", states)
	}
	r := ok(t)(ImportSubagents(agency, nil, []string{"claude"}))
	if !slices.Equal(r.Subagents.Updated, []string{"unity-architect"}) || !slices.Equal(r.Subagents.Kept, []string{"paid-media-auditor"}) ||
		len(r.Subagents.Added) != 0 || len(r.Subagents.Had) != 2 {
		t.Fatalf("%+v", r.Subagents)
	}
	l := loaded(t)
	if len(l.Subagents) != 4 {
		t.Fatalf("%d subagents", len(l.Subagents))
	}
	if u := l.subagent("unity-architect"); !strings.Contains(u.Prompt, "(v2)") || !slices.Equal(u.Agents, []string{"codex"}) {
		t.Errorf("%+v", u)
	}
	if p := l.subagent("paid-media-auditor"); strings.Contains(p.Prompt, "(v2)") || !strings.HasSuffix(p.Prompt, "Always in euros.") {
		t.Errorf("the edited one: %q", p.Prompt[len(p.Prompt)-40:])
	}

	// the folder's link finds the same files
	p := probeSubagents(t, agency+"/tree/main/engineering")
	if len(p.Candidates) != 2 || p.Candidates[0].State != "have" || p.Candidates[1].State != "have" {
		t.Fatalf("%+v", p.Candidates)
	}
	// and one file's
	p = probeSubagents(t, agency+"/blob/main/game-development/unity/unity-architect.md")
	if len(p.Candidates) != 1 || p.Candidates[0].Path != "game-development/unity/unity-architect.md" || p.Candidates[0].State != "have" {
		t.Fatalf("%+v", p.Candidates)
	}
}

// A folder on this computer: a skill's folder is no subagent's, a file
// in an agents folder may be named by its file, and one the agents
// can't take is left out with why.
func TestSubagentsFromAFolder(t *testing.T) {
	h := sandbox(t)
	dir := filepath.Join(h, "src")
	write(t, filepath.Join(dir, "agents", "tester.md"), "---\ndescription: Runs the tests\nmode: subagent\n---\nRun them.\n")
	write(t, filepath.Join(dir, "agents", "main.md"), "---\nname: main\ndescription: Kept by Qwen\n---\nNo.\n")
	write(t, filepath.Join(dir, "commands", "fix.md"), "---\ndescription: A command\n---\nFix it.\n")
	write(t, filepath.Join(dir, "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: PDFs\n---\nRead PDFs.\n")
	write(t, filepath.Join(dir, "skills", "pdf", "forms.md"), "---\nname: forms\ndescription: Forms\n---\nForms.\n")
	p := probeSubagents(t, dir)
	if len(p.Candidates) != 1 || p.Candidates[0].Name != "tester" || p.Candidates[0].Path != "agents/tester.md" {
		t.Fatalf("%+v", p.Candidates)
	}
	if len(p.Left) != 1 || p.Left[0].Path != "agents/main.md" {
		t.Fatalf("%+v", p.Left)
	}
	ok(t)(ImportSubagents(dir, nil, nil))
	if c := probeSubagents(t, dir).Candidates[0]; c.State != "have" {
		t.Errorf("%+v", c)
	}
	if _, err := ProbeSubagents(filepath.Join(dir, "skills")); err == nil {
		t.Error("a folder of skills had subagents")
	}
}
