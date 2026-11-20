package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A project's choices taken to another folder with a library of its own
// (#7278): its hooks and subagents are saved there from the export, given
// to the new folder by the same writers, beside what the folder has; what
// the other library lacks is named, and applying again changes nothing.
// None of the first folder's ownership records goes along.
func TestProjectExportApply(t *testing.T) {
	_, a, _ := pluginProjects(t)
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "session-start", Command: "say bark", Agents: []string{}}))
	s := reviewer
	s.Name, s.Agents = "checker", []string{}
	ok(t)(SaveSubagent("", s))
	ok(t)(ProjectHooks(a, []string{"bark"}, []string{"claude"}))
	ok(t)(ProjectSubagents(a, []string{s.Name}, []string{"claude"}))
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude"}))
	ok(t)(ProjectPi(a, "npm:pi-demo", "on"))

	e, err := ExportProject(a)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, own := range []string{"placed", "hookRecs", "agentFiles", "piRecs", "made", "wrote"} {
		if strings.Contains(string(b), `"`+own+`"`) {
			t.Errorf("the export carries %s: %s", own, b)
		}
	}
	if !strings.Contains(string(b), "say bark") || !strings.Contains(string(b), "You review code.") {
		t.Errorf("the hook's and subagent's definitions didn't go: %s", b)
	}

	// another computer: a library of its own, a folder of its own with
	// the user's hook in it, and none of the first folder
	h := sandbox(t)
	pluginAgents(t, h)
	var back ProjectExport
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	c := filepath.Join(t.TempDir(), "c")
	gitInit(t, c)
	f := filepath.Join(c, ".claude/settings.local.json")
	write(t, f, `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo mine"}]}]}}`)
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}

	out, err := ApplyProject(c, &back)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Saved, []string{"hook bark", "subagent " + s.Name}) {
		t.Errorf("saved %q", out.Saved)
	}
	if len(out.Missing) != 1 || !strings.HasPrefix(out.Missing[0], "plugin acme-kit") {
		t.Errorf("missing %q", out.Missing)
	}
	for _, r := range out.Res {
		if len(r.Problems) > 0 {
			t.Errorf("problems: %+v", r.Problems)
		}
	}
	l := loadT(t)
	if l.hook("bark") == nil || len(l.hook("bark").Agents) != 0 {
		t.Errorf("hook: %+v", l.hook("bark"))
	}
	if p := l.project(c); p == nil || !slices.Equal(p.Hooks["bark"], []string{"claude"}) || !slices.Equal(p.Subagents[s.Name], []string{"claude"}) {
		t.Fatalf("project: %+v", p)
	}
	got := read(t, f)
	if !strings.Contains(got, "say bark") || !strings.Contains(got, "echo mine") {
		t.Errorf("settings.local.json:\n%s", got)
	}
	there(t, filepath.Join(c, ".claude/agents", s.Name+".md"))
	if pi := read(t, filepath.Join(c, ".pi/settings.json")); strings.Count(pi, "npm:pi-demo") != 1 {
		t.Errorf(".pi/settings.json:\n%s", pi)
	}

	// again: nothing more
	out, err = ApplyProject(c, &back)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Saved) != 0 || len(out.Missing) != 1 {
		t.Errorf("again: saved %q, missing %q", out.Saved, out.Missing)
	}
	if n := strings.Count(read(t, f), "say bark"); n != 1 || !strings.Contains(read(t, f), "echo mine") {
		t.Errorf("again, %d entries:\n%s", n, read(t, f))
	}
	if pi := read(t, filepath.Join(c, ".pi/settings.json")); strings.Count(pi, "npm:pi-demo") != 1 {
		t.Errorf("again, .pi/settings.json:\n%s", pi)
	}
	if _, err := ApplyProject(c, &ProjectExport{Kind: "other"}); err == nil {
		t.Error("a file that isn't an export was applied")
	}
}
