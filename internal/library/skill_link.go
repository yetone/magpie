package library

import (
	"slices"
	"strings"
)

// A skill the library has from a folder of the user's, or took in from an
// agent's folder, has nothing to be updated from. Many of them came from
// GitHub to begin with: downloaded from there and brought into the
// library (biubiu8802 on X, wishing it could follow GitHub). Installing
// that skill from GitHub links the two, as an update of one CC Switch
// installed does (ccswitch.go): the library's skill by that name takes
// GitHub's files and GitHub as its source, so a check and an update cover
// it from then on, and the agents' links to it go on pointing at it. Its
// own files, when they aren't GitHub's to the file, go to the backups, as
// an update that replaces an edited skill keeps it (UpdateSkill); a folder
// of the user's it was linked to is left as it is, and only the library's
// link to it goes.

// followsHere is whether installing from src the skill fetched at from
// links the library's skill s, which has its name, to it. It must be that
// skill: traced to that repository, or traced nowhere and saying it does
// the same thing (its SKILL.md's description, as an older download of it
// does). One traced to another repository, or one of the user's own that
// describes something else, is another skill by that name, and goes in
// beside it (skill_beside.go, TestInstallSetPutsAnotherByTheSameNameBeside).
func followsHere(s *Skill, src Source, from string) bool {
	if src.Kind != "github" || s.Source.remote() || s.Called != "" {
		return false
	}
	// where it is traced to: CC Switch's record, the skills CLI's lock,
	// the git checkout its folder is in, or the repository it was grouped
	// with (skill_from.go)
	if r := (&tracer{}).repoOf(s); r != "" {
		return strings.EqualFold(r, src.Repo)
	}
	here, ok := readMeta(skillDir(s.Name))
	there, ok2 := readMeta(from)
	return ok && ok2 && here.Description != "" && here.Description == there.Description
}

// linkSkill puts the skill fetched at from, at path of src, in the place
// of the library's skill s, which then updates from src. The agents named
// get it too. kept is whether s's own files weren't the fetched ones and
// went to the backups.
func (l *Library) linkSkill(s *Skill, src Source, path, from string, agents []string) (kept bool, err error) {
	src.Path = path
	dir := skillDir(s.Name)
	if linked(dir) || !sameTree(dir, from) {
		kept = !linked(dir)
		up := &skillUpdate{name: s.Name, src: &src, adopt: true, from: from}
		if err := up.apply(l, Targets()); err != nil {
			return false, err
		}
	} else {
		// its files are the source's already: only where it updates from
		// is new
		s.Hash, s.Own, s.Commit = hashDir(dir), ownHash(dir), ""
		forgetCheck(s.Name)
	}
	s.Source, s.From, s.Gone = &src, "", ""
	for _, a := range agents {
		if !slices.Contains(s.Agents, a) {
			s.Agents = append(s.Agents, a)
		}
	}
	return kept, nil
}
