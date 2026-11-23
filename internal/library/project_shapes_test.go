package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// #7282 (jinpwng): a project whose pi maps a source to its agents, or whose
// claudePlugins maps an id to on, as another build wrote them, made the
// whole library unreadable, and no change could be made. Both are read as
// magpie's shapes; the file's bytes stay until a change, which keeps them
// beside it first and then writes magpie's shape. A shape that is neither
// still fails the read, and a write leaves the file alone.
func TestProjectReadsOtherShapes(t *testing.T) {
	sandbox(t)
	dir := filepath.Join(t.TempDir(), "a")
	gitInit(t, dir)
	other := filepath.Join(t.TempDir(), "b")
	gitInit(t, other)
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(project string) []byte {
		t.Helper()
		b := []byte(`{"projects":[` + project + `]}` + "\n")
		if err := os.WriteFile(path(), b, 0o600); err != nil {
			t.Fatal(err)
		}
		return b
	}
	q, _ := json.Marshal(dir)

	was := put(`{"dir":` + string(q) + `,"pi":{"npm:example":["pi"],"npm:none":[],"git:github.com/x/y":"off"},"claudePlugins":{"demo@local":true,"off@local":false}}`)
	l, err := loadLocked()
	if err != nil {
		t.Fatalf("the library isn't read: %v", err)
	}
	p := l.project(dir)
	if p == nil {
		t.Fatal("the project is gone")
	}
	if want := map[string]string{"npm:example": "on", "npm:none": "off", "git:github.com/x/y": "off"}; !reflect.DeepEqual(p.Pi, want) {
		t.Errorf("pi = %v, want %v", p.Pi, want)
	}
	if want := []string{"demo@local"}; !reflect.DeepEqual(p.ClaudePlugins, want) {
		t.Errorf("claudePlugins = %v, want %v", p.ClaudePlugins, want)
	}
	if b, _ := os.ReadFile(path()); string(b) != string(was) {
		t.Errorf("a read changed the file:\n%s", b)
	}

	// an unrelated change goes through, the old bytes kept first
	ok(t)(AddProject(other))
	if b, err := os.ReadFile(path() + ".before-reshape"); err != nil || string(b) != string(was) {
		t.Errorf("the file as it was isn't kept (%v):\n%s", err, b)
	}
	b, _ := os.ReadFile(path())
	for _, s := range []string{`"npm:example": "on"`, `"claudePlugins": [`, `"demo@local"`} {
		if !strings.Contains(string(b), s) {
			t.Errorf("the file isn't in magpie's shape (%s):\n%s", s, b)
		}
	}
	if strings.Contains(string(b), "off@local") {
		t.Errorf("a plugin that was off is turned on:\n%s", b)
	}
	// written once in magpie's shape, a later write keeps no new copy
	if err := os.Remove(path() + ".before-reshape"); err != nil {
		t.Fatal(err)
	}
	ok(t)(ProjectPi(other, "npm:later", "on"))
	if _, err := os.Stat(path() + ".before-reshape"); err == nil {
		t.Error("a library in magpie's shape is copied again")
	}

	// neither shape: unknown, and nothing is written over it
	bad := put(`{"dir":` + string(q) + `,"claudePlugins":"demo@local"}`)
	if _, err := loadLocked(); err == nil {
		t.Error("a claudePlugins that is a string is read")
	}
	if _, err := AddProject(other); err == nil {
		t.Error("a change went through over an unreadable library")
	}
	if b, _ := os.ReadFile(path()); string(b) != string(bad) {
		t.Errorf("an unreadable library was written over:\n%s", b)
	}
}

// #7282 (jinpwng): a build that kept pi by category, {"package": [...],
// "extension": [...]} with piOff, has each package read with its state,
// adopted where the project's file declares it, and its extension shown
// as not carried; the category names are never sources.
func TestProjectReadsPiByCategory(t *testing.T) {
	sandbox(t)
	dir := filepath.Join(t.TempDir(), "a")
	gitInit(t, dir)
	file := filepath.Join(dir, ".pi", "settings.json")
	// as that build left the project's own file
	write(t, file, `{"packages":["npm:pi-a","npm:pi-b"]}`+"\n")
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(dir)
	lib := `{"projects":[{"dir":` + string(q) + `,"pi":{"package":["npm:pi-a","npm:pi-b"],"extension":["./ext/x.ts"]},"piOff":["npm:pi-b"]}]}` + "\n"
	if err := os.WriteFile(path(), []byte(lib), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	p := l.project(dir)
	if want := map[string]string{"npm:pi-a": "on", "npm:pi-b": "off"}; !reflect.DeepEqual(p.Pi, want) {
		t.Errorf("pi = %v, want %v", p.Pi, want)
	}
	if want := []PiNotCarried{{"extension", "./ext/x.ts"}}; !reflect.DeepEqual(p.PiNotCarried, want) {
		t.Errorf("not carried = %v, want %v", p.PiNotCarried, want)
	}

	// the first write: the packages adopted, b turned off, nothing named
	// package or extension anywhere
	other := filepath.Join(t.TempDir(), "other")
	gitInit(t, other)
	ok(t)(AddProject(other))
	got := strings.Join(strings.Fields(read(t, file)), "")
	if want := `{"packages":["npm:pi-a",{"extensions":[],"prompts":[],"skills":[],"source":"npm:pi-b","themes":[]}]}`; got != want {
		t.Errorf("settings:\n%s\nwant\n%s", got, want)
	}
	b, _ := os.ReadFile(path())
	for _, s := range []string{`"package"`, `"extension":`} {
		if strings.Contains(string(b), s) {
			t.Errorf("the library keeps %s:\n%s", s, b)
		}
	}
	var v ProjectView
	for _, x := range View0(t).Projects {
		if x.Dir == dir {
			v = x
		}
	}
	if len(v.Pi) != 2 || !v.Pi[0].Adopted || !v.Pi[1].Adopted || len(v.PiNotCarried) != 1 {
		t.Errorf("view: pi %+v, not carried %+v", v.Pi, v.PiNotCarried)
	}
	// letting go of an adopted package gives the project its line back
	ok(t)(ProjectPi(dir, "npm:pi-b", "removed"))
	if got := strings.Join(strings.Fields(read(t, file)), ""); got != `{"packages":["npm:pi-a","npm:pi-b"]}` {
		t.Errorf("after removing b: %s", got)
	}
}

// #7282: what v0.1.4702 wrote from a category-keyed pi, the literals
// "package" and "extension" as two sources in library.json and in the
// .pi/settings.json of the project and its worktree, is taken out again,
// and the real sources read back from the copy kept before that write.
func TestProjectPiStraysTakenOut(t *testing.T) {
	sandbox(t)
	dir := filepath.Join(t.TempDir(), "a")
	gitInit(t, dir)
	// git worktree add ../a-wt
	wt := filepath.Join(filepath.Dir(dir), "a-wt")
	own := filepath.Join(dir, ".git", "worktrees", "a-wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+own+"\n")
	write(t, filepath.Join(own, "gitdir"), filepath.Join(wt, ".git")+"\n")
	write(t, filepath.Join(own, "commondir"), "../..\n")
	for _, d := range []string{dir, wt} {
		write(t, filepath.Join(d, ".pi", "settings.json"), `{"packages":["npm:pi-a","package","extension"]}`+"\n")
	}
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(dir)
	qt, _ := json.Marshal(wt)
	before := `{"projects":[{"dir":` + string(q) + `,"pi":{"package":["npm:pi-a"],"extension":["./ext/x.ts"]}}]}` + "\n"
	if err := os.WriteFile(path()+".before-reshape", []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	recs := `"pi":{"extension":"on","package":"on"},"piRecs":[{"source":"extension","entry":"extension"},{"source":"package","entry":"package"}]`
	lib := `{"projects":[{"dir":` + string(q) + `,` + recs + `,"trees":[{"dir":` + string(qt) + `,` + recs + `}]}]}` + "\n"
	if err := os.WriteFile(path(), []byte(lib), 0o600); err != nil {
		t.Fatal(err)
	}

	keepProjectTrees() // at start, as the app does
	for _, d := range []string{dir, wt} {
		if got := strings.Join(strings.Fields(read(t, filepath.Join(d, ".pi", "settings.json"))), ""); got != `{"packages":["npm:pi-a"]}` {
			t.Errorf("%s settings: %s", d, got)
		}
	}
	l, err := loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	p := l.project(dir)
	if want := map[string]string{"npm:pi-a": "on"}; !reflect.DeepEqual(p.Pi, want) {
		t.Errorf("pi = %v, want %v", p.Pi, want)
	}
	if len(p.PiRecs) != 1 || p.PiRecs[0].Orig == nil {
		t.Errorf("npm:pi-a isn't adopted: %+v", p.PiRecs)
	}
	if want := []PiNotCarried{{"extension", "./ext/x.ts"}}; !reflect.DeepEqual(p.PiNotCarried, want) {
		t.Errorf("not carried = %v, want %v", p.PiNotCarried, want)
	}
	if b, _ := os.ReadFile(path()); strings.Contains(string(b), `"package"`) {
		t.Errorf("the library keeps the literal:\n%s", b)
	}
	// and turning it off reaches the worktree as well
	ok(t)(ProjectPi(dir, "npm:pi-a", "off"))
	if s := read(t, filepath.Join(wt, ".pi", "settings.json")); !strings.Contains(s, `"extensions": []`) {
		t.Errorf("the worktree's pi-a isn't off:\n%s", s)
	}
}
