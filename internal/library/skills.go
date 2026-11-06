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
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/agent"
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
	// changed the skill since. For one from another git repository,
	// Commit is the commit of the repository it was fetched at.
	Hash   string `json:"hash,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Own is Hash leaving aside what installing the skill's dependencies
	// writes (ownHash): set as it is fetched, so a lock file the install
	// writes again isn't taken for a change of the user's
	Own string `json:"own,omitempty"`
	// From is the repository one not installed from GitHub was found to
	// come from (skill_from.go), only to group it with the rest of it
	From string `json:"from,omitempty"`
	// Gone is when an update or a check found its source without it: it
	// was removed there (skill_gone.go). Kept on this computer only.
	Gone string `json:"gone,omitempty"`
	// OffHere turns the skill off on this computer alone (hanslin1666 on
	// Discord): no agent here gets it, whatever Agents says. Agents is
	// what a sync carries and stays as the other computers have it; this
	// is never carried (Collect leaves it out) and a sync keeps it (Put).
	OffHere bool `json:"offHere,omitempty"`
	// Called is the name its SKILL.md gives, when the library keeps it
	// under another because a skill from elsewhere had that name first
	// (skill_beside.go). Its SKILL.md is named as the library names it.
	Called string `json:"called,omitempty"`
	// Note is the user's own word on what the skill is for or when to
	// use it, beside its SKILL.md's description (youyo.p on Discord).
	// Only the library keeps it, and a sync carries it; no agent is
	// given it: the skill's folder is the agents', and this isn't in it.
	Note string `json:"note,omitempty"`
}

// gives reports whether the agent id gets the skill on this computer: it
// is ticked for it, and the skill isn't turned off here.
func (s *Skill) gives(id string) bool {
	return s != nil && !s.OffHere && slices.Contains(s.Agents, id)
}

// SkillHere turns a skill on or off on this computer alone. Which agents
// it is ticked for stays as it is, and so does every other computer's.
func SkillHere(name string, on bool) (*Result, error) {
	return change(func(l *Library) error {
		s := l.skill(name)
		if s == nil {
			return fmt.Errorf("no skill called %s", name)
		}
		s.OffHere = !on
		return nil
	})
}

// maxNote is how long a note on a server or a skill can be, in
// characters: a line that says what it is for, as a hook's description.
const maxNote = 200

// checkNote makes a note one line, as the page shows it, and holds it to
// maxNote.
func checkNote(n *string) error {
	*n = strings.Join(strings.Fields(*n), " ")
	if utf8.RuneCountInString(*n) > maxNote {
		return fmt.Errorf("a note is at most %d characters", maxNote)
	}
	return nil
}

// SkillNote sets the user's note on a skill; "" takes it away.
func SkillNote(name, note string) (*Result, error) {
	if err := checkNote(&note); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		s := l.skill(name)
		if s == nil {
			return fmt.Errorf("no skill called %s", name)
		}
		s.Note = note
		return nil
	})
}

// Source is where a skill came from: a GitHub repository it can be updated
// from, another git repository (fetched with the user's own git), or a
// folder on this machine the library links to, so that editing the folder
// is editing the skill.
type Source struct {
	Kind string `json:"kind"`           // github | git | folder
	Repo string `json:"repo,omitempty"` // owner/repo for github, the address git fetches for git
	Ref  string `json:"ref,omitempty"`
	Path string `json:"path,omitempty"` // in the repository
	Dir  string `json:"dir,omitempty"`  // the folder, for kind folder
	// Via is the app's own steady link a folder skill was linked through
	// while that link leads nowhere and Dir is the version folder found in
	// its place (skill_moved.go); the library goes back to it once it
	// leads to the skill again
	Via string `json:"via,omitempty"`
}

func (s *Source) String() string {
	if s == nil {
		return ""
	}
	if s.Kind == "folder" {
		return s.Dir
	}
	if s.Kind == "git" {
		return gitURL(s)
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

// nameFor is the name a skill in the folder named folder goes by: what its
// SKILL.md says, made fit for a file name. A name in another script doesn't
// survive that: asdfgh1445/ctf-super-hub's "CTF•密码学" and ten more were
// all "CTF", "Strix•RCE 测试" and ten more "Strix", and "头脑风暴" nothing,
// so 35 skills were listed as 16 (tnt1223 on Discord). Such a name, or
// none, goes by its folder's name instead, which a repository keeps apart;
// only a folder whose name is no name either leaves the name as it came
// out.
func nameFor(folder string, m meta) string {
	if n := fitName(m.Name); n != "" && !dropsLetters(m.Name) {
		return n
	}
	if n := fitName(folder); n != "" {
		return n
	}
	return fitName(m.Name)
}

// srcFolder is the name of the folder a source's top is: a repository's
// own name, not the temporary folder it was downloaded to.
func srcFolder(src Source) string {
	if src.Kind == "folder" {
		return filepath.Base(src.Dir)
	}
	r := strings.TrimSuffix(strings.TrimRight(src.Repo, "/"), ".git")
	return r[strings.LastIndexAny(r, "/:")+1:]
}

// fitName is a name made fit for a file name: anything but ASCII letters,
// digits, _ and - is a -, and it is at most 64 bytes.
func fitName(n string) string {
	n = strings.Trim(unsafe.ReplaceAllString(n, "-"), "-_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// dropsLetters is whether making n fit for a file name loses a letter or
// digit of it, not only a space or a mark between words.
func dropsLetters(n string) bool {
	return strings.ContainsFunc(n, func(r rune) bool {
		return r > unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))
	})
}

// ---- linking into agents --------------------------------------------------

// marker is left in a copy magpie makes where it can't link (Windows
// without the right to), to know the copy for its own.
const marker = ".magpie-library"

// ours reports whether the entry at p is magpie's link (or copy) of the
// library's skill by that name.
func ours(p, name string) bool {
	// a link in a WSL distro is read there (linkTarget), Windows can't;
	// one to magpie's copy in the distro is magpie's (wsl_skills.go)
	if to, ok := linkTarget(p); ok {
		return filepath.Clean(to) == filepath.Clean(skillDir(name)) || inWSLStore(to, name)
	}
	fi, err := os.Lstat(p)
	if err != nil || linkEntry(fi) {
		return false
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

// copyIsFresh is whether the copy at p holds the skill hashed as lib.
// A mark matching lib, with nothing changed since it was made, establishes
// that without reading the files (#1031). A copy takeEdits found unchanged
// (untouched) isn't walked again. An incomplete scan returns an error.
func copyIsFresh(p, lib string, untouched bool) (bool, error) {
	var at time.Time
	if !untouched {
		var err error
		at, err = changedAt(p)
		if err != nil {
			return false, err
		}
	}
	if lib == "" {
		return false, nil
	}
	if h, ok := markedHash(p); ok {
		if untouched {
			return h == lib, nil
		}
		if made, ok := copiedAt(p); ok && !at.After(made) {
			return h == lib, nil
		}
	}
	h := hashDir(p)
	return h != "" && h == lib, nil
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
		err := os.Remove(p)
		// a link a WSL distro has can't be removed from Windows: the
		// distro removes it
		if err != nil {
			if _, ok := wslReadlink(p); ok {
				return agent.WSLRemoveLink(p)
			}
		}
		return err
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
		// one look at each part: whether it is there, and where it points
		// when it is a link (#1771)
		e := lookPart(next)
		if e.link {
			to := e.to
			if !filepath.IsAbs(to) {
				to = filepath.Join(filepath.Dir(next), to)
			}
			return resolve(filepath.Join(append([]string{to}, parts[i+1:]...)...), depth+1)
		}
		if !e.there {
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
	e := lookPart(p)
	return e.to, e.link
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
// ~/.agents/skills, so every agent that reads the shared folder has it,
// whatever the library gives (sharedEntry).
func sharedHas(name string) bool { return sharedEntry(name) != "" }

// sharedEntry is the user's own entry in ~/.agents/skills (not a link
// magpie made) by which the agents reading that folder find the library's
// skill by that name: the very folder the library's is or links to. That
// is the entry by its name, or, for a library skill linked to a folder
// right in the shared one under another name, that entry. "" when none.
// HandOverSkills moves such a skill out of it (skill_handover.go).
func sharedEntry(name string) string {
	lib := skillDir(name)
	if _, err := os.Stat(lib); err != nil {
		return ""
	}
	r := realDir(lib)
	p := filepath.Join(sharedSkillsDir(), name)
	if _, err := os.Lstat(p); err == nil && !ours(p, name) && realDir(p) == r {
		return p
	}
	sh := realDir(sharedSkillsDir())
	if rel, err := filepath.Rel(sh, r); err == nil && within(r, sh) && rel != "." && !strings.ContainsRune(rel, filepath.Separator) {
		if q := filepath.Join(sharedSkillsDir(), rel); !ours(q, rel) && realDir(q) == r {
			return q
		}
	}
	return ""
}

// sharedCopy is whether p, the entry in ~/.agents/skills by a library
// skill's name, is the user's own copy of the library's very files: not
// one magpie made (ours), not the library's own folder (sharedEntry), and
// holding what the library's holds.
func sharedCopy(p, name string) bool {
	if _, err := os.Lstat(p); err != nil || ours(p, name) {
		return false
	}
	lib := skillDir(name)
	if _, err := os.Stat(lib); err != nil || realDir(p) == realDir(lib) {
		return false
	}
	return sameTree(p, lib)
}

// within is whether p is dir or inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (l *Library) syncSkills(t *Target, res *Result, all []*Target) {
	if res.unreached(t) {
		return
	}
	if t.Skills == "" {
		return
	}
	id := t.Agent.ID
	// LiveAgent uses a skill in its folder only once it is picked there
	defer l.syncLiveAgentPicks(t, res)
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
		// the folder is ~/.agents/skills itself: what the setting puts
		// there (skills_shared.go) stays
		if l.keepsShared(s) && realDir(t.Skills) == realDir(sharedSkillsDir()) {
			return true
		}
		return s.gives(id) || slices.ContainsFunc(sharers, s.gives)
	}
	// an agent that reads ~/.agents/skills as well as its own folder finds
	// a skill magpie put there for another (Kimi Code) already: a link in
	// its own too would be the skill twice
	shared := realDir(sharedSkillsDir())
	isShared := realDir(t.Skills) == shared
	inShared := func(s *Skill) bool {
		if s == nil || !t.readsSharedDir() || realDir(t.Skills) == shared {
			return false
		}
		// the library's skill is kept in the shared folder itself (brought
		// in from there): every agent reading it has it, ticked or not (#595)
		if sharedHas(s.Name) || l.inSharedHere(s) {
			return true
		}
		p := filepath.Join(sharedSkillsDir(), s.Name)
		if slices.ContainsFunc(all, func(o *Target) bool {
			return o.Skills != "" && realDir(o.Skills) == shared && s.gives(o.Agent.ID)
		}) && (ours(p, s.Name) || realDir(p) == realDir(skillDir(s.Name))) {
			return true
		}
		// the user's own copy of the very same files kept in the shared
		// folder (another installer's, or one copied by hand): the agent
		// finds it there, and a link or copy of the library's in its own
		// folder too is a second tree by that name. pi loads both and
		// warns of a collision, since it drops a skill read twice only
		// when both paths lead to one file (realpath, dist/core/skills.js)
		return sharedCopy(p, s.Name)
	}
	var mine []string
	for _, name := range a.Skills {
		s := l.skill(name)
		p := filepath.Join(t.Skills, name)
		if l.blockedSkills[realDir(p)] {
			mine = append(mine, name)
			continue
		}
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
		if !s.gives(id) {
			continue
		}
		// an agent gone from here is given nothing: what it has still
		// stays its, until unticked
		if t.Gone {
			if slices.Contains(a.Skills, s.Name) && !slices.Contains(mine, s.Name) {
				mine = append(mine, s.Name)
			}
			continue
		}
		p := filepath.Join(t.Skills, s.Name)
		if l.blockedSkills[realDir(p)] {
			if !slices.Contains(mine, s.Name) {
				mine = append(mine, s.Name)
			}
			continue
		}
		// in a WSL distro, magpie's one copy there is what the agent's
		// link leads to (wsl_skills.go): made, or made again, first
		if t.Store != "" && !cp {
			if made, err := l.storeSkill(t, s.Name); err != nil {
				res.fail(id, "skill:"+s.Name, err)
				if ours(p, s.Name) && !slices.Contains(mine, s.Name) {
					mine = append(mine, s.Name)
				}
				continue
			} else if made && ours(p, s.Name) && linked(p) {
				// what its link leads to is new
				res.changed(id)
			}
		}
		// kept in ~/.agents/skills under another name (sharedEntry): the
		// agent whose folder that is has it there already, and a link by
		// this name too would be the skill twice for every reader of it
		if isShared && !ours(p, s.Name) {
			if e := sharedEntry(s.Name); e != "" && e != p {
				continue
			}
		}
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
			mine = append(mine, s.Name)
			// an old copy a previous swap couldn't remove is tried again
			if ours(p, s.Name) {
				dropOld(p, s.Name)
			}
			// magpie's copy in an agent given links now is a link again
			// (#896), unless no link can be made here (Windows without the
			// right to), where the copy stays
			if ours(p, s.Name) && !cp && !linked(p) {
				rl := relink
				if t.Store != "" {
					rl = l.wslRelink(t)
				}
				if ok, err := rl(id, p, s.Name); err != nil {
					res.fail(id, "skill:"+s.Name, err)
					continue
				} else if ok {
					res.changed(id)
				}
			}
			// a copy is made again once the library's skill has changed: in
			// an agent that takes copies, and where magpie couldn't link
			// (Windows without the right to) and left a copy instead
			again := cp && linked(p)
			if ours(p, s.Name) && !linked(p) && l.libHash(s.Name) != "" {
				current, err := copyIsFresh(p, l.libHash(s.Name), l.untouched[p])
				if err != nil {
					res.fail(id, "skill:"+s.Name, err)
					continue
				}
				again = !current
			}
			if ours(p, s.Name) && again {
				if err := copyIn(p, s.Name); err != nil {
					res.fail(id, "skill:"+s.Name, err)
				} else {
					res.changed(id)
				}
			}
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
		} else if t.Store != "" {
			put = func(p, name string) error { return agent.WSLSymlink(t.Agent, filepath.Join(t.Store, name), p) }
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

// sweepShared takes magpie's own entries out of ~/.agents/skills once no
// agent whose folder it is, or that still reads it for the skill, has it
// any more: an entry put there for an agent that reads its own folder now
// (Kimi Code, #4379) would go on reaching every reader of the folder,
// whatever the library ticks.
func (l *Library) sweepShared(all []*Target, res *Result) {
	es, err := os.ReadDir(sharedSkillsDir())
	if err != nil {
		return
	}
	shared := realDir(sharedSkillsDir())
	var holders []*Target
	for _, o := range all {
		if o.Skills != "" && realDir(o.Skills) == shared {
			holders = append(holders, o)
		}
	}
	for _, e := range es {
		name := e.Name()
		p := filepath.Join(sharedSkillsDir(), name)
		// the user's own entries stay, and so does a copy takeEdits
		// couldn't scan, held in place for its edits
		if !ours(p, name) || l.blockedSkills[realDir(p)] {
			continue
		}
		s := l.skill(name)
		// one the setting keeps there stays (skills_shared.go)
		if l.keepsShared(s) || slices.ContainsFunc(holders, func(o *Target) bool { return s.gives(o.Agent.ID) }) {
			continue
		}
		if err := unlink(p); err != nil {
			res.fail("agents", "skill:"+name, err)
			continue
		}
		dropOld(p, name)
	}
}

// ---- finding skills to install --------------------------------------------

// Candidate is a skill found where the user pointed: in a repository, or
// in a folder.
type Candidate struct {
	Path        string `json:"path"` // in the repository or the folder; "" for its root
	Name        string `json:"name"`
	Description string `json:"description"`
	Have        bool   `json:"have"` // the library has this skill (from here, or the very same files)
	// As is the name the library has it by, or would add it by, when
	// that isn't Name: another skill from elsewhere has that name
	As string `json:"as,omitempty"`
}

// Probe is what was found at a source.
type Probe struct {
	Source     string      `json:"source"`
	Kind       string      `json:"kind"`
	Candidates []Candidate `json:"candidates"`
	// Left is the skills there too big to install on their own, left out
	// of the candidates (#2062)
	Left []LeftOut `json:"left,omitempty"`
	root string    // the folder the candidates' paths are in
	src  Source
}

var probes = struct {
	sync.Mutex
	m map[string]*probeEntry
}{m: map[string]*probeEntry{}}

type probeEntry struct {
	p     *Probe
	tmp   string
	timer *time.Timer // removes it when probeLife is up
}

// probeLife is how long a probe's download is kept for an install from it.
var probeLife = 15 * time.Minute

// skillTemp starts the name of a download's folder in the temp dir.
const skillTemp = "magpie-skill-"

// keepProbe caches p's download under input for probeLife, in place of
// one before it; the folder is removed when it is replaced or expires, not
// only at the next probe, which may never come (#2321).
func keepProbe(input string, p *Probe, tmp string) {
	e := &probeEntry{p: p, tmp: tmp}
	probes.Lock()
	old := probes.m[input]
	probes.m[input] = e
	e.timer = time.AfterFunc(probeLife, func() {
		probes.Lock()
		if probes.m[input] == e {
			delete(probes.m, input)
		}
		probes.Unlock()
		os.RemoveAll(tmp)
	})
	probes.Unlock()
	if old != nil {
		old.timer.Stop()
		os.RemoveAll(old.tmp)
	}
}

// sweepStaleDownloads removes the downloads a magpie that quit left in the
// temp dir (#2321): a folder of ours over an hour old, longer than a probe
// is kept or a fetch takes, so none in use.
func sweepStaleDownloads() {
	dir := os.TempDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), skillTemp) {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Hour {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
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

// remote is whether a skill's source is a repository it is fetched from
// again to update: GitHub, or another git repository.
func (s *Source) remote() bool { return s != nil && (s.Kind == "github" || s.Kind == "git") }

// fetch downloads the repository into a new folder and gives it back; one
// not on GitHub is fetched with the user's git (fetchGit). A
// repository codeload doesn't hand out without a token, a private one, is
// asked of GitHub's API with the user's GitHub token (37FlowAI on X): the
// API redirects to codeload, and the token, sent to the API's host alone,
// isn't carried over to it.
//
// A repository too big to download whole is fetched only as far as src.Path
// and name narrow it (fetchSkillFiles); part says it was.
func fetch(src Source) (string, error) {
	dir, _, err := fetchPart(src, "")
	return dir, err
}

func fetchPart(src Source, name string) (dir string, part bool, err error) {
	if src.Kind == "git" {
		dir, err = fetchGit(src)
		return dir, false, err
	}
	dir, err = fetchWhole(src, name)
	return dir, tooBig.has(src) && (src.Path != "" || name != ""), err
}

func fetchWhole(src Source, name string) (string, error) {
	c := &http.Client{Timeout: 2 * time.Minute}
	get := func(u string) (*http.Response, bool, error) {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "magpie")
		return doGitHub(req, func(r *http.Request) (*http.Response, error) { return source.DoOfficial(c, r) })
	}
	if tooBig.has(src) {
		return fetchSkillFiles(src, name)
	}
	resp, _, err := get(tarballURL(src.Repo, src.Ref))
	if err != nil {
		return "", fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	token := false
	if resp.StatusCode == 404 {
		if t, _ := GitHubToken(); t != "" {
			resp.Body.Close()
			ref := ""
			if src.Ref != "" {
				ref = "/" + url.PathEscape(src.Ref)
			}
			if resp, token, err = get(githubAPI + "/repos/" + src.Repo + "/tarball" + ref); err != nil {
				return "", fmt.Errorf("couldn't reach GitHub: %w", err)
			}
		}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 401 && token:
		what := "repository " + src.Repo
		if src.Ref != "" {
			what = src.Repo + " at " + src.Ref
		}
		if !token {
			return "", fmt.Errorf("GitHub has no %s (a private repository installs with a GitHub token that can read it, set in Settings → Network and sharing)", what)
		}
		_, from := GitHubToken()
		where := "the GitHub token in Settings → Network and sharing"
		if from != "settings" {
			where = from
		}
		return "", fmt.Errorf("GitHub has no %s that %s can read", what, where)
	case resp.StatusCode == 403 || resp.StatusCode == 429:
		return "", fmt.Errorf("GitHub is limiting requests from here; try again in a while")
	case resp.StatusCode != 200:
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	tmp, err := os.MkdirTemp("", skillTemp)
	if err != nil {
		return "", err
	}
	if err := untar(resp.Body, tmp); err != nil {
		os.RemoveAll(tmp)
		if errors.Is(err, errTooBig) {
			// a repository that is mostly something else (an app with its
			// skills beside it, #1941): only the skills' folders
			resp.Body.Close()
			tooBig.add(src)
			return fetchSkillFiles(src, name)
		}
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
			if total += h.Size; total > maxFetch {
				return errTooBig
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
// looked for a few folders deep, and not inside another skill — beside
// the skills of a collection: a folder that holds a SKILL.md and several
// skills under it is a collection with a router (#4572), and lists the
// skills under it beside it. A hidden
// folder is an agent's own (.claude, .agents) and is looked in only for its
// skills folder: some repositories keep their skill only in
// .claude/skills (#1964, UFO404). A skill the repository holds twice, in a
// hidden folder or a plugin's, is listed once (unhiddenFirst).
func candidates(root, sub, top string) []Candidate {
	base := filepath.Join(root, filepath.FromSlash(sub))
	var out []Candidate
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if m, ok := readMeta(dir); ok {
			rel, _ := filepath.Rel(root, dir)
			folder := filepath.Base(dir)
			if rel == "." {
				rel, folder = "", top
			}
			out = append(out, Candidate{Path: filepath.ToSlash(rel), Name: nameFor(folder, m), Description: m.Description})
			if skillsBeside(dir) < 2 {
				return
			}
		}
		if depth == 0 {
			return
		}
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			n := e.Name()
			if n == "node_modules" {
				continue
			}
			p, d := filepath.Join(dir, n), depth-1
			if strings.HasPrefix(n, ".") {
				if d == 0 {
					continue
				}
				p, d = filepath.Join(p, "skills"), d-1
			}
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				walk(p, d)
			}
		}
	}
	walk(base, 4)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return unhiddenFirst(out, func(c Candidate) string { return c.Path }, func(a, b Candidate) bool {
		return a.Name == b.Name || lastPart(a.Path) == lastPart(b.Path)
	})
}

// skillsBeside is how many folders directly under dir hold a SKILL.md of
// their own. A folder that is a skill and holds several skills beside its
// own files is a collection with a router (#4572): the router is listed,
// and so are the skills under it. A lone SKILL.md inside another skill
// (dbs-save/inner in dontbesilent2025/dbskill) is none of its own, as
// before.
func skillsBeside(dir string) int {
	es, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range es {
		if !e.IsDir() || e.Name() == "node_modules" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, ok := readMeta(filepath.Join(dir, e.Name())); ok {
			if n++; n > 1 {
				return n
			}
		}
	}
	return n
}

// inHidden is whether a skill's path, from the repository's top, goes
// through a hidden folder.
func inHidden(path string) bool {
	return slices.ContainsFunc(strings.Split(path, "/"), func(p string) bool { return strings.HasPrefix(p, ".") })
}

// unhiddenFirst lists each skill once: of copies of the same skill it keeps
// the one outside every hidden folder, then the one fewest folders deep,
// then the first. An agent's folder often holds a copy of the repository's
// own skills, and a repository that is also a plugin often holds another
// under its plugin's folder: tw93/waza has skills/check and
// plugins/waza/skills/check, and every skill was listed twice (碳碳双键 on
// Discord).
func unhiddenFirst[T any](list []T, path func(T) string, same func(a, b T) bool) []T {
	depth := func(c T) int { return strings.Count(path(c), "/") }
	before := func(o T, j int, c T, i int) bool {
		if ho, hc := inHidden(path(o)), inHidden(path(c)); ho != hc {
			return !ho
		}
		if do, dc := depth(o), depth(c); do != dc {
			return do < dc
		}
		return j < i
	}
	out := make([]T, 0, len(list))
	for i, c := range list {
		dup := false
		for j, o := range list {
			if j != i && same(o, c) && before(o, j, c, i) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

// skillsCommand is the source a skills CLI command copied from a page
// installs from (RockyShowlle on X): "npx skills add owner/repo --skill
// x", run through npx, bunx, pnpm dlx and the like. Anything else is
// given back as it is.
func skillsCommand(input string) string {
	f := strings.Fields(strings.TrimSpace(input))
	i := 0
	switch {
	case len(f) > 1 && (f[0] == "npx" || f[0] == "bunx" || f[0] == "pnpx"):
		i = 1
	case len(f) > 2 && ((f[0] == "pnpm" || f[0] == "yarn") && f[1] == "dlx" || f[0] == "bun" && f[1] == "x"):
		i = 2
	default:
		return input
	}
	for i < len(f) && strings.HasPrefix(f[i], "-") { // npx -y
		i++
	}
	if i+1 >= len(f) || (f[i] != "skills" && !strings.HasPrefix(f[i], "skills@")) || f[i+1] != "add" {
		return input
	}
	for i += 2; i < len(f); i++ {
		switch a := f[i]; {
		case a == "--skill" || a == "-s" || a == "--agent" || a == "-a":
			i++ // and its value
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return input
}

// ProbeSkills looks for skills at what the user typed: a GitHub repository
// (or a folder in one), any other git repository (GitLab, one on a
// company's own server; fetched with the user's git), a folder on this
// machine, or a skills CLI command for one of them.
func ProbeSkills(input string) (*Probe, error) { return probeSkills(input, "") }

// probeSkills is ProbeSkills for the skill called name, when one is
// wanted: a repository too big to download whole is then fetched only for
// the skills by that name (fetchSkillFiles), and the probe is kept apart
// from a whole one (probeKey).
func probeSkills(input, name string) (*Probe, error) {
	input = strings.TrimSpace(skillsCommand(input))
	if input == "" {
		return nil, fmt.Errorf("paste a GitHub or git repository's link or a folder's path")
	}
	if strings.HasPrefix(input, "-") {
		return nil, fmt.Errorf("a repository's address can't start with -")
	}
	sweepStaleDownloads()

	p := &Probe{Source: input}
	if dir := expand(input); filepath.IsAbs(dir) {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s isn't a folder", input)
		}
		p.Kind, p.root, p.src = "folder", dir, Source{Kind: "folder", Dir: dir}
		p.Candidates = candidates(dir, "", filepath.Base(dir))
	} else if src, ok := githubSource(input); ok || isGit(input) {
		if !ok {
			src, _, _ = gitSource(input)
		}
		tmp, part, err := fetchPart(src, name)
		if err != nil {
			return nil, err
		}
		p.Kind, p.root, p.src = src.Kind, tmp, src
		if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(src.Path))); src.Path != "" && err != nil {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("%s has no folder %s", src.Repo, src.Path)
		}
		p.Candidates = candidates(tmp, src.Path, srcFolder(src))
		p.Left = tookLeftOut(tmp, src.Path)
		key := input
		if part && name != "" {
			key = probeKey(input, name)
		}
		keepProbe(key, p, tmp)
	} else if _, ok, err := gitSource(input); ok && err != nil {
		return nil, err
	} else {
		return nil, fmt.Errorf("that's neither a git repository's link (GitHub, GitLab or another host) nor a folder's full path")
	}
	if p.Candidates == nil {
		p.Candidates = []Candidate{}
	}
	if l, err := load(); err == nil {
		for i, c := range p.Candidates {
			from := filepath.Join(p.root, filepath.FromSlash(c.Path))
			if s := l.hadSkill(p.src, c, from); s != nil {
				p.Candidates[i].Have = true
				if s.Name != c.Name {
					p.Candidates[i].As = s.Name
				}
			} else if l.skill(c.Name) != nil {
				if p.src.remote() {
					p.Candidates[i].As = l.freeSkillName(c.Name, p.src)
				} else {
					// a folder of the user's isn't added beside it
					// (installFrom): shown as had by that name, as before
					p.Candidates[i].Have = true
				}
			}
		}
	}
	noteFrom(p.src, p.root, p.Candidates)
	return p, nil
}

func cachedProbe(input string) (*Probe, error) { return cachedProbeFor(input, "") }

// probeKey is where a probe fetched only for the skill called name is
// kept: a later probe of the whole source never takes it for one.
func probeKey(input, name string) string { return input + "\x00" + name }

// cachedProbeFor is the probe kept for input, a whole one or one made for
// the skill called name, or a new one for name.
func cachedProbeFor(input, name string) (*Probe, error) {
	key := strings.TrimSpace(skillsCommand(input))
	probes.Lock()
	es := []*probeEntry{probes.m[key]}
	if name != "" {
		es = append(es, probes.m[probeKey(key, name)])
	}
	probes.Unlock()
	for _, e := range es {
		if e != nil {
			if _, err := os.Stat(e.tmp); err == nil {
				return e.p, nil
			}
		}
	}
	return probeSkills(input, name)
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
	case "git":
		return sameGitRepo(s.Source.Repo, src.Repo) && s.Source.Path == path
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
		// the user has changed in it since and whatever it is called
		// here; another by that name goes in beside it, by a name of its
		// own (skill_beside.go)
		if s := l.hadSkill(src, c, from); s != nil {
			in.had = append(in.had, s.Name)
			continue
		}
		name := c.Name
		if l.skill(name) != nil {
			// the user's own folder is linked as it is, and its SKILL.md
			// can't be named apart from the other: an agent would see two
			// by one name
			if !src.remote() {
				in.skip(c.Name, fmt.Errorf("the library already has another skill called %s, left as it is: give this one another name in its SKILL.md to add it beside it", c.Name))
				continue
			}
			name = l.freeSkillName(c.Name, src)
		}
		dir := skillDir(name)
		// something by that name in the library's folder that the
		// library doesn't list is never written into: that would mix
		// two skills' files, and a failed copy would take it away. One
		// holding exactly this skill's files (an install that stopped
		// before the library was saved left it) is listed where it is;
		// another is the user's, and this one goes in beside it.
		listed := false
		if _, err := os.Lstat(dir); err == nil {
			switch {
			case src.Kind == "folder" && linked(dir) && realDir(dir) == realDir(from):
				src.Dir, listed = from, true
			case src.remote() && !linked(dir) && sameTree(dir, from):
				src.Path, listed = c.Path, true
			case src.Kind == "folder" && realDir(from) == realDir(dir):
				// a folder already in the library's own (#595): linked to
				// itself it fails, and copied onto itself every file of it
				// was emptied
				in.skip(c.Name, fmt.Errorf("%s is in the library's folder already: bring it in from the skills found there", c.Name))
				continue
			case !src.remote():
				in.skip(c.Name, fmt.Errorf("the library's folder already has a %s that isn't this one (%s), left as it is: bring it in from the skills found there to use it", c.Name, dir))
				continue
			default:
				name = l.freeSkillName(c.Name, src)
				dir = skillDir(name)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			// can't tell what is there: left alone
			in.skip(c.Name, err)
			continue
		}
		switch {
		case listed:
			in.had = append(in.had, name)
		case src.Kind == "folder":
			// the user's folder, linked: its files stay as they are, its
			// SKILL.md's name too
			src.Dir = from
			if err := dirLink(from, dir); err != nil {
				if errors.Is(err, fs.ErrExist) {
					in.skip(c.Name, fmt.Errorf("the library's folder already has a %s in it (%s)", name, dir))
					continue
				}
				if err := copyDir(from, dir); err != nil {
					os.RemoveAll(dir)
					in.skip(c.Name, err)
					continue
				}
			}
			in.added = append(in.added, name)
		default:
			src.Path = c.Path
			if err := copyDir(from, dir); err != nil {
				os.RemoveAll(dir)
				in.skip(c.Name, err)
				continue
			}
			if name != c.Name {
				if err := nameSkill(dir, name); err != nil {
					os.RemoveAll(dir)
					in.skip(c.Name, err)
					continue
				}
			}
			in.added = append(in.added, name)
		}
		sk := &Skill{Name: name, Source: &src, Agents: slices.Clone(agents)}
		if name != c.Name {
			sk.Called = c.Name
		}
		if src.Kind == "git" {
			sk.Hash, sk.Own, sk.Commit = hashDir(dir), ownHash(dir), gitHeadOf(p.root)
		}
		if src.Kind == "github" {
			sk.Hash, sk.Own = hashDir(dir), ownHash(dir)
			// every skill the picker showed was offered: a check
			// offers only those the repository adds later
			l.seenAt(src.Repo, src.Ref, c.Path)
			if shown {
				for _, c := range p.Candidates {
					l.seenAt(src.Repo, src.Ref, c.Path)
				}
			}
		}
		forgetCheck(name)
		l.Skills = append(l.Skills, sk)
	}
	return nil
}

// UpdateSkill fetches a skill from GitHub again, in place: the agents'
// links go on pointing at it. One changed here since it was fetched is
// an *EditedError unless replace is set, and then the changed version is
// kept with the backups (#1449).
func UpdateSkill(name string, replace bool) (*Result, error) {
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
	if edited, _ := l.editState(s, Targets()); edited && !replace {
		return nil, editedError(s)
	}
	up, err := f.prepare(s)
	var gone *GoneError
	if errors.As(err, &gone) {
		markGone([]string{name}, nil)
		checks.Lock()
		checks.m[name] = goneCheck(gone)
		checks.Unlock()
	}
	if err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		targets := Targets()
		if s := l.skill(name); s != nil && !replace {
			// changed while it was fetched
			if edited, _ := l.editState(s, targets); edited {
				return editedError(s)
			}
		}
		return up.apply(l, targets)
	})
}

// UpdateSkills fetches again every skill that came from GitHub, each
// repository once, and writes the agents once. A skill that couldn't be
// fetched is said in Unupdated, as is one changed here since it was
// fetched, which is left as it is; the others are updated all the same.
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
		gone   []*GoneError
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
			var g *GoneError
			if errors.As(err, &g) {
				gone = append(gone, g)
				return
			}
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
		var names []string
		checks.Lock()
		for _, g := range gone {
			names = append(names, g.Name)
			checks.m[g.Name] = goneCheck(g)
		}
		checks.Unlock()
		l.setGone(names, nil)
		targets := Targets()
		for _, up := range ups {
			if s := l.skill(up.name); s != nil {
				if edited, _ := l.editState(s, targets); edited {
					failed = append(failed, Problem{What: "skill:" + up.name, Error: editedError(s).Error()})
					continue
				}
			}
			if err := up.apply(l, targets); err != nil {
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
	for _, g := range gone {
		res.Gone = append(res.Gone, g.Name)
	}
	sort.Strings(res.Gone)
	return res, nil
}

// updatable is whether a skill can be fetched again: one from GitHub or
// another git repository, or one CC Switch or the skills CLI installed
// from GitHub.
func updatable(s *Skill) bool {
	if s.Source.remote() {
		return true
	}
	if _, ok := ccSwitchOrigin(s); ok {
		return true
	}
	_, ok := lockOrigin(s)
	return ok
}

// fetcher downloads each repository at each ref once, however many of
// its skills are updated. A repository too big to download whole is
// fetched only under the folder asked for (fetchPart), so a skill
// elsewhere in it is fetched on its own: read from the first folder, it
// would look removed there (goneFrom).
type fetcher struct {
	mu   sync.Mutex
	got  map[string]*fetched
	dirs []string
}

type fetched struct {
	once  sync.Once
	dir   string
	err   error
	part  bool   // only under
	under string // the folder fetched, when part
}

func (f *fetcher) fetch(src Source) (string, error) {
	key := src.Repo + "@" + src.Ref
	g := f.get(key, src)
	if g.part && !inRepoDir(src.Path, g.under) {
		g = f.get(key+"@"+src.Path, src)
	}
	return g.dir, g.err
}

// get is the fetch kept under key, made from src the first time.
func (f *fetcher) get(key string, src Source) *fetched {
	f.mu.Lock()
	if f.got == nil {
		f.got = map[string]*fetched{}
	}
	g := f.got[key]
	if g == nil {
		g = &fetched{}
		f.got[key] = g
	}
	f.mu.Unlock()
	g.once.Do(func() {
		g.dir, g.part, g.err = fetchPart(src, "")
		g.under = src.Path
		if g.err == nil {
			f.mu.Lock()
			f.dirs = append(f.dirs, g.dir)
			f.mu.Unlock()
		}
	})
	return g
}

func (f *fetcher) clean() {
	for _, d := range f.dirs {
		os.RemoveAll(d)
	}
}

// skillUpdate is a skill fetched again, ready to take the old one's place.
type skillUpdate struct {
	name   string
	src    *Source
	adopt  bool
	from   string
	commit string // the commit a git repository was fetched at
}

func (f *fetcher) prepare(s *Skill) (*skillUpdate, error) {
	name := s.Name
	src, adopt := s.Source, false
	if !src.remote() {
		// one CC Switch installed from GitHub becomes the library's own,
		// fetched from there, leaving CC Switch's folder as it is
		o, ok := ccSwitchOrigin(s)
		if !ok {
			// one the skills CLI installed does too, fetched from where
			// its lock says it came (#4645)
			o, ok = lockOrigin(s)
		}
		if !ok {
			return nil, fmt.Errorf("%s isn't from GitHub or a git repository; it's kept as it is", name)
		}
		src, adopt = &o, true
	} else {
		c := *src
		src = &c
	}
	// CC Switch's Path is only the folder's name (origin): the whole
	// repository is looked in
	at := func() Source {
		w := *src
		if adopt {
			w.Path = ""
		}
		return w
	}
	tmp, err := f.fetch(at())
	if err != nil && adopt && src.Ref != "" {
		src.Ref = "" // the branch CC Switch recorded is gone: the default one
		tmp, err = f.fetch(at())
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
		if !adopt && goneFrom(tmp, src.Path) {
			// the repository was read, and the skill isn't in it
			return nil, &GoneError{Name: name, Source: src.Repo}
		}
		return nil, fmt.Errorf("%s has no SKILL.md at %s any more", src.Repo, src.Path)
	}
	up := &skillUpdate{name: name, src: src, adopt: adopt, from: from}
	if src.Kind == "git" {
		up.commit = gitHeadOf(tmp)
	}
	return up, nil
}

// apply puts the fetched skill in the old one's place; the agents' links
// go on pointing at it. The old one goes to the backups unless it is
// known to be the files fetched before, unchanged.
func (up *skillUpdate) apply(l *Library, targets []*Target) error {
	name := up.name
	keep := true
	if s := l.skill(name); s != nil {
		edited, known := l.editState(s, targets)
		keep = edited || !known
	}
	next, old := skillDir("."+name+".next"), skillDir("."+name+".old")
	os.RemoveAll(next)
	os.RemoveAll(old)
	if err := copyDir(up.from, next); err != nil {
		os.RemoveAll(next)
		return err
	}
	// one kept under another name than its SKILL.md gives stays named so
	if s := l.skill(name); s != nil && s.Called != "" {
		if err := nameSkill(next, name); err != nil {
			os.RemoveAll(next)
			return err
		}
	}
	hash, own := hashDir(next), ownHash(next)
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
		// GitHub has to be these; a git repository's is the one fetched
		s.Hash, s.Own, s.Commit = hash, own, up.commit
		s.Gone = "" // its source has it again
	}
	forgetCheck(name)
	if old == "" {
		return nil
	}
	// its installed dependencies go on with it
	carryInstalled(old, skillDir(name))
	if keep {
		var lb *leftBehind
		if _, err := keepReplaced(name, old); err != nil && !errors.As(err, &lb) {
			// the update is in; what it replaced stays beside it, not lost
			return fmt.Errorf("%s is updated, but the version it replaced couldn't be kept with the backups and is at %s: %w", name, old, err)
		}
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
	// a group the user put it in is its, not its name's: another skill
	// called so later is not in it
	l.pruneSkillGroups()
	return nil
}

// UseLibrarySkill settles an agent's own skill that stands in the way of
// the library's by that name for the library's: the agent's folder is kept
// aside with the backups, and the library's linked in its place.
func UseLibrarySkill(name, agentID string) (*Result, error) {
	return UseLibrarySkills([]string{name}, agentID)
}

// UseLibrarySkills is UseLibrarySkill for several of one agent's skills at
// once, as the Library's warning settles them together (#1896). Claude
// Desktop's own skills (Anthropic's built in, or made there) aren't put
// aside: they are listed in its manifest, not only a folder, so only
// KeepAgentSkills settles those.
func UseLibrarySkills(names []string, agentID string) (*Result, error) {
	return change(func(l *Library) error {
		for _, name := range names {
			s, t, err := ownSkill(l, name, agentID)
			if err != nil {
				return err
			}
			if len(t.Desktop) > 0 {
				return fmt.Errorf("Claude Desktop keeps its own %s: it can't be put aside for the library's, only kept", name)
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
		}
		return nil
	})
}

// KeepAgentSkill settles it the other way: the agent keeps its own, and
// the library no longer gives it that skill (nor any agent that reads the
// very same folder).
func KeepAgentSkill(name, agentID string) (*Result, error) {
	return KeepAgentSkills([]string{name}, agentID)
}

// KeepAgentSkills is KeepAgentSkill for several of one agent's skills at
// once (#1896).
func KeepAgentSkills(names []string, agentID string) (*Result, error) {
	return change(func(l *Library) error {
		for _, name := range names {
			s, t, err := ownSkill(l, name, agentID)
			if err != nil {
				return err
			}
			for _, id := range append(sharing(t), t.Agent.ID) {
				s.Agents = set(s.Agents, id, false)
			}
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
	// a skill in several agents is compared with the first found, which
	// is walked once
	seen := walked{}
	for _, pl := range places {
		es, _ := os.ReadDir(pl.dir)
		for _, e := range es {
			name := e.Name()
			// LiveAgent's own, which it makes again in its folder
			if pl.agent == "liveagent" && slices.Contains(liveAgentBuiltins, name) {
				continue
			}
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
			case sameTreeIn(seen, p, f.at):
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

// linkVia is what the library links to for a skill found at the entry at,
// whose folder is really at real: the first link on the way from at that
// is outside every skills folder (the agents', the shared one and the
// library's), which the app that made it keeps pointing at its current
// folder, as ego lite's ~/.local/share/ego/ego-skills does. A folder in a
// bundle's Versions/<v> is named through Versions/Current when Current is
// that very version. It is real when nothing on the way is any steadier,
// or when the way doesn't end at real.
func linkVia(at, real string) string {
	var homes []string
	for _, d := range []string{skillsDir(), sharedSkillsDir()} {
		homes = append(homes, realBelow(d))
	}
	for _, t := range Targets() {
		if t.Skills != "" {
			homes = append(homes, realBelow(t.Skills))
		}
	}
	inHome := func(p string) bool {
		up := realBelow(filepath.Dir(p))
		return slices.ContainsFunc(homes, func(h string) bool { return within(up, h) })
	}
	cur := filepath.Clean(at)
	for i := 0; inHome(cur); i++ {
		to, ok := linkTarget(cur)
		if !ok || i > 40 {
			cur = real
			break
		}
		if !filepath.IsAbs(to) {
			to = filepath.Join(filepath.Dir(cur), to)
		}
		cur = filepath.Clean(to)
	}
	cur = currentVersion(cur)
	if realDir(cur) != real {
		return real
	}
	return cur
}

// currentVersion is p with a Versions/<v> part of it named Versions/Current,
// when that link is there and points at <v>: a macOS framework's versions,
// of which an update leaves the old one behind.
func currentVersion(p string) string {
	parts := strings.Split(p, string(filepath.Separator))
	for i := len(parts) - 2; i > 0; i-- {
		if parts[i] != "Versions" || parts[i+1] == "Current" {
			continue
		}
		vs := strings.Join(parts[:i+1], string(filepath.Separator))
		cur := filepath.Join(vs, "Current")
		if realDir(cur) == realDir(filepath.Join(vs, parts[i+1])) && linked(cur) {
			return filepath.Join(append([]string{cur}, parts[i+2:]...)...)
		}
	}
	return p
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
	// .git behind, and the skills CLI's lock names it where it was, or
	// names a copy it gave the agent when the CLI wrote that very folder
	// (lockRepo), kept as From so an edit made later leaves it grouped
	tr := &tracer{}
	from := tr.folder(f.real)
	if from == "" && f.Library == "" && f.Link == "" && !linked(f.at) {
		from = tr.lockRepo(name, f.real)
	}
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
		// linked through the way the agent's own link goes, not to where
		// it ends today: an app that keeps its skills in a versioned
		// folder points its own link at the new one when it updates
		// (iomect's ego lite, #2635), and the library follows
		via := linkVia(f.at, f.real)
		if err := dirLink(via, skillDir(name)); err != nil {
			return err
		}
		src.Dir = via
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

// RemoveFoundSkill takes a skill the agents have of their own out of every
// agent that has it (#1303), without bringing it into the library first:
// each agent's folder, and its copies of the very same files, go to the
// backups; its links are taken away. Its entry in the shared
// ~/.agents/skills, or a folder of it left in the library's own, goes to
// the backups too, as agents read those. A folder elsewhere that the
// agents only linked to is left where it is, and so is another skill by
// that name (Others): that is another skill.
func RemoveFoundSkill(name string) (*Result, error) {
	return change(func(l *Library) error {
		return removeFound(foundSkills(l), name)
	})
}

// RemoveFoundSkills takes the skills named, of those the agents have of
// their own, out of every agent in one change, each exactly as
// RemoveFoundSkill takes one (wongxy on Discord: 批量删除). One that can't
// be taken out stays, and is said in Unremoved; the others go all the
// same. Only when none went does the change fail, with the first reason.
func RemoveFoundSkills(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no skills to remove")
	}
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	var failed []Problem
	res, err := change(func(l *Library) error {
		found := foundSkills(l)
		for _, name := range names {
			if err := removeFound(found, name); err != nil {
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

// removeFound takes the skill of that name, of those found in the agents,
// out of each place it is: links taken away, folders set aside.
func removeFound(found []FoundSkill, name string) error {
	i := slices.IndexFunc(found, func(f FoundSkill) bool { return f.Name == name })
	if i < 0 {
		return fmt.Errorf("no agent has a skill called %s that the library hasn't", name)
	}
	f := found[i]
	type entry struct{ who, p string }
	var es []entry
	for _, id := range slices.Concat(f.Agents, f.Copies) {
		if t := targetByID(id); t != nil && t.Skills != "" {
			es = append(es, entry{id, filepath.Join(t.Skills, name)})
		}
	}
	if f.Shared != "" {
		es = append(es, entry{"agents", f.Shared})
	}
	if f.Library != "" {
		es = append(es, entry{"library", f.Library})
	}
	// links first, so none is left pointing at a folder already set
	// aside; an agent reading the very same folder as another has its
	// entry gone already
	slices.SortStableFunc(es, func(a, b entry) int {
		la, lb := linked(a.p), linked(b.p)
		switch {
		case la == lb:
			return 0
		case la:
			return -1
		}
		return 1
	})
	seen := map[string]bool{}
	for _, e := range es {
		if seen[e.p] {
			continue
		}
		seen[e.p] = true
		if _, err := os.Lstat(e.p); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if linked(e.p) {
			if err := os.Remove(e.p); err != nil {
				return err
			}
			continue
		}
		if _, err := setAside(e.who, e.p); err != nil {
			return err
		}
	}
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
// Two folders unchanged since they were last compared aren't read again
// (treememo.go).
func sameTree(a, b string) bool { return sameTreeIn(nil, a, b) }

// walked is the folders one pass has walked (treeOf), for a folder
// compared with several others to be walked once in it.
type walked map[string]*tree

// sameTreeIn is sameTree, taking a folder walked already in this pass
// from w (nil: none).
func sameTreeIn(w walked, a, b string) bool {
	a, b = realDir(a), realDir(b)
	look := func(d string) (tree, bool) {
		if t, ok := w[d]; ok {
			if t == nil {
				return tree{}, false
			}
			return *t, true
		}
		t, ok := treeOf(d)
		if w != nil {
			if ok {
				w[d] = &t
			} else {
				w[d] = nil
			}
		}
		return t, ok
	}
	ta, okA := look(a)
	tb, okB := look(b)
	if !okA || !okB {
		// unknown: not the same, and not kept as different
		return false
	}
	return rememberedSame(a, b, ta, tb, func() (bool, bool) { return compareTrees(a, b, ta.files, tb.files) })
}

// compareTrees is sameTree, reading every file of both. sure is false
// when one couldn't be read whole: they aren't taken to be the same, and
// that isn't kept as what they are.
func compareTrees(a, b string, fa, fb map[string]treeEntry) (same, sure bool) {
	if len(fa) != len(fb) {
		return false, true
	}
	for rel, x := range fa {
		y, ok := fb[rel]
		if !ok || x != y {
			return false, true
		}
		if x.link == "" {
			if same, sure := sameFile(filepath.Join(a, rel), filepath.Join(b, rel)); !same {
				return false, sure
			}
		}
	}
	return true, true
}

type treeEntry struct {
	size int64
	link string // where a link in it points
}

// tree is a folder's files and links by where they are in it, and the
// stamp of them (treememo.go), from one walk that opens no file.
type tree struct {
	files   map[string]treeEntry
	stamp   string
	settled bool
}

// treeOf is a folder's files and links; not ok when it can't be read, or
// is too big to be worth comparing.
func treeOf(root string) (tree, bool) {
	out := map[string]treeEntry{}
	st := newStamper()
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
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = treeEntry{link: to}
		} else if d.Type().IsRegular() {
			out[rel] = treeEntry{size: fi.Size()}
			total += fi.Size()
		} else {
			return nil
		}
		st.add(rel, fi)
		if len(out) > 20000 || total > 256<<20 {
			return errors.New("too big to compare")
		}
		return nil
	})
	if err != nil {
		return tree{}, false
	}
	stamp, settled := st.done()
	return tree{files: out, stamp: stamp, settled: settled}, true
}

// sameFile is whether a and b hold the same bytes; sure is false when one
// couldn't be read.
func sameFile(a, b string) (same, sure bool) {
	fa, err := os.Open(a)
	if err != nil {
		return false, false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, false
	}
	defer fb.Close()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	end := func(e error) bool { return e == io.EOF || e == io.ErrUnexpectedEOF }
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if ea != nil && !end(ea) || eb != nil && !end(eb) {
			return false, false
		}
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, true
		}
		if ea != nil || eb != nil {
			return end(ea) && end(eb), true
		}
	}
}

// SkillPath is where a library skill's folder is.
func SkillPath(name string) string { return skillDir(name) }
