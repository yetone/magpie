package library

// A skill its source no longer has (スライム on Discord): the repository
// it came from was read, and its folder isn't there any more. An update
// then has nothing to fetch, which is no failure of the update: the skill
// is marked removed from its source (Skill.Gone), for the page to say so
// and offer to take out every such skill of a source at once. Nothing is
// taken out by itself, as the user may keep using it. Only a source read
// whole tells: a fetch that failed, or a skill left out of a fetch as too
// big, says nothing of whether it is still there.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// GoneError is a skill its source was read without: it was removed there.
type GoneError struct {
	Name   string
	Source string // the repository
}

func (e *GoneError) Error() string {
	return fmt.Sprintf("%s has no %s any more: it was removed from there", e.Source, e.Name)
}

// goneFrom is whether a fetched source folder, tmp, lacks the skill at
// path: neither the folder nor its SKILL.md is there. A skill the fetch
// left out as too big is there, only not fetched; a folder that can't be
// read is unknown, not gone.
func goneFrom(tmp, path string) bool {
	if leftOutAt(tmp, path) {
		return false
	}
	dir := filepath.Join(tmp, filepath.FromSlash(path))
	fi, err := os.Stat(dir)
	if err != nil {
		return absent(err)
	}
	if !fi.IsDir() {
		return true // a file stands where the skill's folder was
	}
	_, err = os.Stat(filepath.Join(dir, "SKILL.md"))
	return err != nil && absent(err)
}

// absent is whether err says a path isn't there, rather than that it
// couldn't be read.
func absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// leftOutAt is whether the fetch into dir left out the skill at path, or
// one inside it, as too big; unlike tookLeftOut it forgets nothing.
func leftOutAt(dir, path string) bool {
	leftOut.Lock()
	defer leftOut.Unlock()
	for _, l := range leftOut.m[dir] {
		if path == "" || l.Path == path || len(l.Path) > len(path) && l.Path[:len(path)+1] == path+"/" {
			return true
		}
	}
	return false
}

// markGone records which skills their sources were found without, and
// which were found there again.
func markGone(gone, there []string) {
	if len(gone) == 0 && len(there) == 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return
	}
	if l.setGone(gone, there) {
		_ = l.save()
	}
}

// setGone is markGone on a library already loaded; it says whether
// anything changed.
func (l *Library) setGone(gone, there []string) bool {
	now := time.Now().UTC().Format(time.RFC3339)
	changed := false
	for _, s := range l.Skills {
		switch {
		case slices.Contains(gone, s.Name) && s.Gone == "":
			s.Gone, changed = now, true
		case slices.Contains(there, s.Name) && s.Gone != "":
			s.Gone, changed = "", true
		}
	}
	return changed
}

// goneCheck is the check a skill found removed from its source keeps
// till it is updated, installed again or removed.
func goneCheck(e *GoneError) SkillCheck {
	return SkillCheck{Name: e.Name, Status: "gone", Error: e.Error()}
}

// RemoveGoneSkills takes out of the library, as RemoveSkills does, the
// skills named that their sources no longer have; with none named, every
// such skill. One found in its source again since is left, and said in
// Unremoved.
func RemoveGoneSkills(names []string) (*Result, error) {
	return RemoveGoneSkillsHow(names, true)
}

// RemoveGoneSkillsHow is RemoveGoneSkills, with keep false deleting their
// folders rather than moving them to the backups (RemoveSkillsHow).
func RemoveGoneSkillsHow(names []string, keep bool) (*Result, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	var take []string
	var left []Problem
	if names == nil {
		for _, s := range l.Skills {
			if s.Gone != "" {
				take = append(take, s.Name)
			}
		}
	} else {
		for _, name := range slices.Compact(slices.Sorted(slices.Values(names))) {
			switch s := l.skill(name); {
			case s == nil:
				left = append(left, Problem{What: "skill:" + name, Error: "no skill called " + name})
			case s.Gone == "":
				left = append(left, Problem{What: "skill:" + name, Error: name + " is in its source again; it's kept"})
			default:
				take = append(take, name)
			}
		}
	}
	if len(take) == 0 {
		if len(left) > 0 {
			return nil, errors.New(left[0].Error)
		}
		return nil, fmt.Errorf("no skill is removed from its source")
	}
	res, err := RemoveSkillsHow(take, keep)
	if res != nil {
		res.Unremoved = append(res.Unremoved, left...)
	}
	return res, err
}
