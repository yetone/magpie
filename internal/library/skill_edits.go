package library

// An agent given its skills as copies (#896) may change one of them: the
// agent itself edits the skill, or the user does in that agent's folder.
// The copy then differs from the library's skill, as it does when the
// library's has changed, and a sync made it again from the library's,
// which undid the edit at the next start of magpie (Joren on Discord). So
// a sync first tells the two apart by magpie's mark, written as the copy
// was made: a copy with anything in it newer than its mark was edited since,
// and the library takes the newest such edit, which the sync then gives to
// every other agent. The library's own change wins when it is newer still
// (an update, a folder edited by hand). An edit that loses, to the library
// or to a newer edit in another agent, is never lost: it is kept aside
// with the backups, as an agent's own skill is.

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// takeEdits takes into the library the newest edit made in an agent's copy
// of each of its skills, before the sync gives the library's to the agents.
func (l *Library) takeEdits(targets []*Target, res *Result) {
	for _, s := range l.Skills {
		lib := realDir(skillDir(s.Name))
		if l.libHash(s.Name) == "" {
			continue
		}
		type edit struct {
			agent, path string
			at          time.Time
		}
		var edits []edit
		seen := map[string]bool{}
		for _, t := range targets {
			if t.Skills == "" || t.Desktop != nil || !slices.Contains(s.Agents, t.Agent.ID) {
				continue
			}
			p := filepath.Join(t.Skills, s.Name)
			if !ours(p, s.Name) || linked(p) || seen[realDir(p)] {
				continue
			}
			seen[realDir(p)] = true
			made, ok := copiedAt(p)
			if !ok {
				continue
			}
			at := changedAt(p)
			if !at.After(made) {
				if l.untouched == nil {
					l.untouched = map[string]bool{}
				}
				l.untouched[p] = true
				continue
			}
			if sameTree(p, lib) {
				continue
			}
			edits = append(edits, edit{t.Agent.ID, p, at})
		}
		if len(edits) == 0 {
			continue
		}
		// the newest edit, unless the library changed after it
		slices.SortStableFunc(edits, func(a, b edit) int { return b.at.Compare(a.at) })
		won := -1
		if edits[0].at.After(changedAt(lib)) {
			won = 0
		}
		if won == 0 {
			e := edits[0]
			// the library's as it was, kept with the backups
			if _, err := keepLibrarySkill(s.Name, lib); err != nil {
				res.fail(e.agent, "skill:"+s.Name, err)
				continue
			}
			err := mirrorDir(e.path, lib)
			delete(l.hashes, s.Name)
			if err != nil {
				res.fail(e.agent, "skill:"+s.Name, err)
				continue
			}
			log.Printf("library: %s changed %s in its copy; the library takes it, and the other agents get it", e.agent, s.Name)
			// made again, with a mark newer than the edit, so the copy isn't
			// taken for edited once the library's changes after it
			if err := copyIn(e.path, s.Name); err != nil {
				res.fail(e.agent, "skill:"+s.Name, err)
			}
			res.changed(e.agent)
		}
		for i, e := range edits {
			if i == won {
				continue
			}
			// lost to a newer change: kept aside, and the sync makes the
			// copy again from the library's
			if dst, err := setAside(e.agent, e.path); err != nil {
				res.fail(e.agent, "skill:"+s.Name, err)
			} else {
				log.Printf("library: %s's edit of %s lost to a newer change; kept at %s", e.agent, s.Name, dst)
				res.changed(e.agent)
			}
		}
	}
}

// copiedAt is when magpie made the copy at p: its mark's time.
func copiedAt(p string) (time.Time, bool) {
	fi, err := os.Stat(filepath.Join(p, marker))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// changedAt is when anything in the folder last changed: the newest time
// of what is in it, a file or a folder (one taken away changes its
// folder's), leaving aside magpie's mark and what Finder or version
// control keep in it.
func changedAt(dir string) time.Time {
	var at time.Time
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != dir && skipInSkill(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(at) {
			at = fi.ModTime()
		}
		return nil
	})
	return at
}

func skipInSkill(name string) bool {
	return name == marker || name == ".git" || name == ".DS_Store"
}

// keepLibrarySkill copies the library's skill to the backups before an
// edit is taken over it.
func keepLibrarySkill(name, lib string) (string, error) {
	dst := filepath.Join(BackupDir(), time.Now().Format("2006-01-02_15-04-05.000"), "library", "skills", name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	return dst, copyDir(lib, dst)
}

// mirrorDir makes the folder to hold what from does: its files written
// over to's, and what to has that from doesn't taken away, leaving aside
// magpie's mark and what Finder or version control keep. File by file, so
// a library skill that is a folder of the user's stays that folder.
func mirrorDir(from, to string) error {
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if p != from && skipInSkill(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(to, rel)
		switch {
		case d.IsDir():
			if fi, err := os.Lstat(dst); err == nil && !fi.IsDir() {
				os.Remove(dst)
			}
			return os.MkdirAll(dst, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return nil
			}
			if cur, err := os.Readlink(dst); err == nil && cur == t {
				return nil
			}
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
			return os.Symlink(t, dst)
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if fi, err := os.Lstat(dst); err == nil && !fi.Mode().IsRegular() {
				if err := os.RemoveAll(dst); err != nil {
					return err
				}
			}
			mode := fs.FileMode(0o644)
			if fi, err := d.Info(); err == nil {
				mode = fi.Mode().Perm()
			}
			return os.WriteFile(dst, b, mode)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(to, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == to {
			return err
		}
		if skipInSkill(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(to, p)
		if _, err := os.Lstat(filepath.Join(from, rel)); err == nil {
			return nil
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}
