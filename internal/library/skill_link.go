package library

import (
	"path/filepath"
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
//
// A git repository on another host is followed as GitHub is (Refeccd on
// X): a skill the Library groups with gitlab.example.com/team/skills, from
// the skills CLI's lock or a checkout of it, went in beside itself as
// name-team when that repository was added, every skill of it.
func followsHere(s *Skill, src Source, from string) bool {
	if !src.remote() || s.Source.remote() || s.Called != "" {
		return false
	}
	// where it is traced to: CC Switch's record, the skills CLI's lock,
	// the git checkout its folder is in, or the repository it was grouped
	// with (skill_from.go)
	if r := (&tracer{}).repoOf(s); r != "" {
		return tracedTo(r, src)
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
	// a git repository's skill updates from the commit it was fetched at
	commit := ""
	if src.Kind == "git" {
		commit = gitHeadOf(repoRoot(from, path))
	}
	if linked(dir) || !sameTree(dir, from) {
		kept = !linked(dir)
		up := &skillUpdate{name: s.Name, src: &src, adopt: true, from: from, commit: commit}
		if err := up.apply(l, Targets()); err != nil {
			return false, err
		}
	} else {
		// its files are the source's already: only where it updates from
		// is new
		s.Hash, s.Own, s.Commit = hashDir(dir), ownHash(dir), commit
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

// tracedTo is whether r, the repository a skill is traced to (owner/repo
// for GitHub, host/path for another host, as repoOf gives it), is src's.
func tracedTo(r string, src Source) bool {
	switch src.Kind {
	case "github":
		return strings.EqualFold(r, src.Repo)
	case "git":
		k := remoteRepo(src.Repo)
		return k != "" && strings.EqualFold(r, k) || strings.EqualFold(strings.TrimSuffix(r, ".git"), strings.TrimSuffix(src.Repo, ".git"))
	}
	return false
}

// sameRepo is whether a skill's source is the repository src is, at any
// folder of it. A GitHub repository added by its git address
// (git@github.com:owner/repo.git) is the one added as owner/repo: taken
// for another, each of its skills went in beside itself under the owner's
// name (Refeccd on X).
func sameRepo(a *Source, src Source) bool {
	if a == nil || !a.remote() || !src.remote() {
		return false
	}
	if a.Kind == "github" && src.Kind == "github" {
		return strings.EqualFold(a.Repo, src.Repo)
	}
	return sameGitRepo(a.Repo, src.Repo)
}

// repoRoot is the folder a repository was fetched to, from where the
// skill at path in it is: from less path's folders.
func repoRoot(from, path string) string {
	if path == "" {
		return from
	}
	for range strings.Split(path, "/") {
		from = filepath.Dir(from)
	}
	return from
}
