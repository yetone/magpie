package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// HandOverSkills gives skills kept in ~/.agents/skills to the library to
// manage (tnt1223 on Discord, "统一移入 magpie 的资源库文件夹，交给 magpie
// 管理"). Several agents read that folder themselves, so a skill there is
// on for each of them whatever the library ticks, and turning it off for
// one, or on this computer alone (OffHere), can't work. Handed over:
//   - a folder kept in ~/.agents/skills moves into the library's own folder
//     (renamed, or copied, checked the same and only then set aside with
//     the backups), and its entry there is gone;
//   - an entry there that only links to a folder elsewhere is taken away,
//     and the library links to that folder past it;
//   - the user's own links to it in the agents' folders, which would point
//     at nothing, are taken away too.
//
// Every agent that read it there, or through such a link, is ticked for
// it, so each goes on having it, now from the library, till it's unticked.
// A skill not in the library yet (found in ~/.agents/skills) is brought in
// first. One that can't be handed over stays as it was, in Unmoved, and
// the others are handed over all the same. It also turns off the setting
// that keeps every skill in ~/.agents/skills. HandBackSkill undoes it.
func HandOverSkills(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no skills to hand over")
	}
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	var failed, partly []Problem
	res, err := change(func(l *Library) error {
		var found []FoundSkill
		ts := Targets()
		for _, name := range names {
			left, err := handOver(l, &found, ts, name)
			if err != nil {
				failed = append(failed, Problem{What: "skill:" + name, Error: err.Error()})
			} else if left != "" {
				partly = append(partly, Problem{Agent: "agents", What: "skill:" + name, Error: left})
			}
		}
		if len(failed) == len(names) {
			return errors.New(failed[0].Error)
		}
		// Choosing which agents get a skill and the setting that puts every
		// skill in ~/.agents/skills (SetSkillsWhere) can't both hold: with
		// it on, every agent reading that folder has every skill and magpie
		// links each one back there. It goes off, and the sync takes out
		// magpie's own links there, never the user's entries
		// (ConatusMoon2222 on X).
		l.SharedSkills = false
		return nil
	})
	if res != nil {
		res.Unmoved = failed
		res.Problems = append(res.Problems, partly...)
	}
	return res, err
}

// SharedSkills are the names of the library's skills kept in
// ~/.agents/skills, which HandOverSkills can move into the library.
func SharedSkills() ([]string, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range l.Skills {
		if sharedHas(s.Name) {
			out = append(out, s.Name)
		}
	}
	return out, nil
}

// readsSharedHere are the agents here that read ~/.agents/skills: those
// that read it besides their own folder, and those whose folder it is.
func readsSharedHere(ts []*Target) []string {
	sh := realDir(sharedSkillsDir())
	var out []string
	for _, t := range ts {
		if t.Skills != "" && !t.Gone && t.Desktop == nil && (t.readsSharedDir() || realDir(t.Skills) == sh) {
			out = append(out, t.Agent.ID)
		}
	}
	return out
}

// handOver hands one skill over (HandOverSkills). left says what of the
// original couldn't be taken away after a whole copy was made.
func handOver(l *Library, found *[]FoundSkill, ts []*Target, name string) (left string, err error) {
	sh := realDir(sharedSkillsDir())
	if l.skill(name) == nil {
		if *found == nil {
			*found = foundSkills(l)
		}
		i := slices.IndexFunc(*found, func(f FoundSkill) bool { return f.Name == name })
		if i < 0 || (*found)[i].Shared == "" && !within((*found)[i].real, sh) {
			return "", fmt.Errorf("no skill called %s in %s", name, sharedSkillsDir())
		}
		if err := importSkill(l, *found, name); err != nil {
			return "", err
		}
	}
	s := l.skill(name)
	entry := sharedEntry(name)
	if entry == "" {
		return "", fmt.Errorf("%s isn't kept in %s", name, sharedSkillsDir())
	}
	lib := skillDir(name)
	r := realDir(lib)
	agents := readsSharedHere(ts)
	// the user's own links to it in the agents' folders: they'd point at
	// nothing once it moves, and the library gives the agent its own
	var strays []string
	for _, t := range ts {
		if t.Skills == "" || t.Desktop != nil || realDir(t.Skills) == sh {
			continue
		}
		q := filepath.Join(t.Skills, name)
		if linked(q) && !ours(q, name) && realDir(q) == r {
			strays = append(strays, q)
			agents = append(agents, t.Agent.ID)
		}
	}
	if within(r, sh) {
		// the folder itself is in the shared one: it moves in
		if !linked(lib) {
			return "", fmt.Errorf("%s is in the library's folder and in %s at once", name, sharedSkillsDir())
		}
		if left, err = moveIn(r, lib); err != nil {
			return "", err
		}
		if e := filepath.Clean(entry); e != r && linked(e) {
			// a link in the shared folder to it there, by its name
			if err := os.Remove(e); err != nil && !errors.Is(err, fs.ErrNotExist) {
				left = err.Error()
			}
		}
		s.Source = nil
	} else {
		// the entry only links to a folder elsewhere (or to the library's
		// own): the library links past it, and only the link goes
		if !linked(entry) {
			return "", fmt.Errorf("%s in %s is a folder of its own", name, sharedSkillsDir())
		}
		if linked(lib) {
			via := linkVia(lib, r)
			old, _ := os.Readlink(lib)
			if filepath.Clean(old) != filepath.Clean(via) {
				if err := os.Remove(lib); err != nil {
					return "", err
				}
				if err := dirLink(via, lib); err != nil {
					if rerr := dirLink(old, lib); rerr != nil {
						return "", fmt.Errorf("%w; and the library's link to %s couldn't be made again: %v", err, old, rerr)
					}
					return "", err
				}
			}
			s.Source = &Source{Kind: "folder", Dir: via}
		}
		if err := os.Remove(entry); err != nil {
			return "", err
		}
	}
	for _, q := range strays {
		os.Remove(q) // a link: what it pointed at is the library's now
	}
	s.Agents = slices.Compact(slices.Sorted(slices.Values(append(s.Agents, agents...))))
	return left, nil
}

// moveIn puts the folder from in the place of the library's link lib to
// it: renamed, or where that can't be (another disk), copied, checked to
// hold the very same files, and only then set aside with the backups. On
// a failure the folder stays where it was and lib links to it as before.
// left is what of from couldn't be taken away after its whole copy was
// in (a file in it held open, on Windows).
func moveIn(from, lib string) (left string, err error) {
	tmp := filepath.Join(filepath.Dir(lib), "."+filepath.Base(lib)+".magpie-in-"+time.Now().Format("20060102150405.000"))
	renamed := os.Rename(from, tmp) == nil
	if !renamed {
		if err := copyDir(from, tmp); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
		// a copy that can't be read back the same is no copy: the
		// original stays where it is
		if !sameTree(from, tmp) {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("the copy of %s didn't read back the same; it stays where it is", from)
		}
	}
	back := func() {
		if renamed {
			os.Rename(tmp, from)
		} else {
			os.RemoveAll(tmp)
		}
	}
	old, err := os.Readlink(lib)
	if err != nil {
		back()
		return "", err
	}
	if err := os.Remove(lib); err != nil {
		back()
		return "", err
	}
	if err := os.Rename(tmp, lib); err != nil {
		dirLink(old, lib)
		back()
		return "", err
	}
	if renamed {
		return "", nil
	}
	if _, err := setAside("agents", from); err != nil {
		var lb *leftBehind
		if errors.As(err, &lb) {
			// whole in the library and in the backups; what is left of
			// it where it was is said
			return fmt.Sprintf("moved into the library, but part of %s couldn't be taken away: %v", from, err), nil
		}
		// nothing of it moved: as it was before
		os.RemoveAll(lib)
		dirLink(old, lib)
		return "", err
	}
	return "", nil
}

// HandBackSkill puts a skill handed over (HandOverSkills) back into
// ~/.agents/skills: its folder moves there, and the library links to it,
// as it did before. Which agents it's ticked for stays as it is; those
// that read ~/.agents/skills have it again whatever is ticked.
func HandBackSkill(name string) (*Result, error) {
	return change(func(l *Library) error {
		s := l.skill(name)
		if s == nil {
			return fmt.Errorf("no skill called %s", name)
		}
		lib := skillDir(name)
		if linked(lib) {
			return fmt.Errorf("%s isn't kept in the library's folder: it links to %s", name, realDir(lib))
		}
		if fi, err := os.Stat(lib); err != nil || !fi.IsDir() {
			return fmt.Errorf("%s has no folder in the library: %v", name, err)
		}
		p := filepath.Join(sharedSkillsDir(), name)
		if _, err := os.Lstat(p); err == nil {
			if !ours(p, name) {
				return fmt.Errorf("%s has a %s of its own", sharedSkillsDir(), name)
			}
			// magpie's own link there (for Goose, Kimi Code or Cindy)
			if err := unlink(p); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(sharedSkillsDir(), 0o755); err != nil {
			return err
		}
		if err := os.Rename(lib, p); err != nil {
			if err := copyDir(lib, p); err != nil {
				os.RemoveAll(p)
				return err
			}
			if !sameTree(lib, p) {
				os.RemoveAll(p)
				return fmt.Errorf("the copy of %s didn't read back the same; it stays in the library", name)
			}
			if _, err := setAside("library", lib); err != nil {
				var lb *leftBehind
				if !errors.As(err, &lb) {
					os.RemoveAll(p)
				}
				return err
			}
		}
		if err := dirLink(p, lib); err != nil {
			if rerr := move(p, lib); rerr != nil {
				return fmt.Errorf("%w; the skill is at %s", err, p)
			}
			return err
		}
		s.Source = &Source{Kind: "folder", Dir: p}
		return nil
	})
}
