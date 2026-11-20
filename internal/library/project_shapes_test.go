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
