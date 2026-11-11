package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// a Library as a remote magpie tells it: two skills and two prompt sets
func remoteLibrary(t *testing.T) *Bundle {
	t.Helper()
	sandbox(t)
	_, err := Put(&Bundle{
		Instructions: Instructions{Agents: []string{"claude"}, Sets: []InstrSet{{ID: "work", Name: "Work"}}, Active: "work"},
		Texts:        map[string]string{"default": "Be brief.", "work": "Reply in the user's language."},
		MCP:          []*Server{},
		Skills: []*CarriedSkill{
			{Name: "pdf", Agents: []string{"claude"}, Files: map[string][]byte{"SKILL.md": []byte("---\nname: pdf\ndescription: PDFs\n---\n")}},
			{Name: "notes", Agents: []string{"claude"}, Files: map[string][]byte{"SKILL.md": []byte("---\nname: notes\ndescription: Notes\n---\n"), "run.sh": []byte("echo 1\n")}, Exec: []string{"run.sh"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	// what travels is JSON
	j, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var out Bundle
	if err := json.Unmarshal(j, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func borrowedText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(skillDir(name), "SKILL.md"))
	if err != nil {
		return ""
	}
	return string(b)
}

func names(l *Library) []string {
	var out []string
	for _, s := range l.Skills {
		out = append(out, s.Name)
	}
	slices.Sort(out)
	return out
}

// KoiLx0 on X: a client uses the remote's skills and prompts, and what
// the user has stays theirs.
func TestBorrowRemoteLibrary(t *testing.T) {
	remote := remoteLibrary(t)
	if remote.MCP == nil || len(remote.MCP) != 0 {
		t.Fatalf("the servers stay on their computer: %v", remote.MCP)
	}

	// the client: a user's own skill of the same name, and their own prompt
	h := sandbox(t)
	skill(t, filepath.Join(h, "mine/pdf"), "pdf", "My PDFs")
	if _, err := InstallSkills(filepath.Join(h, "mine"), []string{"pdf"}, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	mine := borrowedText(t, "pdf")
	if mine == "" {
		t.Fatal("the user's skill wasn't installed")
	}

	// switched off: nothing is taken
	if _, err := Borrow("office", "Office", remote); err == nil {
		t.Fatal("a Library was taken with the switch off")
	}
	if _, err := SetBorrow("office", true); err != nil {
		t.Fatal(err)
	}
	r, err := Borrow("office", "Office", remote)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Added, "skill:notes") || !slices.Contains(r.Kept, "skill:pdf") {
		t.Fatalf("added %v, kept %v", r.Added, r.Kept)
	}
	if got := borrowedText(t, "pdf"); got != mine {
		t.Fatalf("the user's own pdf was written over: %q", got)
	}
	if borrowedText(t, "notes") == "" {
		t.Fatal("the remote's skill didn't arrive")
	}
	if fi, err := os.Stat(filepath.Join(skillDir("notes"), "run.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 && runtime.GOOS != "windows" {
		t.Fatalf("the script lost its mode: %v %v", fi, err)
	}
	l := mustLoad(t)
	if i := slices.IndexFunc(l.Skills, func(s *Skill) bool { return s.Name == "notes" }); i < 0 || !slices.Contains(l.Skills[i].Agents, "claude") {
		t.Fatalf("not given to the agents the remote gives it to: %+v", l.Skills)
	}
	// the prompts come as sets of their own, and the agents read the first
	// since they read nothing
	sid := setID("office", "work")
	if !l.hasSet(sid) || !l.hasSet(setID("office", "default")) {
		t.Fatalf("sets %+v", l.Instructions.Sets)
	}
	if l.active() != setID("office", "default") && l.active() != sid {
		t.Fatalf("active %q", l.active())
	}
	if setText(l.textPath(sid)) != "Reply in the user's language." {
		t.Fatalf("text %q", setText(l.textPath(sid)))
	}

	// a borrowed set is not passed on by Shared
	back, err := Shared()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range back.Skills {
		if s.Name == "notes" {
			t.Fatal("a borrowed skill was told on")
		}
	}
	for _, s := range back.Instructions.Sets {
		if strings.HasPrefix(s.ID, borrowedPrefix) {
			t.Fatal("a borrowed set was told on")
		}
	}

	// the remote changes a skill and drops one; the user edited neither
	remote.Skills = slices.DeleteFunc(remote.Skills, func(s *CarriedSkill) bool { return s.Name == "pdf" })
	for _, s := range remote.Skills {
		s.Files["SKILL.md"] = []byte("---\nname: notes\ndescription: Notes v2\n---\n")
	}
	if _, err := Borrow("office", "Office", remote); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(borrowedText(t, "notes"), "v2") {
		t.Fatalf("not updated: %q", borrowedText(t, "notes"))
	}

	// the user edits it: from then on it is theirs
	edited := "---\nname: notes\ndescription: My notes\n---\n"
	if err := os.WriteFile(filepath.Join(skillDir("notes"), "SKILL.md"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range remote.Skills {
		s.Files["SKILL.md"] = []byte("---\nname: notes\ndescription: Notes v3\n---\n")
	}
	r, err = Borrow("office", "Office", remote)
	if err != nil {
		t.Fatal(err)
	}
	if borrowedText(t, "notes") != edited || !slices.Contains(r.Kept, "skill:notes") {
		t.Fatalf("the user's edit was written over: %q %v", borrowedText(t, "notes"), r.Kept)
	}
	// the remote drops it: the user's edit stays
	remote.Skills = nil
	if _, err := Borrow("office", "Office", remote); err != nil {
		t.Fatal(err)
	}
	if borrowedText(t, "notes") != edited {
		t.Fatal("a skill the user changed was taken out")
	}

	// the user's own pdf and prompt are never touched by turning it off
	if _, err := SetBorrow("office", false); err != nil {
		t.Fatal(err)
	}
	l = mustLoad(t)
	if borrowedText(t, "pdf") != mine || !slices.Contains(names(l), "pdf") || !slices.Contains(names(l), "notes") {
		t.Fatalf("turning it off took the user's: %v", names(l))
	}
	if l.hasSet(sid) {
		t.Fatalf("its sets stayed: %+v", l.Instructions.Sets)
	}
}

// A skill taken and not changed goes when the remote drops it or the
// switch goes off; one the user removed is not brought back.
func TestBorrowedSkillGoesWithTheRemote(t *testing.T) {
	remote := remoteLibrary(t)
	sandbox(t)
	if _, err := SetBorrow("office", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Borrow("office", "Office", remote); err != nil {
		t.Fatal(err)
	}
	if got := names(mustLoad(t)); !slices.Equal(got, []string{"notes", "pdf"}) {
		t.Fatalf("%v", got)
	}
	if _, err := RemoveSkill("pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := Borrow("office", "Office", remote); err != nil {
		t.Fatal(err)
	}
	if got := names(mustLoad(t)); !slices.Equal(got, []string{"notes"}) {
		t.Fatalf("a skill the user removed came back: %v", got)
	}
	if _, err := SetBorrow("office", false); err != nil {
		t.Fatal(err)
	}
	if got := names(mustLoad(t)); len(got) != 0 {
		t.Fatalf("left after switching off: %v", got)
	}
}
