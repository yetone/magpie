package library

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/source"
	"gopkg.in/yaml.v3"
)

// Skill is a folder with a SKILL.md, kept in the library and linked into
// the agents that get it.
type Skill struct {
	Name   string   `json:"name"`
	Source *Source  `json:"source,omitempty"`
	Agents []string `json:"agents"`
	// Hash is of the skill's files as last fetched from GitHub, and Commit
	// the last commit to have touched its folder there, once a check has
	// found the two to agree: together they tell whether GitHub has
	// changed the skill since.
	Hash   string `json:"hash,omitempty"`
	Commit string `json:"commit,omitempty"`
	// From is the repository one not installed from GitHub was found to
	// come from (skill_from.go), only to group it with the rest of it
	From string `json:"from,omitempty"`
}

// Source is where a skill came from: a GitHub repository it can be updated
// from, or a folder on this machine the library links to, so that editing
// the folder is editing the skill.
type Source struct {
	Kind string `json:"kind"` // github | folder
	Repo string `json:"repo,omitempty"`
	Ref  string `json:"ref,omitempty"`
	Path string `json:"path,omitempty"` // in the repository
	Dir  string `json:"dir,omitempty"`  // the folder, for kind folder
}

func (s *Source) String() string {
	if s == nil {
		return ""
	}
	if s.Kind == "folder" {
		return s.Dir
	}
	u := "https://github.com/" + s.Repo
	if s.Ref != "" || s.Path != "" {
		ref := s.Ref
		if ref == "" {
			ref = "HEAD"
		}
		u += "/tree/" + ref
		if s.Path != "" {
			u += "/" + s.Path
		}
	}
	return u
}

func skillsDir() string { return filepath.Join(Dir(), "skills") }

func skillDir(name string) string { return filepath.Join(skillsDir(), name) }

// meta is what a SKILL.md says of itself in its front matter.
type meta struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
}

func readMeta(dir string) (meta, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return meta{}, false
	}
	return parseMeta(b), true
}

// parseMeta reads a SKILL.md's front matter.
func parseMeta(b []byte) meta {
	var m meta
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if rest, ok := strings.CutPrefix(s, "---\n"); ok {
		if front, _, ok := strings.Cut(rest, "\n---"); ok {
			_ = yaml.Unmarshal([]byte(front), &m)
		}
	}
	m.Name, m.Description = strings.TrimSpace(m.Name), strings.Join(strings.Fields(m.Description), " ")
	return m
}

var unsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// nameFor is the name a skill found at dir goes by: what its SKILL.md
// says, else its folder's, made fit for a file name.
func nameFor(dir string, m meta) string {
	n := m.Name
	if n == "" {
		n = filepath.Base(dir)
	}
	n = strings.Trim(unsafe.ReplaceAllString(n, "-"), "-_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// ---- linking into agents --------------------------------------------------

// marker is left in a copy magpie makes where it can't link (Windows
// without the right to), to know the copy for its own.
const marker = ".magpie-library"

// ours reports whether the entry at p is magpie's link (or copy) of the
// library's skill by that name.
func ours(p, name string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	if linkEntry(fi) {
		to, err := os.Readlink(p)
		return err == nil && filepath.Clean(to) == filepath.Clean(skillDir(name))
	}
	if fi.IsDir() {
		_, err := os.Stat(filepath.Join(p, marker))
		return err == nil
	}
	return false
}

func link(p, name string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	err := dirLink(skillDir(name), p)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	if err := copyDir(skillDir(name), p); err != nil {
		os.RemoveAll(p)
		return err
	}
	return markCopy(p, "copied from "+skillDir(name)+"\n")
}

// markCopy writes magpie's mark into a copy just made at p: what it says,
// then the hash of what was copied. A sync tells a copy is still the
// library's skill by that hash and the mark's time (copyIsFresh), without
// reading every file of every copy each time (#1031).
func markCopy(p, says string) error {
	h := hashDir(p)
	if h != "" {
		says += "hash " + h + "\n"
	}
	return os.WriteFile(filepath.Join(p, marker), []byte(says), 0o644)
}

// markedHash is the hash magpie's mark at p says was copied, if it says one.
func markedHash(p string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(p, marker))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "hash "); ok && h != "" {
			return h, true
		}
	}
	return "", false
}

// copyIn puts a copy of the library's skill at p, marked as magpie's, in
// place of a link: made beside it and put in the place of what is there,
// so the agent never finds half of one.
func copyIn(p, name string) error {
	lib := skillDir(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// an old copy a previous swap couldn't remove is tried again
	dropOld(p, name)
	next := filepath.Join(filepath.Dir(p), "."+name+".magpie-next")
	os.RemoveAll(next)
	if err := copyDir(realDir(lib), next); err != nil {
		os.RemoveAll(next)
		return err
	}
	if err := markCopy(next, "copied from "+lib+" by magpie, and copied again when it changes\n"); err != nil {
		os.RemoveAll(next)
		return err
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.IsDir() {
		// nothing there yet, or a link, which goes in one step
		if err := unlink(p); err != nil {
			os.RemoveAll(next)
			return err
		}
		return os.Rename(next, p)
	}
	// The old copy is moved aside whole before the new one goes in, and only
	// then removed: removing it in place can fail part-way (a file held open
	// on Windows), which would leave the agent a copy with no SKILL.md and no
	// marker that magpie no longer knows for its own.
	old := oldPath(p, name)
	if err := os.Rename(p, old); err != nil {
		os.RemoveAll(next)
		return err
	}
	if err := os.Rename(next, p); err != nil {
		// put the old copy back, so the agent keeps a whole skill
		if back := os.Rename(old, p); back != nil {
			return errors.Join(err, back)
		}
		os.RemoveAll(next)
		return err
	}
	// best effort: what's left is tried again on the next sync
	os.RemoveAll(old)
	return nil
}

// oldPrefix starts the name an old copy of a skill is moved aside to while
// the new one is put in its place.
func oldPrefix(name string) string { return "." + name + ".magpie-old" }

// oldPath is a free name beside p to move the old copy of a skill aside to.
func oldPath(p, name string) string {
	dir := filepath.Dir(p)
	for i := 0; ; i++ {
		o := filepath.Join(dir, oldPrefix(name))
		if i > 0 {
			o += "-" + strconv.Itoa(i)
		}
		if _, err := os.Lstat(o); errors.Is(err, fs.ErrNotExist) {
			return o
		}
	}
}

// dropOld removes, best effort, the old copies of a skill earlier swaps
// moved aside beside p but couldn't remove.
func dropOld(p, name string) {
	es, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		return
	}
	pre := oldPrefix(name)
	for _, e := range es {
		n := e.Name()
		if n == pre || (strings.HasPrefix(n, pre+"-") && isDigits(n[len(pre)+1:])) {
			os.RemoveAll(filepath.Join(filepath.Dir(p), n))
		}
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// fresh is whether both folders could be read and the copy at p holds
// what the library's skill does. Failed hashes don't prove it can be discarded.
func fresh(p, name string) bool {
	return copyIsFresh(p, hashDir(realDir(skillDir(name))))
}

// copyIsFresh is fresh against the library skill's hash, lib. A copy whose
// mark says it was made of lib, with nothing in it changed since the mark,
// is that without its files being read: a sync used to read every file of
// every copy in every agent, which over a WSL distro's share took a group's
// switch 10 seconds and more (#1031). Any other copy is read and hashed.
func copyIsFresh(p, lib string) bool {
	return copyIsFreshAs(p, lib, false)
}

// copyIsFreshAs is copyIsFresh for a copy known to be unchanged since its
// mark (untouched), which then isn't walked again.
func copyIsFreshAs(p, lib string, untouched bool) bool {
	if lib == "" {
		return false
	}
	if h, ok := markedHash(p); ok {
		if untouched {
			return h == lib
		}
		if made, ok := copiedAt(p); ok && !changedAt(p).After(made) {
			return h == lib
		}
	}
	h := hashDir(p)
	return h != "" && h == lib
}

// libHash is hashDir of the library's skill by that name, read once for
// a change rather than once for each agent given it.
func (l *Library) libHash(name string) string {
	if h, ok := l.hashes[name]; ok {
		return h
	}
	h := hashDir(realDir(skillDir(name)))
	if l.hashes == nil {
		l.hashes = map[string]string{}
	}
	l.hashes[name] = h
	return h
}

func unlink(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil
	}
	if linkEntry(fi) {
		return os.Remove(p)
	}
	return os.RemoveAll(p)
}

// realDir is where a folder really is, links followed: symlinks, and on
// Windows junctions too, which filepath.EvalSymlinks leaves as they are
// (a skill junctioned into two agents from ~/.agents/skills is one folder).
func realDir(p string) string {
	p = filepath.Clean(p)
	if r, ok := resolve(p, 0); ok {
		return r
	}
	return p
}

// realBelow is realDir for a path that may not be there yet: the nearest
// folder above it that is, links followed, and the rest as it is.
func realBelow(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if _, err := os.Lstat(p); err == nil {
			return filepath.Join(realDir(p), rest)
		}
		up := filepath.Dir(p)
		if up == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = up
	}
}

// resolve follows every link on the way to p, a part at a time; not ok
// when a part of it isn't there or the links go round.
func resolve(p string, depth int) (string, bool) {
	if depth > 40 {
		return "", false
	}
	vol := filepath.VolumeName(p)
	cur := vol
	if filepath.IsAbs(p) {
		cur = vol + string(filepath.Separator)
	}
	parts := strings.FieldsFunc(p[len(vol):], func(r rune) bool { return r < 128 && os.IsPathSeparator(uint8(r)) })
	for i, part := range parts {
		next := part
		if cur != "" {
			next = filepath.Join(cur, part)
		}
		if to, ok := linkTarget(next); ok {
			if !filepath.IsAbs(to) {
				to = filepath.Join(filepath.Dir(next), to)
			}
			return resolve(filepath.Join(append([]string{to}, parts[i+1:]...)...), depth+1)
		}
		if _, err := os.Lstat(next); err != nil {
			return "", false
		}
		cur = next
	}
	if cur == "" {
		cur = "."
	}
	return cur, true
}

// linkTarget is where the entry at p points when it is a link: a symlink,
// or on Windows a junction (a mount point, which Go since 1.23 reports as
// irregular, not as a symlink).
func linkTarget(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", false
	}
	if !linkEntry(fi) {
		return "", false
	}
	to, err := os.Readlink(p)
	if err != nil || to == "" {
		return "", false
	}
	return to, true
}

// linkEntry is whether the entry is a link rather than a folder of its own:
// a symlink, or on Windows a junction (which Go reports as irregular).
// Taking one away is os.Remove, which leaves what it points at, even when
// where it points can't be read.
func linkEntry(fi fs.FileInfo) bool {
	return fi.Mode()&fs.ModeSymlink != 0 || runtime.GOOS == "windows" && fi.Mode()&fs.ModeIrregular != 0
}

// linked is whether the entry at p is a link to a folder elsewhere rather
// than a folder of its own: taking it away leaves that folder as it is.
func linked(p string) bool {
	_, ok := linkTarget(p)
	return ok
}

// sharedSkillsDir is the user-wide shared skills folder, the cross-agent
// convention a project's .agents/skills is the project's own of (#227):
// the library finds skills there but never moves one out of it. It gives
// skills to it only for the agents whose one place it is (Kimi Code,
// Goose, Cindy): one link there, whichever of them it's for.
func sharedSkillsDir() string { return filepath.Join(home(), ".agents", "skills") }

// sharedHas is whether the library's skill by that name is kept in
// ~/.agents/skills: the user's own entry there (not a link magpie made)
// is the very folder the library's is or links to, so every agent that
// reads the shared folder has it, whatever the library gives.
func sharedHas(name string) bool {
	p, lib := filepath.Join(sharedSkillsDir(), name), skillDir(name)
	if _, err := os.Lstat(p); err != nil || ours(p, name) {
		return false
	}
	if _, err := os.Stat(lib); err != nil {
		return false
	}
	return realDir(p) == realDir(lib)
}

// within is whether p is dir or inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (l *Library) syncSkills(t *Target, res *Result, all []*Target) {
	if t.Skills == "" {
		return
	}
	id := t.Agent.ID
	a := l.applied(id)
	// another agent may read the very same folder (one linked to the other):
	// what that agent is to have stays
	sharers := []string{}
	for _, o := range all {
		if o != t && o.Skills != "" && realDir(o.Skills) == realDir(t.Skills) {
			sharers = append(sharers, o.Agent.ID)
		}
	}
	cp := l.copies(t, sharers)
	wanted := func(s *Skill) bool {
		if s == nil {
			return false
		}
		return slices.Contains(s.Agents, id) || slices.ContainsFunc(sharers, func(o string) bool { return slices.Contains(s.Agents, o) })
	}
	// an agent that reads ~/.agents/skills as well as its own folder finds
	// a skill magpie put there for another (Kimi Code) already: a link in
	// its own too would be the skill twice
	shared := realDir(sharedSkillsDir())
	inShared := func(s *Skill) bool {
		if s == nil || !slices.Contains(readsShared, id) || realDir(t.Skills) == shared {
			return false
		}
		// the library's skill is kept in the shared folder itself (brought
		// in from there): every agent reading it has it, ticked or not (#595)
		if sharedHas(s.Name) {
			return true
		}
		p := filepath.Join(sharedSkillsDir(), s.Name)
		return slices.ContainsFunc(all, func(o *Target) bool {
			return o.Skills != "" && realDir(o.Skills) == shared && slices.Contains(s.Agents, o.Agent.ID)
		}) && (ours(p, s.Name) || realDir(p) == realDir(skillDir(s.Name)))
	}
	var mine []string
	for _, name := range a.Skills {
		s := l.skill(name)
		p := filepath.Join(t.Skills, name)
		if wanted(s) && !inShared(s) || !ours(p, name) {
			continue
		}
		if err := unlink(p); err != nil {
			res.fail(id, "skill:"+name, err)
			mine = append(mine, name)
			continue
		}
		dropOld(p, name)
		res.changed(id)
	}
	for _, s := range l.Skills {
		if !slices.Contains(s.Agents, id) {
			continue
		}
		p := filepath.Join(t.Skills, s.Name)
		if inShared(s) {
			// a copy of its own of the very same files would be the skill
			// twice as much as a link: kept aside, as it would be for one
			if _, err := os.Lstat(p); err == nil && !ours(p, s.Name) && realDir(p) != realDir(skillDir(s.Name)) && sameTree(p, skillDir(s.Name)) {
				if _, err := setAside(id, p); err != nil {
					res.fail(id, "skill:"+s.Name, err)
				} else {
					res.changed(id)
				}
			}
			continue
		}
		// the folder the library's skill links to is there already: one
		// brought in from ~/.agents/skills, which stays where it is
		if ours(p, s.Name) || realDir(p) == realDir(skillDir(s.Name)) {
			// an old copy a previous swap couldn't remove is tried again
			if ours(p, s.Name) {
				dropOld(p, s.Name)
			}
			// magpie's copy in an agent given links now is a link again
			// (#896), unless no link can be made here (Windows without the
			// right to), where the copy stays
			if ours(p, s.Name) && !cp && !linked(p) {
				if ok, err := relink(id, p, s.Name); err != nil {
					res.fail(id, "skill:"+s.Name, err)
				} else if ok {
					res.changed(id)
				}
			}
			// a copy is made again once the library's skill has changed: in
			// an agent that takes copies, and where magpie couldn't link
			// (Windows without the right to) and left a copy instead
			if ours(p, s.Name) && ((cp && linked(p)) || (!linked(p) && l.libHash(s.Name) != "" && !copyIsFreshAs(p, l.libHash(s.Name), l.untouched[p]))) {
				if err := copyIn(p, s.Name); err != nil {
					res.fail(id, "skill:"+s.Name, err)
				} else {
					res.changed(id)
				}
			}
			mine = append(mine, s.Name)
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			// the agent has one of its own by that name: the very one, or
			// a copy of it (what installed it put it back, or copied it
			// into each agent, as CC Switch does), gives way to the
			// library's, kept aside; one that differs is the user's to
			// settle (UseLibrarySkill, KeepAgentSkill)
			if !sameTree(p, skillDir(s.Name)) {
				res.Problems = append(res.Problems, Problem{Agent: id, What: "skill:" + s.Name, Own: true,
					Error: fmt.Sprintf("%s already has a skill of its own called %s", t.Agent.Name, s.Name)})
				continue
			}
			if _, err := setAside(id, p); err != nil {
				res.fail(id, "skill:"+s.Name, err)
				continue
			}
		}
		put := link
		if cp {
			put = copyIn
		}
		if err := put(p, s.Name); err != nil {
			res.fail(id, "skill:"+s.Name, err)
			continue
		}
		mine = append(mine, s.Name)
		res.changed(id)
	}
	a.Skills = mine
}

// ---- finding skills to install --------------------------------------------

// Candidate is a skill found where the user pointed: in a repository, or
// in a folder.
type Candidate struct {
	Path        string `json:"path"` // in the repository or the folder; "" for its root
	Name        string `json:"name"`
	Description string `json:"description"`
	Have        bool   `json:"have"` // the library has a skill by that name
}

// Probe is what was found at a source.
type Probe struct {
	Source     string      `json:"source"`
	Kind       string      `json:"kind"`
	Candidates []Candidate `json:"candidates"`
	root       string      // the folder the candidates' paths are in
	src        Source
}

var probes = struct {
	sync.Mutex
	m map[string]*probeEntry
}{m: map[string]*probeEntry{}}

type probeEntry struct {
	p   *Probe
	tmp string
	at  time.Time
}

// githubSource reads a repository out of what the user typed: owner/repo,
// or a github.com URL, to a folder in it.
func githubSource(s string) (Source, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	if !strings.Contains(s, "://") && strings.HasPrefix(s, "github.com/") {
		s = "https://" + s
	}
	var parts []string
	if u, err := url.Parse(s); err == nil && u.Scheme != "" {
		if u.Host != "github.com" && u.Host != "www.github.com" {
			return Source{}, false
		}
		parts = strings.Split(strings.Trim(u.Path, "/"), "/")
	} else if regexp.MustCompile(`^[\w.-]+/[\w.-]+$`).MatchString(s) {
		parts = strings.Split(s, "/")
	} else {
		return Source{}, false
	}
	if len(parts) < 2 {
		return Source{}, false
	}
	src := Source{Kind: "github", Repo: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")}
	if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
		if src.Ref = parts[3]; src.Ref == "HEAD" {
			src.Ref = "" // the default branch, as a source without a ref is
		}
		src.Path = strings.Join(parts[4:], "/")
		src.Path = strings.TrimSuffix(strings.TrimSuffix(src.Path, "SKILL.md"), "/")
	}
	return src, true
}

func expand(p string) string {
	if rest, ok := strings.CutPrefix(p, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == '\\') {
		return filepath.Join(home(), rest)
	}
	return p
}

// tarballURL is where GitHub hands out a repository's files; a variable
// for tests. It is codeload, what github.com's own "Download ZIP" uses,
// rather than the API's /tarball: the API allows 60 requests an hour to an
// address without a token, which a few installs, or a network shared with
// others, used up.
var tarballURL = func(repo, ref string) string {
	if ref == "" {
		ref = "HEAD"
	}
	return "https://codeload.github.com/" + repo + "/tar.gz/" + url.PathEscape(ref)
}

// fetch downloads the repository into a new folder and gives it back.
func fetch(src Source) (string, error) {
	req, _ := http.NewRequest("GET", tarballURL(src.Repo, src.Ref), nil)
	req.Header.Set("User-Agent", "magpie")
	c := &http.Client{Timeout: 2 * time.Minute}
	resp, err := source.DoOfficial(c, req)
	if err != nil {
		return "", fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404:
		if src.Ref != "" {
			return "", fmt.Errorf("GitHub has no %s at %s (a private repository can't be installed from)", src.Repo, src.Ref)
		}
		return "", fmt.Errorf("GitHub has no repository %s (a private one can't be installed from)", src.Repo)
	case resp.StatusCode == 403 || resp.StatusCode == 429:
		return "", fmt.Errorf("GitHub is limiting requests from here; try again in a while")
	case resp.StatusCode != 200:
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	tmp, err := os.MkdirTemp("", "magpie-skill-")
	if err != nil {
		return "", err
	}
	if err := untar(resp.Body, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	return tmp, nil
}

// untar unpacks a GitHub tarball, whose one top folder is dropped.
func untar(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	total := int64(0)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		_, rel, _ := strings.Cut(h.Name, "/")
		if rel == "" {
			continue
		}
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if !strings.HasPrefix(p, dst+string(filepath.Separator)) {
			continue // never outside
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += h.Size; total > 200<<20 {
				return fmt.Errorf("the repository is too big to install skills from (over 200 MB)")
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}

// candidates are the skills under root/sub: a folder with a SKILL.md,
// looked for a few folders deep, and not inside another skill.
func candidates(root, sub string) []Candidate {
	base := filepath.Join(root, filepath.FromSlash(sub))
	var out []Candidate
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if m, ok := readMeta(dir); ok {
			rel, _ := filepath.Rel(root, dir)
			if rel == "." {
				rel = ""
			}
			out = append(out, Candidate{Path: filepath.ToSlash(rel), Name: nameFor(dir, m), Description: m.Description})
			return
		}
		if depth == 0 {
			return
		}
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			n := e.Name()
			if strings.HasPrefix(n, ".") || n == "node_modules" {
				continue
			}
			p := filepath.Join(dir, n)
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				walk(p, depth-1)
			}
		}
	}
	walk(base, 4)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ProbeSkills looks for skills at what the user typed: a GitHub repository
// (or a folder in one), or a folder on this machine.
func ProbeSkills(input string) (*Probe, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("paste a GitHub link or a folder's path")
	}
	probes.Lock()
	for k, e := range probes.m {
		if time.Since(e.at) > 15*time.Minute {
			os.RemoveAll(e.tmp)
			delete(probes.m, k)
		}
	}
	probes.Unlock()

	p := &Probe{Source: input}
	if dir := expand(input); filepath.IsAbs(dir) {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s isn't a folder", input)
		}
		p.Kind, p.root, p.src = "folder", dir, Source{Kind: "folder", Dir: dir}
		p.Candidates = candidates(dir, "")
	} else if src, ok := githubSource(input); ok {
		tmp, err := fetch(src)
		if err != nil {
			return nil, err
		}
		p.Kind, p.root, p.src = "github", tmp, src
		if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(src.Path))); src.Path != "" && err != nil {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("%s has no folder %s", src.Repo, src.Path)
		}
		p.Candidates = candidates(tmp, src.Path)
		probes.Lock()
		if old := probes.m[input]; old != nil {
			os.RemoveAll(old.tmp)
		}
		probes.m[input] = &probeEntry{p: p, tmp: tmp, at: time.Now()}
		probes.Unlock()
	} else {
		return nil, fmt.Errorf("that's neither a GitHub repository nor a folder's full path")
	}
	if p.Candidates == nil {
		p.Candidates = []Candidate{}
	}
	if l, err := load(); err == nil {
		for i, c := range p.Candidates {
			p.Candidates[i].Have = l.skill(c.Name) != nil
		}
	}
	noteFrom(p.src, p.root, p.Candidates)
	return p, nil
}

func cachedProbe(input string) (*Probe, error) {
	probes.Lock()
	e := probes.m[strings.TrimSpace(input)]
	probes.Unlock()
	if e != nil {
		if _, err := os.Stat(e.tmp); err == nil {
			return e.p, nil
		}
	}
	return ProbeSkills(input)
}

// InstallSkills adds the skills at those paths of the source to the library
// and gives them to the agents named. One the library has already, or
// one whose name is taken, is left as it is and said in the result; the
// others are installed all the same.
func InstallSkills(input string, paths, agents []string) (*Result, error) {
	p, err := cachedProbe(input)
	if err != nil {
		return nil, err
	}
	return installChange(func(l *Library, in *installed) error { return installFrom(l, p, paths, agents, true, in) })
}

// installed is what an install did with each skill picked: added, had
// already (the same skill from the same source), or left out, and why.
type installed struct {
	added, had []string
	skipped    []Problem
}

func (in *installed) skip(name string, err error) {
	in.skipped = append(in.skipped, Problem{What: "skill:" + name, Error: err.Error()})
}

// installChange runs an install in a change of the library. Only when
// nothing was added or had already does it fail, with the first reason a
// skill was left out: one skill in the way of a set never stops the rest
// (lc on Discord).
func installChange(f func(l *Library, in *installed) error) (*Result, error) {
	in := &installed{}
	res, err := change(func(l *Library) error {
		if err := f(l, in); err != nil {
			return err
		}
		if len(in.added) == 0 && len(in.had) == 0 && len(in.skipped) > 0 {
			return errors.New(in.skipped[0].Error)
		}
		return nil
	})
	if res != nil {
		res.Installed, res.Had, res.Skipped = in.added, in.had, in.skipped
	}
	return res, err
}

// fromSameSource is whether the library's skill came from where the one
// being installed comes from: that repository's folder, or that folder.
func fromSameSource(s *Skill, src Source, path, from string) bool {
	if s.Source == nil || s.Source.Kind != src.Kind {
		return false
	}
	switch src.Kind {
	case "github":
		return strings.EqualFold(s.Source.Repo, src.Repo) && s.Source.Path == path
	case "folder":
		return realDir(s.Source.Dir) == realDir(from)
	}
	return false
}

// installFrom adds the skills at those paths of a probe's source to the
// library. shown is whether the user saw every skill the probe found, in
// the picker: those not picked aren't offered as new by a check. A skill
// that can't be added is said in in.skipped, and the user's folder in its
// way is never written into or taken away.
func installFrom(l *Library, p *Probe, paths, agents []string, shown bool, in *installed) error {
	if err := os.MkdirAll(skillsDir(), 0o755); err != nil {
		return err
	}
	for _, path := range paths {
		i := slices.IndexFunc(p.Candidates, func(c Candidate) bool { return c.Path == path })
		if i < 0 {
			in.skip(lastPart(path), fmt.Errorf("no skill at %q", path))
			continue
		}
		c := p.Candidates[i]
		if err := checkName("skill", c.Name); err != nil {
			in.skip(c.Name, err)
			continue
		}
		from := filepath.Join(p.root, filepath.FromSlash(c.Path))
		src := p.src
		// the same skill from the same place is had already, whatever
		// the user has changed in it since; another by that name is theirs
		if s := l.skill(c.Name); s != nil {
			if fromSameSource(s, src, c.Path, from) {
				in.had = append(in.had, c.Name)
			} else {
				in.skip(c.Name, fmt.Errorf("the library already has another skill called %s, left as it is", c.Name))
			}
			continue
		}
		dir := skillDir(c.Name)
		// something by that name in the library's folder that the
		// library doesn't list is never written into: that would mix
		// two skills' files, and a failed copy would take it away. One
		// holding exactly this skill's files (an install that stopped
		// before the library was saved left it) is listed where it is.
		if _, err := os.Lstat(dir); err == nil {
			switch {
			case src.Kind == "folder" && linked(dir) && realDir(dir) == realDir(from):
				src.Dir = from
			case src.Kind == "github" && !linked(dir) && sameTree(dir, from):
				src.Path = c.Path
			case src.Kind == "folder" && realDir(from) == realDir(dir):
				// a folder already in the library's own (#595): linked to
				// itself it fails, and copied onto itself every file of it
				// was emptied
				in.skip(c.Name, fmt.Errorf("%s is in the library's folder already: bring it in from the skills found there", c.Name))
				continue
			default:
				in.skip(c.Name, fmt.Errorf("the library's folder already has a %s that isn't this one (%s), left as it is: bring it in from the skills found there to use it", c.Name, dir))
				continue
			}
			in.had = append(in.had, c.Name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			// can't tell what is there: left alone
			in.skip(c.Name, err)
			continue
		} else if src.Kind == "folder" {
			src.Dir = from
			if err := dirLink(from, dir); err != nil {
				if errors.Is(err, fs.ErrExist) {
					in.skip(c.Name, fmt.Errorf("the library's folder already has a %s in it (%s)", c.Name, dir))
					continue
				}
				if err := copyDir(from, dir); err != nil {
					os.RemoveAll(dir)
					in.skip(c.Name, err)
					continue
				}
			}
			in.added = append(in.added, c.Name)
		} else {
			src.Path = c.Path
			if err := copyDir(from, dir); err != nil {
				os.RemoveAll(dir)
				in.skip(c.Name, err)
				continue
			}
			in.added = append(in.added, c.Name)
		}
		sk := &Skill{Name: c.Name, Source: &src, Agents: slices.Clone(agents)}
		if src.Kind == "github" {
			sk.Hash = hashDir(dir)
			// every skill the picker showed was offered: a check
			// offers only those the repository adds later
			l.seenAt(src.Repo, src.Ref, c.Path)
			if shown {
				for _, c := range p.Candidates {
					l.seenAt(src.Repo, src.Ref, c.Path)
				}
			}
		}
		forgetCheck(c.Name)
		l.Skills = append(l.Skills, sk)
	}
	return nil
}

// UpdateSkill fetches a skill from GitHub again, in place: the agents'
// links go on pointing at it.
func UpdateSkill(name string) (*Result, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	s := l.skill(name)
	if s == nil {
		return nil, fmt.Errorf("no skill called %s", name)
	}
	f := &fetcher{}
	defer f.clean()
	up, err := f.prepare(s)
	if err != nil {
		return nil, err
	}
	return change(func(l *Library) error { return up.apply(l) })
}

// UpdateSkills fetches again every skill that came from GitHub, each
// repository once, and writes the agents once. A skill that couldn't be
// fetched is said in Unupdated; the others are updated all the same.
func UpdateSkills() (*Result, error) { return updateSkills(nil) }

// UpdateSomeSkills is UpdateSkills for the skills named only: those a
// check found GitHub to have changed.
func UpdateSomeSkills(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no skills to update")
	}
	return updateSkills(names)
}

func updateSkills(names []string) (*Result, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	f := &fetcher{}
	defer f.clean()
	var (
		ups    []*skillUpdate
		failed []Problem
		pmu    sync.Mutex
		wg     sync.WaitGroup
		sem    = make(chan struct{}, 4)
	)
	for _, s := range l.Skills {
		if !updatable(s) || names != nil && !slices.Contains(names, s.Name) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			up, err := f.prepare(s)
			pmu.Lock()
			defer pmu.Unlock()
			if err != nil {
				failed = append(failed, Problem{What: "skill:" + s.Name, Error: err.Error()})
				return
			}
			ups = append(ups, up)
		}()
	}
	wg.Wait()
	sort.Slice(failed, func(i, j int) bool { return failed[i].What < failed[j].What })
	res, err := change(func(l *Library) error {
		for _, up := range ups {
			if err := up.apply(l); err != nil {
				failed = append(failed, Problem{What: "skill:" + up.name, Error: err.Error()})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Updated = []string{}
	for _, up := range ups {
		if !slices.ContainsFunc(failed, func(p Problem) bool { return p.What == "skill:"+up.name }) {
			res.Updated = append(res.Updated, up.name)
		}
	}
	sort.Strings(res.Updated)
	res.Unupdated = failed
	return res, nil
}

// updatable is whether a skill can be fetched again: one from GitHub, or
// one CC Switch installed from there.
func updatable(s *Skill) bool {
	if s.Source != nil && s.Source.Kind == "github" {
		return true
	}
	_, ok := ccSwitchOrigin(s)
	return ok
}

// fetcher downloads each repository at each ref once, however many of
// its skills are updated.
type fetcher struct {
	mu   sync.Mutex
	got  map[string]*fetched
	dirs []string
}

type fetched struct {
	once sync.Once
	dir  string
	err  error
}

func (f *fetcher) fetch(src Source) (string, error) {
	f.mu.Lock()
	if f.got == nil {
		f.got = map[string]*fetched{}
	}
	key := src.Repo + "@" + src.Ref
	g := f.got[key]
	if g == nil {
		g = &fetched{}
		f.got[key] = g
	}
	f.mu.Unlock()
	g.once.Do(func() {
		g.dir, g.err = fetch(src)
		if g.err == nil {
			f.mu.Lock()
			f.dirs = append(f.dirs, g.dir)
			f.mu.Unlock()
		}
	})
	return g.dir, g.err
}

func (f *fetcher) clean() {
	for _, d := range f.dirs {
		os.RemoveAll(d)
	}
}

// skillUpdate is a skill fetched again, ready to take the old one's place.
type skillUpdate struct {
	name  string
	src   *Source
	adopt bool
	from  string
}

func (f *fetcher) prepare(s *Skill) (*skillUpdate, error) {
	name := s.Name
	src, adopt := s.Source, false
	if src == nil || src.Kind != "github" {
		// one CC Switch installed from GitHub becomes the library's own,
		// fetched from there, leaving CC Switch's folder as it is
		o, ok := ccSwitchOrigin(s)
		if !ok {
			return nil, fmt.Errorf("%s isn't from GitHub; it's kept as it is", name)
		}
		src, adopt = &o, true
	} else {
		c := *src
		src = &c
	}
	tmp, err := f.fetch(*src)
	if err != nil && adopt && src.Ref != "" {
		src.Ref = "" // the branch CC Switch recorded is gone: the default one
		tmp, err = f.fetch(*src)
	}
	if err != nil {
		return nil, err
	}
	if adopt {
		path, ok := origin(tmp, *src, name)
		if !ok {
			return nil, fmt.Errorf("%s has no skill %s any more", src.Repo, name)
		}
		src.Path = path
	}
	from := filepath.Join(tmp, filepath.FromSlash(src.Path))
	if _, ok := readMeta(from); !ok {
		return nil, fmt.Errorf("%s has no SKILL.md at %s any more", src.Repo, src.Path)
	}
	return &skillUpdate{name: name, src: src, adopt: adopt, from: from}, nil
}

// apply puts the fetched skill in the old one's place; the agents' links
// go on pointing at it.
func (up *skillUpdate) apply(l *Library) error {
	name := up.name
	next, old := skillDir("."+name+".next"), skillDir("."+name+".old")
	os.RemoveAll(next)
	os.RemoveAll(old)
	if err := copyDir(up.from, next); err != nil {
		os.RemoveAll(next)
		return err
	}
	hash := hashDir(next)
	if fi, err := os.Lstat(skillDir(name)); err == nil && linkEntry(fi) {
		// a link to CC Switch's folder: only the link goes
		if err := os.Remove(skillDir(name)); err != nil {
			os.RemoveAll(next)
			return err
		}
		old = ""
	} else if err := os.Rename(skillDir(name), old); err != nil {
		os.RemoveAll(next)
		return err
	}
	if err := os.Rename(next, skillDir(name)); err != nil {
		if old != "" {
			os.Rename(old, skillDir(name))
		}
		return err
	}
	if s := l.skill(name); s != nil {
		if up.adopt {
			s.Source = up.src
		}
		// the commit it's at is known again once a check finds the files
		// GitHub has to be these
		s.Hash, s.Commit = hash, ""
	}
	forgetCheck(name)
	if old == "" {
		return nil
	}
	return os.RemoveAll(old)
}

// SkillAgents sets which agents get a skill.
func SkillAgents(name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		s := l.skill(name)
		if s == nil {
			return fmt.Errorf("no skill called %s", name)
		}
		s.Agents = slices.Sorted(slices.Values(agents))
		return nil
	})
}

// EverySkillAgents gives every skill in the library to the agents named, or
// takes every one from them, in one write rather than one for each skill
// (#443). An agent not named keeps what it has, as with a skill's All chip.
func EverySkillAgents(agents []string, on bool) (*Result, error) {
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents to give the skills to")
	}
	return skillsAgents(nil, agents, on)
}

// SomeSkillsAgents does as EverySkillAgents for the skills named alone: a
// repository's skills, from its group's chips (#787).
func SomeSkillsAgents(names, agents []string, on bool) (*Result, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no skills to give the agents")
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents to give the skills to")
	}
	return skillsAgents(names, agents, on)
}

// skillsAgents gives the skills named, or every one when none are, to the
// agents, or takes them from them.
func skillsAgents(names, agents []string, on bool) (*Result, error) {
	return change(func(l *Library) error {
		for _, name := range names {
			if l.skill(name) == nil {
				return fmt.Errorf("no skill called %s", name)
			}
		}
		for _, s := range l.Skills {
			if names != nil && !slices.Contains(names, s.Name) {
				continue
			}
			kept := slices.DeleteFunc(slices.Clone(s.Agents), func(a string) bool { return slices.Contains(agents, a) })
			if on {
				kept = append(kept, agents...)
			}
			s.Agents = slices.Sorted(slices.Values(kept))
		}
		return nil
	})
}

// RemoveSkill takes a skill out of the library and every agent; its folder
// is kept aside with the backups, never just deleted.
func RemoveSkill(name string) (*Result, error) {
	return change(func(l *Library) error {
		return removeSkill(l, name, time.Now().Format("2006-01-02_15-04-05.000"))
	})
}

// RemoveSkills takes the skills named out of the library and every agent
// in one write (#449), each as RemoveSkill does: the folders kept aside
// together, in one backup, and a folder of the user's only unlinked. One
// that can't be taken out stays, and is said in Unremoved.
func RemoveSkills(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no skills to remove")
	}
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	var failed []Problem
	res, err := change(func(l *Library) error {
		stamp := time.Now().Format("2006-01-02_15-04-05.000")
		for _, name := range names {
			if err := removeSkill(l, name, stamp); err != nil {
				failed = append(failed, Problem{What: "skill:" + name, Error: err.Error()})
			}
		}
		if len(failed) == len(names) {
			return errors.New(failed[0].Error)
		}
		return nil
	})
	if res != nil {
		res.Unremoved = failed
	}
	return res, err
}

// removeSkill takes a skill out of l, its folder moved aside to the backup
// stamped so, or its link to a folder of the user's taken away. The entry
// goes only once its folder has.
func removeSkill(l *Library, name, stamp string) error {
	i := slices.IndexFunc(l.Skills, func(s *Skill) bool { return s.Name == name })
	if i < 0 {
		return fmt.Errorf("no skill called %s", name)
	}
	p := skillDir(name)
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// its folder is gone already: only the entry goes
	case err == nil && (fi.Mode()&fs.ModeSymlink != 0 || linked(p)): // a junction too, as the page says
		if err := os.Remove(p); err != nil { // a folder of the user's: only the link goes
			return err
		}
	default:
		aside := filepath.Join(BackupDir(), stamp, "skills", name)
		if err := os.MkdirAll(filepath.Dir(aside), 0o700); err != nil {
			return err
		}
		if err := move(p, aside); err != nil {
			return err
		}
	}
	l.Skills = slices.Delete(l.Skills, i, i+1)
	forgetCheck(name)
	for _, p := range l.Projects {
		delete(p.Skills, name)
	}
	return nil
}

// UseLibrarySkill settles an agent's own skill that stands in the way of
// the library's by that name for the library's: the agent's folder is kept
// aside with the backups, and the library's linked in its place.
func UseLibrarySkill(name, agentID string) (*Result, error) {
	return change(func(l *Library) error {
		s, t, err := ownSkill(l, name, agentID)
		if err != nil {
			return err
		}
		p := filepath.Join(t.Skills, name)
		if _, err := os.Lstat(p); err == nil && !ours(p, name) && realDir(p) != realDir(skillDir(name)) {
			if _, err := setAside(t.Agent.ID, p); err != nil {
				return err
			}
		}
		if !slices.Contains(s.Agents, t.Agent.ID) && !slices.ContainsFunc(sharing(t), func(o string) bool { return slices.Contains(s.Agents, o) }) {
			s.Agents = set(s.Agents, t.Agent.ID, true)
		}
		return nil
	})
}

// KeepAgentSkill settles it the other way: the agent keeps its own, and
// the library no longer gives it that skill (nor any agent that reads the
// very same folder).
func KeepAgentSkill(name, agentID string) (*Result, error) {
	return change(func(l *Library) error {
		s, t, err := ownSkill(l, name, agentID)
		if err != nil {
			return err
		}
		for _, id := range append(sharing(t), t.Agent.ID) {
			s.Agents = set(s.Agents, id, false)
		}
		return nil
	})
}

func ownSkill(l *Library, name, agentID string) (*Skill, *Target, error) {
	s := l.skill(name)
	if s == nil {
		return nil, nil, fmt.Errorf("no skill called %s", name)
	}
	id, err := Takes(agentID, "skills")
	if err != nil {
		return nil, nil, err
	}
	t := targetByID(id)
	if t == nil || t.Skills == "" {
		return nil, nil, fmt.Errorf("%s has no skills folder magpie knows of", agentID)
	}
	return s, t, nil
}

// sharing are the other agents whose skills folder is the very same as t's.
func sharing(t *Target) []string {
	var out []string
	for _, o := range Targets() {
		if o.Agent.ID != t.Agent.ID && o.Skills != "" && realDir(o.Skills) == realDir(t.Skills) {
			out = append(out, o.Agent.ID)
		}
	}
	return out
}

// ---- skills the agents have of their own ----------------------------------

// FoundSkill is a skill an agent has that the library doesn't.
type FoundSkill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Agents      []string `json:"agents"`           // the agents that have this very folder
	Others      []string `json:"others,omitempty"` // agents with another by that name
	// Copies are the agents with another folder by that name holding the
	// very same files (CC Switch copies a skill into each agent): brought
	// in, they get the library's in its place
	Copies []string `json:"copies,omitempty"`
	Link   string   `json:"link,omitempty"` // where it really is, when it's a link
	// Shared is its entry in the user-wide ~/.agents/skills, when it is
	// there; the agents that have it are those whose entry is a link (or a
	// junction) to the very same folder
	Shared string `json:"shared,omitempty"`
	// Library is its folder in the library's own skills folder, put there
	// by hand (or left by a library.json that lost it): brought in, it is
	// listed where it is (#595)
	Library string `json:"library,omitempty"`
	real    string
	at      string // the entry in the first agent's folder
}

func foundSkills(l *Library) []FoundSkill {
	var out []*FoundSkill
	byName := map[string]*FoundSkill{}
	// the library's own folder first, for what it holds that the library
	// doesn't list, then the shared folder, so a skill there is the row the
	// agents' links to it join
	type place struct {
		dir, agent string
		lib        bool
	}
	places := []place{{dir: skillsDir(), lib: true}, {dir: sharedSkillsDir()}}
	for _, t := range Targets() {
		// Claude Desktop's are its own, built in or made there, kept in its
		// manifest: one brought in would leave it a link it can't read from
		if t.Skills != "" && t.Desktop == nil {
			places = append(places, place{dir: t.Skills, agent: t.Agent.ID})
		}
	}
	for _, pl := range places {
		es, _ := os.ReadDir(pl.dir)
		for _, e := range es {
			name := e.Name()
			p := filepath.Join(pl.dir, name)
			f := byName[name]
			// magpie's link to the library's folder is an agent having the
			// one found there
			if strings.HasPrefix(name, ".") || ours(p, name) && (f == nil || f.Library == "") || l.skill(name) != nil || !nameRe.MatchString(name) {
				continue
			}
			m, ok := readMeta(p)
			if !ok {
				continue
			}
			r := realDir(p)
			switch {
			case f == nil:
				f = &FoundSkill{Name: name, Description: m.Description, Agents: []string{}, real: r, at: p}
				switch {
				case pl.lib:
					f.Library = p
				case pl.agent == "":
					f.Shared = p
				default:
					f.Agents = append(f.Agents, pl.agent)
				}
				if linked(p) {
					f.Link = r
				}
				byName[name] = f
				out = append(out, f)
			case f.real == r:
				if pl.agent == "" {
					f.Shared = p
				} else if !slices.Contains(f.Agents, pl.agent) {
					f.Agents = append(f.Agents, pl.agent)
				}
			case pl.agent == "":
				// another by that name in the shared folder than the one in
				// the library's: no agent's, and left as it is
			case sameTree(p, f.at):
				f.Copies = append(f.Copies, pl.agent)
			default:
				f.Others = append(f.Others, pl.agent)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	res := []FoundSkill{}
	for _, f := range out {
		res = append(res, *f)
	}
	return res
}

// ImportSkill takes a skill the agents have of their own into the library:
// a folder is moved in, a link to a folder elsewhere is linked to from the
// library, and the agents that had it get the library's from then on.
func ImportSkill(name string) (*Result, error) {
	return change(func(l *Library) error { return importSkill(l, foundSkills(l), name) })
}

// ImportSkills is ImportSkill for several skills at once, written to the
// agents once. One that can't be brought in is said in Unimported, and
// the others are brought in all the same.
func ImportSkills(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no skills to bring in")
	}
	var failed []Problem
	res, err := change(func(l *Library) error {
		found := foundSkills(l)
		for _, name := range names {
			if err := importSkill(l, found, name); err != nil {
				failed = append(failed, Problem{What: "skill:" + name, Error: err.Error()})
			}
		}
		if len(failed) == len(names) {
			return errors.New(failed[0].Error)
		}
		return nil
	})
	if res != nil {
		res.Unimported = failed
	}
	return res, err
}

func importSkill(l *Library, found []FoundSkill, name string) error {
	i := slices.IndexFunc(found, func(f FoundSkill) bool { return f.Name == name })
	if i < 0 {
		return fmt.Errorf("no agent has a skill called %s that the library hasn't", name)
	}
	if slices.ContainsFunc(l.Skills, func(s *Skill) bool { return s.Name == name }) {
		return fmt.Errorf("the library has a skill called %s already", name)
	}
	f := found[i]
	if err := os.MkdirAll(skillsDir(), 0o755); err != nil {
		return err
	}
	// where it came from is told before it is moved: a copy leaves its
	// .git behind, and the skills CLI's lock names it where it was
	from := (&tracer{}).folder(f.real)
	src := &Source{Kind: "folder", Dir: f.real}
	// one in the shared ~/.agents/skills (or a link into it) stays
	// there, linked to: the shared folder is the user's, never emptied
	shared := f.Shared != "" || within(f.real, realDir(sharedSkillsDir()))
	switch {
	case f.Library != "":
		// in the library's folder already (#595): listed where it is, a
		// folder of its own or a link to one elsewhere
		if !linked(f.Library) {
			src = nil
		}
	case f.Link != "" || shared:
		if err := dirLink(f.real, skillDir(name)); err != nil {
			return err
		}
	default:
		// a move that copied it all but couldn't take the whole folder away
		// (a file in it held open) has brought it in all the same: what is
		// left is set aside below, not left to stand in the library's way
		var left *leftBehind
		if err := move(f.real, skillDir(name)); err != nil && !errors.As(err, &left) {
			return err
		}
		src = nil
	}
	for _, id := range f.Agents { // links to what was moved, or to the folder elsewhere
		if t := targetByID(id); t != nil {
			p := filepath.Join(t.Skills, name)
			if linked(p) {
				os.Remove(p)
			} else if _, err := os.Lstat(p); err == nil && src == nil && realDir(p) != realDir(skillDir(name)) {
				setAside(id, p)
			}
		}
	}
	// an agent with a copy of the very same gets the library's in its
	// place (its copy kept aside), as the agents with the folder itself do
	agents := slices.Concat(f.Agents, f.Copies)
	slices.Sort(agents)
	l.Skills = append(l.Skills, &Skill{Name: name, Source: src, Agents: slices.Compact(agents), From: from})
	return nil
}

// ---- files ----------------------------------------------------------------

func copyDir(from, to string) error {
	// onto the folder itself, or into it, a copy would empty each file as it
	// went (O_TRUNC) or never end (#595)
	if src, dst := realDir(from), realBelow(to); src == dst || within(dst, src) {
		return fmt.Errorf("can't copy %s into itself", from)
	}
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			if d.Name() == ".git" && p != from {
				return filepath.SkipDir
			}
			return os.MkdirAll(dst, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if t, err := os.Readlink(p); err == nil {
				return os.Symlink(t, dst)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		fi, _ := d.Info()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// move renames, or copies and removes where a rename can't cross disks.
func move(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	} else if errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := copyDir(from, to); err != nil {
		os.RemoveAll(to)
		return err
	}
	if err := os.RemoveAll(from); err != nil {
		return &leftBehind{err}
	}
	return nil
}

// leftBehind is a move that copied everything but couldn't take all of
// what it moved away (a file in it open, on Windows): the copy is whole.
type leftBehind struct{ err error }

func (e *leftBehind) Error() string { return e.err.Error() }
func (e *leftBehind) Unwrap() error { return e.err }

// setAside moves an agent's own skill (a folder, or a link) out of its
// way, into a backup of its own: nothing of the user's is just deleted.
func setAside(agent, p string) (string, error) {
	dst := filepath.Join(BackupDir(), time.Now().Format("2006-01-02_15-04-05.000"), fileName(agent), "skills", filepath.Base(p))
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dst = filepath.Join(filepath.Dir(dst), fmt.Sprintf("%s-%d", filepath.Base(p), i))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if err := move(p, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// sameTree is whether two skill folders (links followed) hold the same
// files with the same bytes, leaving aside what Finder or version control
// keeps in one (.DS_Store, .git) and the mark on magpie's own copy.
func sameTree(a, b string) bool {
	a, b = realDir(a), realDir(b)
	fa, okA := treeOf(a)
	fb, okB := treeOf(b)
	if !okA || !okB || len(fa) != len(fb) {
		return false
	}
	for rel, x := range fa {
		y, ok := fb[rel]
		if !ok || x != y {
			return false
		}
		if x.link == "" && !sameFile(filepath.Join(a, rel), filepath.Join(b, rel)) {
			return false
		}
	}
	return true
}

type treeEntry struct {
	size int64
	link string // where a link in it points
}

// treeOf is a folder's files and links by where they are in it; not ok
// when it can't be read, or is too big to be worth comparing.
func treeOf(root string) (map[string]treeEntry, bool) {
	out := map[string]treeEntry{}
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			if !d.IsDir() {
				return errors.New("not a folder")
			}
			return nil
		}
		if n := d.Name(); n == ".DS_Store" || n == marker || n == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.Type()&fs.ModeSymlink != 0 {
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = treeEntry{link: to}
		} else if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			out[rel] = treeEntry{size: fi.Size()}
			total += fi.Size()
		}
		if len(out) > 20000 || total > 256<<20 {
			return errors.New("too big to compare")
		}
		return nil
	})
	return out, err == nil
}

func sameFile(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil || eb != nil {
			return ea == eb || (ea == io.EOF || ea == io.ErrUnexpectedEOF) && (eb == io.EOF || eb == io.ErrUnexpectedEOF)
		}
	}
}

// SkillText is a library skill's SKILL.md, for the page to show.
func SkillText(name string) (string, error) {
	if err := checkName("skill", name); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(skillDir(name), "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("%s has no SKILL.md in the library", name)
	}
	return string(b), nil
}

// SkillPath is where a library skill's folder is.
func SkillPath(name string) string { return skillDir(name) }
