package library

// Where the library's skills go (usrname1833 on Discord): to the agents
// each is ticked for, in each agent's own folder (the default), or also
// into ~/.agents/skills, the folder many agents and tools read besides
// their own (readsShared, Target.SharedToo), so every one of them finds
// every skill, ticked or not, as it finds one the user keeps there.
//
// The skill's files stay in the library's folder and ~/.agents/skills
// gets magpie's link to it, as it does for an agent whose folder it is
// (Goose, Kimi Code, Cindy): an update, a check, an edit, a sync and
// turning one off on this computer work as for any skill, and turning
// the setting off takes out magpie's links there and nothing else. An
// entry of the user's own by a skill's name is never written over.
//
// It is this computer's setting: Collect never carries it.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// The places SetSkillsWhere takes.
const (
	SkillsInAgents = "agents" // each agent's own folder, as ticked
	SkillsInShared = "shared" // ~/.agents/skills too, every skill
)

// SetSkillsWhere sets where the library's skills go: SkillsInAgents or
// SkillsInShared. Every skill is put in ~/.agents/skills, or taken out of
// it, at the sync it runs.
func SetSkillsWhere(where string) (*Result, error) {
	if where != SkillsInAgents && where != SkillsInShared {
		return nil, fmt.Errorf("skills go in %s or %s, not %q", SkillsInAgents, SkillsInShared, where)
	}
	return change(func(l *Library) error {
		l.SharedSkills = where == SkillsInShared
		return nil
	})
}

// keepsShared is whether the library puts its skill s in ~/.agents/skills:
// the setting is on, the skill is on here and its folder is there.
func (l *Library) keepsShared(s *Skill) bool {
	if !l.SharedSkills || s == nil || s.OffHere {
		return false
	}
	_, err := os.Stat(filepath.Join(skillDir(s.Name), "SKILL.md"))
	return err == nil
}

// inSharedHere is whether ~/.agents/skills has the library's skill s by
// the setting: magpie's link (or copy) there.
func (l *Library) inSharedHere(s *Skill) bool {
	return l.keepsShared(s) && ours(filepath.Join(sharedSkillsDir(), s.Name), s.Name)
}

// syncShared puts each skill the setting keeps there into ~/.agents/skills,
// before the agents that read it are synced (they then get no second
// link). One given to an agent whose folder that is (Kimi Code) is that
// agent's sync's, which puts it there its own way; sweepShared takes out
// what the setting no longer keeps.
func (l *Library) syncShared(all []*Target, res *Result) {
	if !l.SharedSkills {
		return
	}
	shared := realDir(sharedSkillsDir())
	var holders []*Target
	for _, o := range all {
		if o.Skills != "" && !o.Gone && realDir(o.Skills) == shared {
			holders = append(holders, o)
		}
	}
	for _, s := range l.Skills {
		if !l.keepsShared(s) || sharedEntry(s.Name) != "" || slices.ContainsFunc(holders, func(o *Target) bool { return s.gives(o.Agent.ID) }) {
			continue
		}
		p := filepath.Join(sharedSkillsDir(), s.Name)
		if l.blockedSkills[realDir(p)] {
			continue
		}
		if ours(p, s.Name) {
			if linked(p) {
				continue
			}
			// a copy, where no link could be made (Windows without the
			// right to): a link once one can be, else made again when the
			// library's skill has changed, an edited copy kept aside
			if ok, err := relink("agents", p, s.Name); err != nil || ok {
				if err != nil {
					res.fail("agents", "skill:"+s.Name, err)
				}
				continue
			}
			if h := l.libHash(s.Name); h != "" {
				fresh, err := copyIsFresh(p, h, false)
				if err == nil && !fresh {
					_, err = setAside("agents", p)
					if err == nil {
						err = link(p, s.Name)
					}
				}
				if err != nil {
					res.fail("agents", "skill:"+s.Name, err)
				}
			}
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			// the user's own by that name stays theirs, and is what the
			// agents reading the folder find; the agents ticked for the
			// library's still get it in their own folders (inSharedHere
			// is false for it). Said at no sync: every change would carry
			// it as a failure
			continue
		}
		if err := link(p, s.Name); err != nil {
			res.fail("agents", "skill:"+s.Name, err)
		}
	}
}
