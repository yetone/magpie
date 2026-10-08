package library

// How an agent is given its skills (#896): a link to the library's folder,
// which an update reaches at once, or a copy of its own, which a tool that
// doesn't follow links (or a sync, a backup, an archive) takes as a folder
// like any other. The library has one way for every agent, and an agent
// can have its own. A copy carries magpie's mark (marker), so it is known
// for magpie's own and is all a change of way or a Remove ever takes away;
// a folder of the user's by that name is never touched. A copy is made
// again on each sync once the library's skill differs from it: the page
// says which copies differ until then (SkillView.Behind), with a button
// that syncs. A copy edited since it was made goes to the library first,
// when its edit is the newest (takeEdits).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// The ways an agent can be given its skills.
const (
	HowLink = "link"
	HowCopy = "copy"
)

// copies is whether the agent of t gets copies of its skills: one that
// can only take copies (in a WSL distro), else its own way, else the
// library's. Agents reading the very same folder (sharers) get copies when
// any of them is to, so that they don't undo each other's.
func (l *Library) copies(t *Target, sharers []string) bool {
	if t.Copy {
		return true
	}
	return slices.ContainsFunc(append([]string{t.Agent.ID}, sharers...), func(id string) bool { return l.howOf(id) == HowCopy })
}

// howOf is the way the agent is given its skills: its own, else the
// library's.
func (l *Library) howOf(id string) string {
	if h := l.SkillHow[id]; h == HowLink || h == HowCopy {
		return h
	}
	if l.CopySkills {
		return HowCopy
	}
	return HowLink
}

// SetSkillHow sets the way the library gives its skills to every agent
// (agent "") or to one ("" for how the library does it); the agents' links
// and copies are made again the new way.
func SetSkillHow(agent, how string) (*Result, error) {
	if how != "" && how != HowLink && how != HowCopy {
		return nil, fmt.Errorf("skills are given as %s or %s, not %q", HowLink, HowCopy, how)
	}
	return change(func(l *Library) error {
		if agent == "" {
			if how == "" {
				return fmt.Errorf("the library gives skills as %s or %s", HowLink, HowCopy)
			}
			l.CopySkills = how == HowCopy
			return nil
		}
		if err := checkAgent(agent); err != nil {
			return err
		}
		if how == "" {
			delete(l.SkillHow, agent)
			if len(l.SkillHow) == 0 {
				l.SkillHow = nil
			}
			return nil
		}
		if l.SkillHow == nil {
			l.SkillHow = map[string]string{}
		}
		l.SkillHow[agent] = how
		return nil
	})
}

// relink puts a link to the library's skill at p in place of magpie's copy
// there, not ok when no link can be made (Windows without the right to),
// which leaves the copy as it is. A copy that differs from the library's
// skill (edited in the agent) is kept aside rather than lost.
func relink(agent, p, name string) (bool, error) {
	next := filepath.Join(filepath.Dir(p), "."+name+".magpie-link")
	os.Remove(next)
	if err := dirLink(skillDir(name), next); err != nil {
		return false, nil
	}
	var err error
	if fresh(p, name) {
		old := oldPath(p, name)
		if err = os.Rename(p, old); err == nil {
			// best effort: what's left is tried again on the next sync
			defer os.RemoveAll(old)
		}
	} else {
		_, err = setAside(agent, p)
	}
	if err != nil {
		os.Remove(next)
		return false, err
	}
	if err := os.Rename(next, p); err != nil {
		os.Remove(next)
		return false, errors.Join(err, link(p, name))
	}
	return true, nil
}

// behind are, for each skill, the agents given it as a copy (of their own
// choosing, on this machine) whose copy differs from the library's skill.
func (l *Library) behind(targets []*Target) map[string][]string {
	out := map[string][]string{}
	for _, t := range targets {
		if t.Skills == "" || t.Desktop != nil || t.Agent.WSL != "" || l.howOf(t.Agent.ID) != HowCopy {
			continue
		}
		for _, s := range l.Skills {
			if !slices.Contains(s.Agents, t.Agent.ID) {
				continue
			}
			p := filepath.Join(t.Skills, s.Name)
			if !ours(p, s.Name) || linked(p) {
				continue
			}
			if h := l.libHash(s.Name); h != "" && !copyIsFresh(p, h) && !slices.Contains(out[s.Name], t.Agent.ID) {
				out[s.Name] = append(out[s.Name], t.Agent.ID)
			}
		}
	}
	return out
}
