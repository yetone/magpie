package library

// A skill folder copied into the library's own skills folder by hand
// (jwaitq on Discord) is the library's: the user put it where the
// library keeps its skills. #595 only showed it under "In your agents",
// to be brought in with a click, and the Skills page didn't list it.
// adoptLibraryFolders brings each in on the next read of the page, as
// that click would (importSkill): listed where it is, given to the agents
// already linking to it and to no other. Removing a skill takes its
// folder out of the library's (to the backups, or deleted), so one the
// user removed is never brought back.

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// adoptLibraryFolders lists in l each direct child of the library's skills
// folder with a SKILL.md that l doesn't list. Left out: a name starting
// with "." (magpie's own .name.next, .name.old and .name.magpie-old
// leftovers among them), a folder with no SKILL.md, a copy of magpie's
// (its mark) and the skill router's entry. A folder whose name is no
// skill name (one in another script) is renamed to the name its SKILL.md
// gives, made fit (nameFor), when that is free. It returns the skills it
// brought in, and whether any of them is given to an agent.
func (l *Library) adoptLibraryFolders() (adopted []string, given bool) {
	es, err := os.ReadDir(skillsDir())
	if err != nil {
		return nil, false
	}
	var names []string
	for _, e := range es {
		name := e.Name()
		p := filepath.Join(skillsDir(), name)
		if strings.HasPrefix(name, ".") || l.skill(name) != nil || routerOurs(p) {
			continue
		}
		m, ok := readMeta(p)
		if !ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(p, marker)); err == nil {
			continue
		}
		if !nameRe.MatchString(name) {
			n := nameFor(name, m)
			if checkName("skill", n) != nil || l.skill(n) != nil {
				continue
			}
			if _, err := os.Lstat(skillDir(n)); err == nil {
				continue
			}
			if err := os.Rename(p, skillDir(n)); err != nil {
				log.Printf("library: naming the skill folder %s %s: %v", name, n, err)
				continue
			}
			name = n
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, false
	}
	found := foundSkills(l)
	for _, name := range names {
		if err := importSkill(l, found, name); err != nil {
			log.Printf("library: bringing in the skill folder %s: %v", name, err)
			continue
		}
		adopted = append(adopted, name)
		if s := l.skill(name); s != nil && len(s.Agents) > 0 {
			given = true
		}
	}
	return adopted, given
}
