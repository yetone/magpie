package library

// A skill linked in from a folder the user keeps, whose source is gone
// (@Eternalfate__ on X, #7226): "Bring in all" links a skill of the skills
// CLI's ~/.agents/skills into the library instead of moving it, and
// `npx skills remove <name>` then deletes that folder. The library's link
// leads nowhere, and every agent ticked for it was given a link to that
// link. markDangling marks such a skill Gone (the page offers to take it
// out, RemoveGoneSkills, which only removes the link) and Dangling, which
// `gives` reads, so no sync writes an agent a link to it. Nothing the user
// owns is touched: only magpie's own links are taken away, and a folder
// that can't be read is unknown, not gone.

import (
	"os"
	"path/filepath"
	"time"
)

// sourceGone is whether the library's skill by that name is a link to a
// folder that isn't there any more, or has no SKILL.md left. A skill that
// is a folder of the library's own, or a link that can't be looked at, is
// not: unknown keeps the entry.
func sourceGone(name string) bool {
	p := skillDir(name)
	if !linked(p) {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return absent(err)
	}
	if !fi.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(p, "SKILL.md"))
	return err != nil && absent(err)
}

// markDangling sets Dangling for each skill linked in whose source is
// gone, marks it Gone, and clears that when it is back. It says whether a
// skill's Gone changed (to be saved). A skill from a repository keeps its
// Gone for skill_gone.go's own check.
func (l *Library) markDangling() bool {
	changed := false
	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range l.Skills {
		s.Dangling = false
		if s.Source.remote() {
			continue
		}
		switch {
		case sourceGone(s.Name):
			s.Dangling = true
			if s.Gone == "" {
				s.Gone, changed = now, true
			}
		case s.Gone != "" && linked(skillDir(s.Name)):
			if _, err := os.Stat(filepath.Join(skillDir(s.Name), "SKILL.md")); err == nil {
				s.Gone, changed = "", true
			}
		}
	}
	return changed
}
