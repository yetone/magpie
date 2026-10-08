package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	slash "path" // a repository's paths, always with /
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ---- skills a repository has added since --------------------------------

// NewSkill is a skill a check for updates found in a GitHub repository the
// library has skills from, beside them, that the library doesn't have and
// that the page hasn't offered before: one the repository added since its
// skills were installed. The page offers to add it, or to set it aside.
type NewSkill struct {
	// ID is the skill's folder on GitHub, what the page adds it from and
	// sets it aside by
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Repo        string `json:"repo"`
	Ref         string `json:"ref,omitempty"`
	Path        string `json:"path"`
	// From is the repository's folder the check found it in, what Add
	// installs it from
	From string `json:"from"`
	// Agents are those that have the repository's other skills: the ones
	// an Add gives it to
	Agents []string `json:"agents"`
}

// The last check's finds, by repository and ref, stay for the page until
// the next check of that repository.
var newFound = struct {
	sync.Mutex
	m map[string][]NewSkill
}{m: map[string][]NewSkill{}}

// repoKey is a repository at a ref, as SeenSkills and newFound keep it.
func repoKey(repo, ref string) string { return repo + "@" + ref }

// seenAt records that the page offered the skills at those paths of a
// repository: picked or not at install, or set aside after a check. A
// check doesn't offer them as new again.
func (l *Library) seenAt(repo, ref string, paths ...string) {
	if l.SeenSkills == nil {
		l.SeenSkills = map[string][]string{}
	}
	k := repoKey(repo, ref)
	for _, p := range paths {
		if !slices.Contains(l.SeenSkills[k], p) {
			l.SeenSkills[k] = append(l.SeenSkills[k], p)
		}
	}
	sort.Strings(l.SeenSkills[k])
}

// fromRepo are the library's skills installed from a repository at a ref.
func (l *Library) fromRepo(repo, ref string) []*Skill {
	var out []*Skill
	for _, s := range l.Skills {
		if s.Source != nil && s.Source.Kind == "github" && s.Source.Repo == repo && s.Source.Ref == ref {
			out = append(out, s)
		}
	}
	return out
}

// newBase is the folder of a repository a check looks for new skills in:
// the deepest one that holds every installed skill's folder. Skills
// installed from skills/engineering are looked for beside them, not in
// the rest of the repository; ones from several of skills/'s folders are
// looked for in all of skills/.
func newBase(skills []*Skill) string {
	base, first := "", true
	for _, s := range skills {
		dir := slash.Dir(s.Source.Path)
		if s.Source.Path == "" || dir == "." {
			return "" // a skill at the top, or right under it
		}
		if first {
			base, first = dir, false
			continue
		}
		for base != "" && dir != base && !strings.HasPrefix(dir, base+"/") {
			if base = slash.Dir(base); base == "." {
				base = ""
			}
		}
	}
	return base
}

// gitTree is a repository's files as GitHub's API lists them.
type gitTree struct {
	Truncated bool `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
}

// skillDirsIn are the folders with a SKILL.md under base in a repository's
// tree, as candidates would find them: a few folders deep, none hidden or
// in node_modules, and none inside another skill.
func skillDirsIn(t *gitTree, base string) []string {
	var dirs []string
	for _, e := range t.Tree {
		if e.Type != "blob" || slash.Base(e.Path) != "SKILL.md" {
			continue
		}
		dir := slash.Dir(e.Path)
		if dir == "." {
			dir = ""
		}
		rel := dir
		if base != "" {
			var ok bool
			if rel, ok = strings.CutPrefix(dir, base+"/"); !ok && dir != base {
				continue
			} else if dir == base {
				rel = ""
			}
		}
		parts := []string{}
		if rel != "" {
			parts = strings.Split(rel, "/")
		}
		if len(parts) > 4 || slices.ContainsFunc(parts, func(p string) bool { return strings.HasPrefix(p, ".") || p == "node_modules" }) {
			continue
		}
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	// one inside another skill isn't a skill of its own
	out := dirs[:0]
	for _, d := range dirs {
		if !slices.ContainsFunc(out, func(o string) bool { return o == "" && d != "" || strings.HasPrefix(d, o+"/") }) {
			out = append(out, d)
		}
	}
	return out
}

// repoTree is a repository's files at a ref, from GitHub's API.
func repoTree(repo, ref string) (*gitTree, error) {
	if ref == "" {
		ref = "HEAD"
	}
	body, code, _, err := apiGet(githubAPI + "/repos/" + repo + "/git/trees/" + url.PathEscape(ref) + "?recursive=1")
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("GitHub answered %d listing %s's files", code, repo)
	}
	var t gitTree
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("GitHub's list of %s's files wasn't understood: %w", repo, err)
	}
	return &t, nil
}

// findNew looks in one repository at one ref for skills beside the
// library's from there that it doesn't have and hasn't offered: GitHub's
// list of the repository's files first, and only when that has one, the
// repository itself, for their names and descriptions. ok is false when
// it couldn't be told; what an earlier check found then stays.
func findNew(l *Library, repo, ref string, f *fetcher, limited *atomic.Pointer[errLimited]) (found []NewSkill, ok bool) {
	if limited.Load() != nil {
		return nil, false
	}
	skills := l.fromRepo(repo, ref)
	if len(skills) == 0 {
		return nil, false
	}
	base := newBase(skills)
	known := map[string]bool{}
	for _, p := range l.SeenSkills[repoKey(repo, ref)] {
		known[p] = true
	}
	// a skill from this repository at any ref is the library's already
	for _, s := range l.Skills {
		if s.Source != nil && s.Source.Kind == "github" && s.Source.Repo == repo {
			known[s.Source.Path] = true
		}
	}
	tree, err := repoTree(repo, ref)
	if err != nil {
		var e errLimited
		if errors.As(err, &e) {
			limited.Store(&e)
		}
		return nil, false
	}
	if !tree.Truncated && !slices.ContainsFunc(skillDirsIn(tree, base), func(d string) bool { return !known[d] }) {
		return []NewSkill{}, true
	}
	src := Source{Kind: "github", Repo: repo, Ref: ref}
	tmp, err := f.fetch(src)
	if err != nil {
		return nil, false
	}
	// none when the repository's skills are on no agent: [], which the
	// page reads as a list (#1217)
	agents := []string{}
	for _, s := range skills {
		for _, a := range s.Agents {
			if !slices.Contains(agents, a) {
				agents = append(agents, a)
			}
		}
	}
	sort.Strings(agents)
	found = []NewSkill{}
	from := (&Source{Kind: "github", Repo: repo, Ref: ref, Path: base}).String()
	for _, c := range candidates(tmp, base) {
		if known[c.Path] || l.skill(c.Name) != nil || checkName("skill", c.Name) != nil {
			continue
		}
		id := (&Source{Kind: "github", Repo: repo, Ref: ref, Path: c.Path}).String()
		found = append(found, NewSkill{ID: id, Name: c.Name, Description: c.Description, Repo: repo, Ref: ref, Path: c.Path, From: from, Agents: slices.Clone(agents)})
	}
	return found, true
}

// newSkills is what the last checks found new, less what the library has
// taken in or set aside since.
func newSkills(l *Library) []NewSkill {
	newFound.Lock()
	defer newFound.Unlock()
	out := []NewSkill{}
	for k, list := range newFound.m {
		for _, n := range list {
			if repoKey(n.Repo, n.Ref) != k || len(l.fromRepo(n.Repo, n.Ref)) == 0 || l.skill(n.Name) != nil ||
				slices.Contains(l.SeenSkills[k], n.Path) {
				continue
			}
			if slices.ContainsFunc(l.Skills, func(s *Skill) bool {
				return s.Source != nil && s.Source.Kind == "github" && s.Source.Repo == n.Repo && s.Source.Path == n.Path
			}) {
				continue
			}
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// foundNew are the new skills the last checks found, of those IDs.
func foundNew(ids []string) []NewSkill {
	newFound.Lock()
	defer newFound.Unlock()
	var hit []NewSkill
	for _, list := range newFound.m {
		for _, n := range list {
			if slices.Contains(ids, n.ID) {
				hit = append(hit, n)
			}
		}
	}
	return hit
}

// AddNewSkills adds the new skills a check found, by their IDs, each to
// the agents that have its repository's other skills. The repository is
// fetched once for those of it.
func AddNewSkills(ids []string) (*Result, error) {
	hit := foundNew(ids)
	if len(hit) == 0 {
		return nil, fmt.Errorf("those skills aren't new any more; check for updates again")
	}
	type group struct {
		p      *Probe
		paths  []string
		agents []string
	}
	groups := map[string]*group{}
	var order []string
	for _, n := range hit {
		g := groups[n.From]
		if g == nil {
			p, err := cachedProbe(n.From)
			if err != nil {
				return nil, err
			}
			g = &group{p: p, agents: n.Agents}
			groups[n.From] = g
			order = append(order, n.From)
		}
		g.paths = append(g.paths, n.Path)
	}
	return installChange(func(l *Library, in *installed) error {
		for _, from := range order {
			g := groups[from]
			if err := installFrom(l, g.p, g.paths, g.agents, false, in); err != nil {
				return err
			}
		}
		return nil
	})
}

// IgnoreNewSkills sets aside the new skills a check found, by their IDs:
// a later check doesn't offer them again.
func IgnoreNewSkills(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("no skills to set aside")
	}
	hit := foundNew(ids)
	if len(hit) == 0 {
		return fmt.Errorf("those skills aren't new any more; check for updates again")
	}
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return err
	}
	for _, n := range hit {
		l.seenAt(n.Repo, n.Ref, n.Path)
	}
	return l.save()
}
