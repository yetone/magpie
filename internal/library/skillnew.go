package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	slash "path" // a repository's paths, always with /
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ---- skills a repository has added since --------------------------------

// NewSkill is a skill a check for updates found where the library has
// skills from, beside them, that the library doesn't have and that the
// page hasn't offered before: one a GitHub or git repository, or a folder
// on this machine, added since its skills were installed (refeccd on
// Discord). The page offers to add it, or to set it aside.
type NewSkill struct {
	// ID is the skill's folder (on GitHub, in the git repository, or on
	// this machine), what the page adds it from and sets it aside by
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Kind is where it is: github, git or folder
	Kind string `json:"kind"`
	// Repo is the repository (owner/repo, or the address git fetches),
	// or the folder the user added
	Repo string `json:"repo"`
	Ref  string `json:"ref,omitempty"`
	Path string `json:"path"`
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
// A git repository's address never reads as GitHub's owner/repo.
func repoKey(repo, ref string) string { return repo + "@" + ref }

// folderKey is a folder the user added skills from, as SeenSkills and
// newFound keep it: its key in SeenSkills is how the library knows which
// folder its linked skills were added from, to look in it again.
func folderKey(root string) string { return foldersKey + root }

const foldersKey = "folder:"

// placeKey is where a found skill is, as SeenSkills and newFound keep it.
func (n *NewSkill) placeKey() string {
	if n.Kind == "folder" {
		return folderKey(n.Repo)
	}
	return repoKey(n.Repo, n.Ref)
}

// seenAt records that the page offered the skills at those paths of a
// repository or folder (by repoKey or folderKey): picked or not at
// install, or set aside after a check. A check doesn't offer them as new
// again.
func (l *Library) seenAt(k string, paths ...string) {
	if l.SeenSkills == nil {
		l.SeenSkills = map[string][]string{}
	}
	for _, p := range paths {
		if !slices.Contains(l.SeenSkills[k], p) {
			l.SeenSkills[k] = append(l.SeenSkills[k], p)
		}
	}
	sort.Strings(l.SeenSkills[k])
}

// fromRepo are the library's skills installed from a GitHub repository
// at a ref.
func (l *Library) fromRepo(repo, ref string) []*Skill { return l.fromPlace("github", repo, ref) }

// fromPlace are the library's skills installed from a GitHub or git
// repository at a ref, or linked from a folder under the one the user
// added (repo, for kind folder).
func (l *Library) fromPlace(kind, repo, ref string) []*Skill {
	var out []*Skill
	for _, s := range l.Skills {
		if s.Source == nil || s.Source.Kind != kind {
			continue
		}
		switch kind {
		case "github":
			if s.Source.Repo == repo && s.Source.Ref == ref {
				out = append(out, s)
			}
		case "git":
			if sameGitRepo(s.Source.Repo, repo) && s.Source.Ref == ref {
				out = append(out, s)
			}
		case "folder":
			if _, in := relUnder(repo, s.Source.Dir); in {
				out = append(out, s)
			}
		}
	}
	return out
}

// relUnder is dir's path from root, with /, when dir is root or in it:
// as the paths are, or with links followed (/tmp is /private/tmp).
func relUnder(root, dir string) (string, bool) {
	if root == "" || dir == "" {
		return "", false
	}
	for _, p := range [][2]string{{filepath.Clean(root), filepath.Clean(dir)}, {realDir(root), realDir(dir)}} {
		rel, err := filepath.Rel(p[0], p[1])
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		if rel == "." {
			rel = ""
		}
		return filepath.ToSlash(rel), true
	}
	return "", false
}

// agentsOf are the agents that have any of those skills, sorted: none is
// [], which the page reads as a list (#1217).
func agentsOf(skills []*Skill) []string {
	agents := []string{}
	for _, s := range skills {
		for _, a := range s.Agents {
			if !slices.Contains(agents, a) {
				agents = append(agents, a)
			}
		}
	}
	sort.Strings(agents)
	return agents
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

// maxTree is the most of a repository's file list magpie reads; a list can
// be tens of MB for a big app (stablyai/orca's is ~10 MB, and GitHub
// answers one whole under its own 100,000-entry cap).
const maxTree = 64 << 20

// gitTree is a repository's files as GitHub's API lists them.
type gitTree struct {
	Truncated bool       `json:"truncated"`
	Tree      []gitEntry `json:"tree"`
}

// gitEntry is one file or folder in a gitTree.
type gitEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// skillDirsIn are the folders with a SKILL.md under base in a repository's
// tree, as candidates would find them: a few folders deep, none in
// node_modules, a hidden folder only through its skills folder, none
// inside another skill, and each skill once (unhiddenFirst).
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
		if len(parts) > 4 || slices.Contains(parts, "node_modules") || !hiddenThroughSkills(parts) {
			continue
		}
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	// one inside another skill isn't a skill of its own, beside the skills
	// of a collection: a folder that holds a SKILL.md and several skills
	// under it lists them beside it (#4572), and a lone SKILL.md inside
	// another skill still isn't one of its own
	collection := func(o string) bool {
		n := 0
		for _, d := range dirs {
			below := ""
			if o == "" {
				below = d // the repository itself is the skill
			} else if rest, in := strings.CutPrefix(d, o+"/"); in {
				below = rest
			}
			if below == "" || strings.Contains(below, "/") {
				continue
			}
			if n++; n > 1 {
				return true
			}
		}
		return false
	}
	out := dirs[:0]
	for _, d := range dirs {
		if !slices.ContainsFunc(out, func(o string) bool {
			return (o == "" && d != "" || strings.HasPrefix(d, o+"/")) && !collection(o)
		}) {
			out = append(out, d)
		}
	}
	return unhiddenFirst(out, func(d string) string { return d }, func(a, b string) bool { return slash.Base(a) == slash.Base(b) })
}

// hiddenThroughSkills is whether a path candidates walks: every hidden
// folder in it is an agent's own, entered only for its skills folder.
func hiddenThroughSkills(parts []string) bool {
	for i, p := range parts {
		if strings.HasPrefix(p, ".") && (i+1 == len(parts) || parts[i+1] != "skills") {
			return false
		}
	}
	return true
}

// repoTree is a repository's files at a ref, from GitHub's API.
func repoTree(repo, ref string) (*gitTree, error) {
	return listTree(repo, ref, "")
}

// listTree is GitHub's list of the files at a ref under path (the whole
// repository's for ""), with paths from that folder. A list over maxTree,
// or one cut off, is an error that says so, never a list read in part.
func listTree(repo, ref, path string) (*gitTree, error) {
	if ref == "" {
		ref = "HEAD"
	}
	ish := ref
	if path != "" {
		ish += ":" + path // git's ref:path, a folder's own tree
	}
	body, code, _, err := apiGet(githubAPI+"/repos/"+repo+"/git/trees/"+url.PathEscape(ish)+"?recursive=1", maxTree)
	if errors.Is(err, errOverCap) {
		return nil, fmt.Errorf("GitHub's list of %s's files is over %s", repo, mb(maxTree))
	}
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

// treeUnder is a repository's files at a ref under path, with their paths
// from its root. With a path it asks for that folder's list alone: one
// skill's folder is a few KB where the whole repository's list is over
// 11 MB for sickn33/agentic-awesome-skills (#2794). When that can't be
// had (no such folder, a ref git's ref:path doesn't read, a list cut
// off) the whole repository's is read, which says for sure what is there.
// A rate limit is said at once.
func treeUnder(repo, ref, path string) (*gitTree, error) {
	if path != "" {
		t, err := listTree(repo, ref, path)
		var e errLimited
		if errors.As(err, &e) {
			return nil, err
		}
		if err == nil && !t.Truncated {
			for i := range t.Tree {
				t.Tree[i].Path = path + "/" + t.Tree[i].Path
			}
			t.Tree = append([]gitEntry{{Path: path, Type: "tree", Mode: "040000"}}, t.Tree...)
			return t, nil
		}
	}
	return repoTree(repo, ref)
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
	tree, err := treeUnder(repo, ref, base)
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
	// only base's folder: candidates looks nowhere else, and a
	// repository too big to download whole is then fetched only there
	src := Source{Kind: "github", Repo: repo, Ref: ref, Path: base}
	tmp, err := f.fetch(src)
	if err != nil {
		return nil, false
	}
	agents := agentsOf(skills)
	found = []NewSkill{}
	from := (&Source{Kind: "github", Repo: repo, Ref: ref, Path: base}).String()
	for _, c := range candidates(tmp, base, srcFolder(Source{Kind: "github", Repo: repo})) {
		// another skill by its name, from elsewhere, doesn't make it the
		// library's: it would go in beside that one (skill_beside.go)
		if known[c.Path] || checkName("skill", c.Name) != nil ||
			l.hadSkill(Source{Kind: "github", Repo: repo, Ref: ref}, c, filepath.Join(tmp, filepath.FromSlash(c.Path))) != nil {
			continue
		}
		id := (&Source{Kind: "github", Repo: repo, Ref: ref, Path: c.Path}).String()
		found = append(found, NewSkill{ID: id, Name: c.Name, Description: c.Description, Kind: "github", Repo: repo, Ref: ref, Path: c.Path, From: from, Agents: slices.Clone(agents)})
	}
	return found, true
}

// findNewGit is findNew for a repository on another git host, which has
// no API to list its files: the repository fetched with the user's git
// (the fetch a check of a changed skill makes, shared through f) is read
// for skills beside the library's from there.
func findNewGit(l *Library, repo, ref string, f *fetcher) (found []NewSkill, ok bool) {
	skills := l.fromPlace("git", repo, ref)
	if len(skills) == 0 {
		return nil, false
	}
	base := newBase(skills)
	known := map[string]bool{}
	for _, p := range l.SeenSkills[repoKey(repo, ref)] {
		known[p] = true
	}
	for _, s := range l.Skills {
		if s.Source != nil && s.Source.Kind == "git" && sameGitRepo(s.Source.Repo, repo) {
			known[s.Source.Path] = true
		}
	}
	src := Source{Kind: "git", Repo: repo, Ref: ref}
	tmp, err := f.fetch(src)
	if err != nil {
		return nil, false
	}
	agents := agentsOf(skills)
	found = []NewSkill{}
	// the address as the library has it, .git and all: what an Add
	// installs from is then the same repository, by the same key
	ish := ref
	if ish == "" {
		ish = "HEAD"
	}
	from := repo
	if ref != "" || base != "" {
		from = strings.TrimSuffix(repo+"/-/tree/"+ish+"/"+base, "/")
	}
	for _, c := range candidates(tmp, base, srcFolder(src)) {
		if known[c.Path] || checkName("skill", c.Name) != nil ||
			l.hadSkill(src, c, filepath.Join(tmp, filepath.FromSlash(c.Path))) != nil {
			continue
		}
		id := (&Source{Kind: "git", Repo: repo, Ref: ref, Path: c.Path}).String()
		found = append(found, NewSkill{ID: id, Name: c.Name, Description: c.Description, Kind: "git", Repo: repo, Ref: ref, Path: c.Path, From: from, Agents: slices.Clone(agents)})
	}
	return found, true
}

// findNewFolder looks in a folder the user added skills from for skills
// that the library doesn't link and the page hasn't offered: ones put
// there since (a git pull in a checkout, a skill written by hand). It is
// read as Add from folder reads it. ok is false when the folder can't be
// read; what an earlier check found then stays.
func findNewFolder(l *Library, root string) (found []NewSkill, ok bool) {
	skills := l.fromPlace("folder", root, "")
	if len(skills) == 0 {
		return nil, false
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, false
	}
	known := map[string]bool{}
	for _, p := range l.SeenSkills[folderKey(root)] {
		known[p] = true
	}
	for _, s := range l.Skills {
		if s.Source != nil && s.Source.Kind == "folder" {
			if rel, in := relUnder(root, s.Source.Dir); in {
				known[rel] = true
			}
		}
	}
	agents := agentsOf(skills)
	found = []NewSkill{}
	src := Source{Kind: "folder", Dir: root}
	for _, c := range candidates(root, "", filepath.Base(root)) {
		from := filepath.Join(root, filepath.FromSlash(c.Path))
		// a folder by a name the library has is never added beside it
		// (installFrom), and one in the library's own folder is had
		if known[c.Path] || checkName("skill", c.Name) != nil || l.skill(c.Name) != nil ||
			l.hadSkill(src, c, from) != nil {
			continue
		}
		if _, in := relUnder(skillsDir(), from); in {
			continue
		}
		found = append(found, NewSkill{ID: from, Name: c.Name, Description: c.Description, Kind: "folder", Repo: root, Path: c.Path, From: root, Agents: slices.Clone(agents)})
	}
	return found, true
}

// folderRoots are the folders the user added the library's linked skills
// from, as SeenSkills keeps them.
func (l *Library) folderRoots() []string {
	var out []string
	for k := range l.SeenSkills {
		if root, ok := strings.CutPrefix(k, foldersKey); ok && root != "" && len(l.fromPlace("folder", root, "")) > 0 {
			out = append(out, root)
		}
	}
	sort.Strings(out)
	return out
}

// addedFromFolders is whether the library has linked skills and knows a
// folder it added skills from: a check then looks in it. It is on every
// read of the page, so it reads no folder.
func (l *Library) addedFromFolders() bool {
	if !slices.ContainsFunc(l.Skills, func(s *Skill) bool { return s.Source != nil && s.Source.Kind == "folder" }) {
		return false
	}
	for k := range l.SeenSkills {
		if strings.HasPrefix(k, foldersKey) {
			return true
		}
	}
	return false
}

// lookInFolders is the part of a check that reads the folders the user
// added skills from, for skills put there since.
func lookInFolders(l *Library) {
	for _, root := range l.folderRoots() {
		if found, ok := findNewFolder(l, root); ok {
			newFound.Lock()
			newFound.m[folderKey(root)] = found
			newFound.Unlock()
		}
	}
}

// newSkills is what the last checks found new, less what the library has
// taken in or set aside since.
func newSkills(l *Library) []NewSkill {
	newFound.Lock()
	defer newFound.Unlock()
	out := []NewSkill{}
	for k, list := range newFound.m {
		for _, n := range list {
			if n.placeKey() != k || len(l.fromPlace(n.Kind, n.Repo, n.Ref)) == 0 ||
				slices.Contains(l.SeenSkills[k], n.Path) {
				continue
			}
			src := Source{Kind: n.Kind, Repo: n.Repo, Ref: n.Ref}
			if slices.ContainsFunc(l.Skills, func(s *Skill) bool { return fromSameSource(s, src, n.Path, n.ID) }) {
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
		l.seenAt(n.placeKey(), n.Path)
	}
	return l.save()
}
