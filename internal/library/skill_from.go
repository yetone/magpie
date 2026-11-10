package library

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// Where a skill came from, for the page to group it with the others from
// the same repository (White Immortal on Discord: the skills of
// github.com/mattpocock/skills together, however each came in). One
// installed from GitHub says so itself; for the others it is traced:
// CC Switch's record, the skills CLI's lock (npx skills add, in
// ~/.agents/.skill-lock.json or $XDG_STATE_HOME/skills), the git checkout a linked folder is in,
// or the repository the user installed from since that has the very same
// skill (From). It only groups: such a skill is not updated from there,
// unless the library's own copy is the repository's to the file, which
// then is the repository's skill (noteFrom).

// repoOf is the repository a library skill came from: owner/repo on
// GitHub, host/path elsewhere, "" when it can't be told.
func (tr *tracer) repoOf(s *Skill) string {
	if s.Source != nil && s.Source.Kind == "github" {
		return s.Source.Repo
	}
	if s.Source != nil && s.Source.Kind == "git" {
		return remoteRepo(s.Source.Repo) // host/path, as a checkout of it is
	}
	if o, ok := ccSwitchOrigin(s); ok {
		return o.Repo
	}
	if s.From != "" && !tr.staleFrom(s) {
		return fromKey(s.From)
	}
	dir := skillDir(s.Name)
	if !linked(dir) {
		// the library's own copy: the skills CLI's, moved in, by its name,
		// when the CLI wrote it (lockRepo)
		if e := tr.lockRepo(s.Name, dir); e != "" && (s.Source == nil || within(realDir(s.Source.Dir), realDir(sharedSkillsDir()))) {
			return e
		}
		return ""
	}
	return tr.folder(realDir(dir))
}

// folder is the repository a skill's folder elsewhere came from: the
// skills CLI's lock for one in ~/.agents/skills, else its git checkout.
func (tr *tracer) folder(dir string) string {
	if within(dir, realDir(sharedSkillsDir())) {
		if e := tr.lockRepo(filepath.Base(dir), dir); e != "" {
			return e
		}
	}
	return gitRepoOf(dir)
}

// staleFrom is whether a skill linked from ~/.agents/skills was given
// its From by a lock entry left from another skill of its name: brought
// in before lockRepo judged the folder, it kept the entry's repository.
func (tr *tracer) staleFrom(s *Skill) bool {
	if s.Source == nil || s.Source.Kind != "folder" {
		return false
	}
	dir := realDir(s.Source.Dir)
	if !within(dir, realDir(sharedSkillsDir())) {
		return false
	}
	e, ok := tr.lock()[filepath.Base(dir)]
	return ok && (e.repo == s.From || e.repo == fromKey(s.From)) && tr.lockRepo(filepath.Base(dir), dir) == ""
}

// tracer reads what tracing needs once for a whole page.
type tracer struct {
	locked map[string]lockEntry
	read   bool
}

// lockEntry is a skill the skills CLI's lock names: the repository, and
// when the CLI last wrote the skill's folder (updatedAt, else
// installedAt; zero in a lock that says neither). github says the
// repository is on GitHub, as the lock told; one elsewhere is only
// grouped (repoOf), never updated from.
type lockEntry struct {
	repo   string
	at     time.Time
	github bool
}

// lockSlack is how long after its files a lock entry may be written: the
// CLI writes the folder, then the lock, a few seconds apart at most.
const lockSlack = time.Minute

// lockRepo is the repository the lock names for the skill called name
// whose folder is dir, when that folder is the one the CLI wrote. The
// lock is by name, and an entry outlives its skill: one deleted by hand,
// or from magpie, leaves it (AzheRise on X: a skill from a GitHub
// repository deleted, another of the same name made in ~/.dsh/skills was
// filed under that repository once brought in). A folder the CLI wrote
// has a file at least as old as the entry; one made since, all its files
// newer, is another skill that only has the name.
func (tr *tracer) lockRepo(name, dir string) string {
	e, ok := tr.lock()[name]
	if !ok || (!e.at.IsZero() && !madeBy(dir, e.at.Add(lockSlack))) {
		return ""
	}
	return e.repo
}

// lockOrigin is where on GitHub the skills CLI got a skill the library
// has (npx skills add, its lock), so that it can be updated from there
// like one installed here (#4645, Apri1qing: brought in or handed over,
// it kept only a From label, and update left it as it was). Only a lock
// that says when it wrote the skill's folder, and a folder that is the
// one it wrote (lockRepo), tells the skill apart from another of its
// name, and an update writes over it: a lock that says neither only
// groups. It is the origin of a skill with no source, or one whose folder
// is in ~/.agents/skills, as lockRepo's grouping has it: a folder of the
// user's own elsewhere, or a skill with a source of its own, is not the
// CLI's to be taken over by an update.
func lockOrigin(s *Skill) (Source, bool) {
	if s.Source != nil && !within(realDir(s.Source.Dir), realDir(sharedSkillsDir())) {
		return Source{}, false
	}
	tr := &tracer{}
	e, ok := tr.lock()[s.Name]
	if !ok || !e.github || e.at.IsZero() || !madeBy(realDir(skillDir(s.Name)), e.at.Add(lockSlack)) {
		return Source{}, false
	}
	return Source{Kind: "github", Repo: e.repo}, true
}

// madeBy is whether a file in the skill's folder dir was last written by
// at: a skill the lock's install wrote, edited since or not, keeps the
// files it didn't edit as they were written. A folder that can't be read
// is not.
func madeBy(dir string, at time.Time) bool {
	found, seen := false, 0
	filepath.WalkDir(realDir(dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if seen++; seen > 5000 {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil && !fi.ModTime().After(at) {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// lock is the skills CLI's lock: each skill's repository by its name.
// The CLI keeps it in ~/.agents, or under $XDG_STATE_HOME when that is set
// (skills 1.7: $XDG_STATE_HOME/skills/.skill-lock.json, the skills still
// in ~/.agents/skills). Both are read, the XDG one's entries winning: one
// written before the variable was set names skills the newer doesn't.
func (tr *tracer) lock() map[string]lockEntry {
	if !tr.read {
		tr.read, tr.locked = true, readSkillLock(filepath.Join(home(), ".agents", ".skill-lock.json"))
		if x := appdir.Getenv("XDG_STATE_HOME"); x != "" {
			for name, r := range readSkillLock(filepath.Join(x, "skills", ".skill-lock.json")) {
				if tr.locked == nil {
					tr.locked = map[string]lockEntry{}
				}
				tr.locked[name] = r
			}
		}
	}
	return tr.locked
}

func readSkillLock(path string) map[string]lockEntry {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f struct {
		Skills map[string]struct {
			Source        string `json:"source"`
			SourceType    string `json:"sourceType"`
			SourceURL     string `json:"sourceUrl"`
			SourceBaseURL string `json:"sourceBaseUrl"`
			InstalledAt   string `json:"installedAt"`
			UpdatedAt     string `json:"updatedAt"`
		} `json:"skills"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	out := map[string]lockEntry{}
	for name, e := range f.Skills {
		r, gh := "", false
		if e.SourceType == "github" && repoRe.MatchString(e.Source) {
			r, gh = strings.TrimSuffix(e.Source, ".git"), true
		} else if e.SourceType == "well-known" {
			r = wellKnownBase(e.SourceBaseURL, e.SourceURL)
		} else if e.SourceURL != "" {
			r = remoteRepo(e.SourceURL)
		}
		if r != "" {
			le := lockEntry{repo: r, github: gh}
			for _, ts := range []string{e.UpdatedAt, e.InstalledAt} {
				if at, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					le.at = at
					break
				}
			}
			out[name] = le
		}
	}
	return out
}

// wellKnownBase is where a kit of skills a site serves at its
// /.well-known/agent-skills/ came from (#1862, WardLu): the lock's
// sourceBaseUrl, as host/path, so Feishu's lark-base, lark-doc and
// lark-sheets are one group and Fish Audio's two another, while two kits
// under one host with different base URLs stay apart. Each skill's
// sourceUrl is its own file there, so it isn't the group. A lock written
// before sourceBaseUrl has the base in the sourceUrl, before its
// /.well-known/; one with neither is grouped by its sourceUrl as before.
func wellKnownBase(base, src string) string {
	if b := siteOf(base); b != "" {
		return b
	}
	if i := strings.Index(src, "/.well-known/"); i > 0 {
		if b := siteOf(src[:i]); b != "" {
			return b
		}
	}
	return remoteRepo(src)
}

// fromKey is the group a skill's From names. A skill brought in from
// ~/.agents/skills keeps the lock's key as it was then (ImportSkills), and
// before well-known kits were grouped by their base that was each skill's
// own sourceUrl, "docs.fish.audio/.well-known/agent-skills/fish-audio-api/
// skill.md": read as the base before /.well-known/, so a kit brought in
// then is one group as the lock now says (#1862, WardLu on v0.1.1223).
func fromKey(from string) string {
	if before, _, ok := strings.Cut(from, "/.well-known/"); ok && before != "" {
		return before
	}
	return from
}

// siteOf reads a web address as host/path, or the host alone, with no
// user, query or trailing slash: "" when it isn't an http(s) address.
func siteOf(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	p := strings.Trim(u.Path, "/")
	if before, _, ok := strings.Cut("/"+p+"/", "/.well-known/"); ok {
		p = strings.Trim(before, "/")
	}
	if strings.Contains(p, "..") {
		return ""
	}
	if p == "" {
		return host
	}
	return host + "/" + p
}

// gitRepoOf is the repository of the git checkout a skill's folder is, or
// is in a few folders down: its remote origin. The checkout is the one git
// finds (provider.GitAt): a .git git passes over, an empty one say, is
// passed over here too (#3675), and its config is in the repository's
// folder, the common one of a linked worktree. A checkout that is the home
// folder, or a folder of an app's own in it (~/.claude kept in git), is
// the user's dotfiles rather than where the skill came from.
func gitRepoOf(dir string) string {
	h := realDir(home())
	for i := 0; i < 4 && dir != "" && dir != h; i++ {
		if p := filepath.Dir(dir); p == h && strings.HasPrefix(filepath.Base(dir), ".") {
			return ""
		}
		switch git, common, err := provider.GitAt(filepath.Join(dir, ".git")); {
		case err != nil || git == "no":
			return ""
		case git == "yes":
			return originURL(filepath.Join(common, "config"))
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	return ""
}

var originSection = lazyre.New(`^\[\s*remote\s+"origin"\s*\]$`)

// originURL is the repository remote "origin" of a git config fetches.
func originURL(cfg string) string {
	b, err := os.ReadFile(cfg)
	if err != nil {
		return ""
	}
	in := false
	for _, line := range bytes.Split(b, []byte("\n")) {
		l := strings.TrimSpace(string(line))
		if strings.HasPrefix(l, "[") {
			in = originSection.MatchString(l)
			continue
		}
		if k, v, ok := strings.Cut(l, "="); in && ok && strings.TrimSpace(k) == "url" {
			return remoteRepo(strings.TrimSpace(v))
		}
	}
	return ""
}

var scpRemote = lazyre.New(`^(?:[\w.-]+@)?([\w.-]+\.[\w.-]+):(.+)$`)

// remoteRepo reads a git remote's address as a repository: owner/repo on
// GitHub, host/path elsewhere. A user or a token in it is never kept.
func remoteRepo(s string) string {
	s = strings.TrimSpace(s)
	host, p := "", ""
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		host, p = u.Hostname(), u.Path
	} else if m := scpRemote.FindStringSubmatch(s); m != nil {
		host, p = m[1], m[2]
	} else if repoRe.MatchString(s) {
		host, p = "github.com", s
	} else {
		return ""
	}
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if strings.Count(p, "/") < 1 || strings.Contains(p, "..") {
		return ""
	}
	if host == "github.com" {
		parts := strings.Split(p, "/")
		return parts[0] + "/" + parts[1]
	}
	return host + "/" + p
}

// noteFrom writes down, for the skills the library has already that a
// repository being installed from has too, that they came from there (and
// binds a copy that is the repository's whole to it, to update from):
// one whose SKILL.md is that repository's very own, and one traced to
// nowhere by the same name when the library has others from there. A
// pack added a while ago is mostly older copies (White Immortal on
// Discord: 20 of mattpocock/skills' 38 grouped, the rest edited there
// since), and a name the repository has beside others the user took
// from it is that skill. root is where the repository was fetched to. It
// gives how many it noted.
func noteFrom(src Source, root string, cands []Candidate) int {
	if src.Kind != "github" {
		return 0
	}
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return 0
	}
	tr := &tracer{}
	have := slices.ContainsFunc(l.Skills, func(s *Skill) bool { return strings.EqualFold(tr.repoOf(s), src.Repo) })
	noted, bound := 0, 0
	var same []*Skill
	for _, c := range cands {
		s := l.skill(c.Name)
		if s == nil || (s.Source != nil && s.Source.Kind == "github") {
			continue
		}
		grouped := strings.EqualFold(s.From, src.Repo)
		mine, err1 := os.ReadFile(filepath.Join(skillDir(s.Name), "SKILL.md"))
		theirs, err2 := os.ReadFile(filepath.Join(root, filepath.FromSlash(c.Path), "SKILL.md"))
		if err1 != nil || err2 != nil {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(mine), bytes.TrimSpace(theirs)) {
			if !grouped {
				s.From, have = src.Repo, true
				noted++
			}
			// the library's own copy, every file of it the repository's:
			// it is that skill, and updates from there as one installed
			// from it does (RockyShowlle on X). One linked to a folder
			// elsewhere is kept by whatever put it there (npx skills, a
			// git checkout), and an edited one stays only grouped.
			if dir := skillDir(s.Name); s.Source == nil && !linked(dir) && sameTree(dir, filepath.Join(root, filepath.FromSlash(c.Path))) {
				s.Source = &Source{Kind: "github", Repo: src.Repo, Ref: src.Ref, Path: c.Path}
				s.Hash, s.Own, s.Commit, s.From = hashDir(dir), ownHash(dir), "", ""
				l.seenAt(repoKey(src.Repo, src.Ref), c.Path)
				forgetCheck(s.Name)
				bound++
			}
		} else if !grouped && tr.repoOf(s) == "" {
			same = append(same, s)
		}
	}
	if have {
		for _, s := range same {
			s.From = src.Repo
			noted++
		}
	}
	if noted > 0 || bound > 0 {
		l.save()
	}
	return noted
}

// RegroupSkills traces the library's skills again (the Library page's
// Group again): each GitHub repository a skill is traced to is fetched,
// and the skills it has that aren't grouped with it yet are noted as from
// there, as installing from it again would. It gives how many were
// grouped; an error only when no repository could be fetched.
func RegroupSkills() (int, error) {
	l, err := load()
	if err != nil {
		return 0, err
	}
	tr := &tracer{}
	var repos []string
	seen := map[string]bool{}
	for _, s := range l.Skills {
		r := tr.repoOf(s)
		// a site's kit (docs.example.com/kit) reads like owner/repo, but
		// a GitHub owner has no dot
		if !repoRe.MatchString(r) || strings.Contains(strings.Split(r, "/")[0], ".") || seen[strings.ToLower(r)] {
			continue
		}
		seen[strings.ToLower(r)] = true
		repos = append(repos, r)
	}
	noted, fetched := 0, 0
	var first error
	for _, r := range repos {
		src := Source{Kind: "github", Repo: r}
		root, err := fetch(src)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		fetched++
		noted += noteFrom(src, root, candidates(root, "", srcFolder(src)))
		os.RemoveAll(root)
	}
	if fetched == 0 && first != nil {
		return 0, first
	}
	return noted, nil
}
