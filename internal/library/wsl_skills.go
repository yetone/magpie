package library

// Skills in a WSL distro (cutemmuu on X). An agent there can't follow a
// link to the library's folder on Windows, and Windows can't make a link
// there that the distro follows (one os.Symlink makes through
// \\wsl.localhost is an empty folder in the distro), so each agent in a
// distro used to get a copy of its own of every skill. Now the distro
// holds one copy of each skill its agents are given, magpie's, in
// ~/.local/share/magpie/skills (Target.Store); each agent there gets a
// link to it, made in the distro by wsl.exe (agent.WSLSymlink), as an
// agent on Windows gets one to the library's folder. An edit made in an
// agent through its link is in that copy, which takeEdits looks at as it
// does at any copy of magpie's. An agent set to take copies (SkillHow)
// still gets a copy of its own.
//
// The copy isn't put in the distro's ~/.agents/skills: every agent there
// that reads that folder would find each skill in it, ticked for it or
// not, and the ticks are per agent.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/agent"
)

// wslStoreRel is magpie's folder for skills in a distro, below its home.
var wslStoreRel = []string{".local", "share", "magpie", "skills"}

// wslStored gives t, an agent's target in a WSL distro whose home magpie
// opens at home, the distro's store of skills, unless it has no skills.
// An agent whose skills folder is on a Windows drive (its folder a link
// to /mnt/c/…, #4502), or a store that is, keeps copies of its own: a
// link the distro makes there is one Windows can neither read nor follow.
func wslStored(t *Target, home string) {
	if t.Skills == "" || home == "" {
		return
	}
	store := wslOpen(t, filepath.Join(append([]string{home}, wslStoreRel...)...))
	if !agent.WSLInDistro(t.Agent, t.Skills) || !agent.WSLInDistro(t.Agent, store) {
		t.Copy = true
		return
	}
	t.Store = store
}

// inWSLStore is whether to, a path as magpie opens it, is a skill by that
// name in a distro's store: what magpie's link in an agent there leads to.
func inWSLStore(to, name string) bool {
	parts := strings.FieldsFunc(filepath.ToSlash(to), func(r rune) bool { return r == '/' || r == '\\' })
	n := len(parts)
	if n < len(wslStoreRel)+1 || parts[n-1] != name {
		return false
	}
	for i, p := range wslStoreRel {
		if !strings.EqualFold(parts[n-1-len(wslStoreRel)+i], p) {
			return false
		}
	}
	return true
}

// storeSkill makes the distro's copy of the skill (t.Store), or makes it
// again when the library's has changed since; looked at once a sync step
// for each store, and changed says this call made it. A folder there
// that isn't magpie's copy is never touched.
func (l *Library) storeSkill(t *Target, name string) (changed bool, err error) {
	p := filepath.Join(t.Store, name)
	key := strings.ToLower(p)
	if err, ok := l.stored[key]; ok {
		return false, err
	}
	changed, err = func() (bool, error) {
		fi, err := os.Lstat(p)
		if err != nil {
			return true, copyIn(p, name)
		}
		if !fi.IsDir() || !ours(p, name) {
			return false, errors.New(p + " is in the way of magpie's copy of " + name + " in WSL")
		}
		dropOld(p, name)
		h := l.libHash(name)
		if h == "" {
			return false, nil
		}
		current, err := copyIsFresh(p, h, l.untouched[p])
		if err != nil || current {
			return false, err
		}
		return true, copyIn(p, name)
	}()
	if l.stored == nil {
		l.stored = map[string]error{}
	}
	l.stored[key] = err
	return changed && err == nil, err
}

// wslRelink is relink for t, an agent in a WSL distro: its copy of the
// skill made a link the distro makes to the distro's copy (storeSkill).
// The copy is moved aside first, since the distro won't put a link in a
// folder's place, and put back when no link can be made. A copy that
// differs from the library's skill (edited, and its edit lost) is kept
// aside with the backups rather than lost.
func (l *Library) wslRelink(t *Target) func(id, p, name string) (bool, error) {
	return func(id, p, name string) (bool, error) {
		current, err := copyIsFresh(p, l.libHash(name), l.untouched[p])
		if err != nil {
			return false, err
		}
		old := oldPath(p, name)
		if err := os.Rename(p, old); err != nil {
			return false, err
		}
		if err := agent.WSLSymlink(t.Agent, filepath.Join(t.Store, name), p); err != nil {
			if back := os.Rename(old, p); back != nil {
				return false, errors.Join(err, back)
			}
			return false, err
		}
		if current {
			// best effort: what's left is tried again on the next sync
			os.RemoveAll(old)
		} else if _, err := setAside(id, old); err != nil {
			return true, err
		}
		return true, nil
	}
}

// sweepWSLStores takes out of each distro's store the copies no agent
// there has a link to, or is given, any more. A copy edited since magpie
// made it is kept aside with the backups. A store with an agent that
// couldn't be reached is left as it is: that agent may still link to any.
func (l *Library) sweepWSLStores(targets []*Target, res *Result) {
	type store struct {
		path   string
		owner  string
		keep   map[string]bool
		frozen bool
	}
	stores := map[string]*store{}
	for _, t := range targets {
		if t.Store == "" {
			continue
		}
		key := strings.ToLower(t.Store)
		s := stores[key]
		if s == nil {
			s = &store{path: t.Store, owner: t.Agent.ID, keep: map[string]bool{}}
			stores[key] = s
		}
		if t.Unreached != nil {
			s.frozen = true
		}
		for _, sk := range l.Skills {
			if sk.gives(t.Agent.ID) {
				s.keep[sk.Name] = true
			}
		}
		for _, name := range l.applied(t.Agent.ID).Skills {
			s.keep[name] = true
		}
	}
	for _, s := range stores {
		if s.frozen {
			continue
		}
		es, err := os.ReadDir(s.path)
		if err != nil {
			continue
		}
		for _, e := range es {
			name := e.Name()
			p := filepath.Join(s.path, name)
			if strings.HasPrefix(name, ".") || s.keep[name] || !ours(p, name) || linked(p) {
				continue
			}
			dropOld(p, name)
			h, _ := markedHash(p)
			if h != "" && hashDir(p) == h {
				err = os.RemoveAll(p)
			} else {
				_, err = setAside(s.owner, p)
			}
			if err != nil {
				res.fail(s.owner, "skill:"+name, err)
			}
		}
	}
}
