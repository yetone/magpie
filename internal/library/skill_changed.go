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
//
// A skill may ask to have its dependencies installed in its own folder
// (npm install in easyeda-api-skill; ggbr on Discord). What that writes
// (node_modules, a Python virtualenv, bytecode, the lock file written
// again) is the install's, not a change of the user's: it neither marks
// the skill changed nor is lost to an update, which carries it over to
// the version fetched (carryInstalled).

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// installedDirs are the folders installing a skill's dependencies, or
// running it, makes in it, wherever in it they are.
var installedDirs = map[string]bool{
	"node_modules":  true,
	".venv":         true,
	"venv":          true,
	"__pycache__":   true,
	".pytest_cache": true,
	".mypy_cache":   true,
	".ruff_cache":   true,
}

// lockFiles are what a package manager writes again as it installs, even
// over the one the skill came with.
var lockFiles = map[string]bool{
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"bun.lock":          true,
	"bun.lockb":         true,
	"uv.lock":           true,
}

// fromInstall is whether an entry of a skill's folder called name was
// written by installing its dependencies or running it rather than by
// hand: one of installedDirs (a folder, or a link to one), compiled
// Python, and with locks a lock file.
func fromInstall(name string, dir, locks bool) bool {
	if dir && installedDirs[name] {
		return true
	}
	if dir {
		return false
	}
	if strings.HasSuffix(name, ".pyc") || strings.HasSuffix(name, ".pyo") {
		return true
	}
	return locks && lockFiles[name]
}

// ownHash is the hash of a skill's folder leaving aside, as well as what
// Finder keeps in it, what installing its dependencies writes: what is
// the skill's own. "" when it can't be read.
func ownHash(dir string) string { return rememberedHash(dir, leaveInstalled) }

// EditedError is an update refused because the skill was changed here
// since it was fetched from GitHub, or From, the git repository it came
// from.
type EditedError struct{ Name, From string }

func (e *EditedError) Error() string {
	from := e.From
	if from == "" {
		from = "GitHub"
	}
	return fmt.Sprintf("%s was changed here since it was fetched from %s; updating it would replace those changes", e.Name, from)
}

// editedError is the refusal of an update of s, naming where it came from.
func editedError(s *Skill) *EditedError {
	e := &EditedError{Name: s.Name}
	if s.Source != nil && s.Source.Kind == "git" {
		e.From = s.Source.Repo
	}
	return e
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
// aside what Finder keeps in it: "" when it can't be read.
func libraryHash(name string) string {
	dir := skillDir(name)
	// a folder not wholly read has no time to remember its hash by
	at, err := changedAt(dir)
	if err != nil {
		at = time.Time{}
	}
	edits.Lock()
	m, ok := edits.m[dir]
	edits.Unlock()
	if ok && !at.IsZero() && m.at.Equal(at) {
		return m.hash
	}
	h := hashFiles(dir, true)
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
// be read, or it is Pending: its folder isn't the version Hash names.
func (l *Library) editState(s *Skill, targets []*Target) (edited, known bool) {
	if !s.Source.remote() || s.Hash == "" || s.Pending {
		return false, false
	}
	lib := skillDir(s.Name)
	if linked(lib) {
		return false, false
	}
	// asked first: the hashes after it read every installed dependency
	if !onlyInstalled(s, lib) {
		h := libraryHash(s.Name)
		if h == "" {
			return false, false
		}
		// the fetched files may hold a .DS_Store of their own
		if h != s.Hash && hashDir(lib) != s.Hash {
			return true, true
		}
	}
	for _, t := range targets {
		if t.Skills == "" || t.Desktop != nil || !s.gives(t.Agent.ID) {
			continue
		}
		p := filepath.Join(t.Skills, s.Name)
		if !ours(p, s.Name) || linked(p) {
			continue
		}
		made, ok := copiedAt(p)
		if !ok {
			continue
		}
		// a copy not wholly read can't be told unchanged; the sync keeps
		// it in place, and an update keeps the library's version aside
		at, err := changedAt(p)
		if err != nil {
			return false, false
		}
		if !at.After(made) {
			continue
		}
		if !sameTree(p, lib) && ownHash(p) != ownHash(lib) {
			return true, true
		}
	}
	return false, true
}

// onlyInstalled is whether the library's folder of s differs from the
// files fetched only by what installing its dependencies wrote. Own is
// kept from when this was known; one fetched before has only Hash, and
// is the same when the lock file it may have come with was left as it
// was, or it came with none.
func onlyInstalled(s *Skill, lib string) bool {
	if s.Own != "" {
		h := ownHash(lib)
		return h != "" && h == s.Own
	}
	if h := rememberedHash(lib, leaveBuilt); h != "" && h == s.Hash {
		return true
	}
	h := ownHash(lib)
	return h != "" && h == s.Hash
}

// carryInstalled moves what installing a skill's dependencies wrote in
// its folder as it was, old, to the folder an update put in its place,
// so they needn't be installed again: what the new version doesn't have
// itself. What can't be moved stays in old.
func carryInstalled(old, dir string) {
	filepath.WalkDir(old, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == old {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		isDir := d.IsDir() || d.Type()&fs.ModeSymlink != 0
		if !fromInstall(d.Name(), isDir, true) {
			return nil
		}
		rel, _ := filepath.Rel(old, p)
		dst := filepath.Join(dir, rel)
		if _, err := os.Lstat(dst); err == nil {
			// the new version's own, as fetched
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err == nil {
			if err := os.Rename(p, dst); err != nil {
				log.Printf("library: %s stays with the version replaced: %v", rel, err)
			}
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
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
