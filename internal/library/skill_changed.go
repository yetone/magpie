package library

// A skill from GitHub can be changed on this machine after it was
// installed: the user edits it in the library's folder, an agent it is
// linked into edits it there, or an agent's copy is edited and taken in
// (skill_edits.go). An update from GitHub takes its files in place of the
// library's, so it would undo such a change without a word (#1449). So
// the library tells a changed skill by the hash of its files as fetched
// (Skill.Hash): the page says it was changed here, an update of it alone
// asks first, an update of many leaves it as it is, and the version an
// update replaces is kept with the backups unless it is known to be
// GitHub's own.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// EditedError is an update refused because the skill was changed here
// since it was fetched from GitHub.
type EditedError struct{ Name string }

func (e *EditedError) Error() string {
	return fmt.Sprintf("%s was changed here since it was fetched from GitHub; updating it would replace those changes", e.Name)
}

// edits remembers, for each skill's library folder, its hash as of when
// anything in it last changed, so a page read doesn't read every skill's
// files again.
var edits = struct {
	sync.Mutex
	m map[string]editMemo
}{m: map[string]editMemo{}}

type editMemo struct {
	at   time.Time
	hash string
}

// libraryHash is the hash of the library's folder of a skill, leaving
// aside what isn't part of the skill (SkipInSkill): "" when it can't be
// read.
func libraryHash(name string) string {
	dir := skillDir(name)
	at := changedAt(dir)
	edits.Lock()
	m, ok := edits.m[dir]
	edits.Unlock()
	if ok && !at.IsZero() && m.at.Equal(at) {
		return m.hash
	}
	h := hashFiles(dir)
	if h != "" && !at.IsZero() {
		edits.Lock()
		edits.m[dir] = editMemo{at: at, hash: h}
		edits.Unlock()
	}
	return h
}

// editState tells whether a skill from GitHub was changed here since it
// was fetched: its library folder no longer holds the files fetched, or
// an agent's copy of it was edited since magpie made it and isn't taken
// in yet. known is false when that can't be told: magpie didn't keep the
// hash of what it fetched (installed before it did), or the folder can't
// be read.
func (l *Library) editState(s *Skill, targets []*Target) (edited, known bool) {
	if s.Source == nil || s.Source.Kind != "github" || s.Hash == "" {
		return false, false
	}
	lib := skillDir(s.Name)
	if linked(lib) {
		return false, false
	}
	h := libraryHash(s.Name)
	if h == "" {
		return false, false
	}
	// what a file manager writes into a folder the user opened isn't
	// part of the skill, so it is left out of both hashes alike
	if h != s.Hash {
		return true, true
	}
	for _, t := range targets {
		if t.Skills == "" || t.Desktop != nil || !slices.Contains(s.Agents, t.Agent.ID) {
			continue
		}
		p := filepath.Join(t.Skills, s.Name)
		if !ours(p, s.Name) || linked(p) {
			continue
		}
		made, ok := copiedAt(p)
		if !ok || !changedAt(p).After(made) {
			continue
		}
		if !sameTree(p, lib) {
			return true, true
		}
	}
	return false, true
}

// keepReplaced moves the library's version of a skill an update replaces
// to the backups, where it is found as an edit taken over is.
func keepReplaced(name, old string) (string, error) {
	dst := filepath.Join(BackupDir(), time.Now().Format("2006-01-02_15-04-05.000"), "library", "skills", name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	return dst, move(old, dst)
}
