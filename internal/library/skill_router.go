package library

// The skill router: with the Library's switch on (SkillRouter), an
// agent's skills folder is given one skill of magpie's, magpie-skills, in
// place of a link for each skill ticked for it. Its SKILL.md lists those
// skills, each with its description and the path of its own SKILL.md,
// which the agent reads when a task matches one. An
// agent then carries one skill's description in its prompt, not every
// one's, however many the library gives it.
//
// The entry is a link to a folder magpie writes for the agent's folder
// (routerDir): its SKILL.md, and under .skills a link to each skill it
// lists. The paths the SKILL.md gives lead through the agent's own skills
// folder, and .skills is a hidden folder, which the agents that look for
// skills in every folder under theirs (Codex, OpenCode, Pi) leave alone,
// so none of them finds a listed skill twice. Claude Code asks before it
// reads outside its project, even there, so magpie adds it Read rules
// for the entry and the folders behind it (routerAllow).
//
// What stays a link of its own (routedFor):
//   - a skill the user keeps direct (Skill.Router), to be a /command of
//     the agent's, or always in its prompt;
//   - one whose SKILL.md sets disable-model-invocation: it is for the
//     user to type, which a listed skill can't be;
//   - one the agent finds in ~/.agents/skills anyway.
//
// And an agent whose folder can't take the router keeps every link
// (routerWhy): one given copies (in WSL, or by the user's choice), Claude
// Desktop (synced by syncDesktopSkills, never here), LiveAgent (which
// uses only the skills picked in its own list), an agent whose folder is
// ~/.agents/skills itself (read by many agents, each of which would find
// a second router beside its own), and one whose folder has a
// magpie-skills of the library's or the user's own.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// RouterSkill is the name of the one skill the router gives an agent.
const RouterSkill = "magpie-skills"

// SkillDirect is Skill.Router for a skill the router leaves out: each
// agent given it keeps its own link, as with the router off.
const SkillDirect = "direct"

// Why an agent's folder keeps every skill's own link with the router on.
const (
	routerCopies    = "copies"    // it is given copies, which a link can't list
	routerLiveAgent = "liveagent" // it uses only the skills picked in its list
	routerShared    = "shared"    // its folder is ~/.agents/skills, read by many
	routerTaken     = "taken"     // the library has a skill called magpie-skills
	routerOwn       = "own"       // its folder has a magpie-skills of its own
)

// routerDescMax is the most a SKILL.md's description may be: Codex
// refuses a skill whose description is longer.
const routerDescMax = 1024

// SetSkillRouter turns the router on or off: on, each agent that can take
// it is given magpie-skills in place of the links of the skills it lists;
// off, their own links again.
func SetSkillRouter(on bool) (*Result, error) {
	return change(func(l *Library) error {
		l.SkillRouter = on
		return nil
	})
}

// SkillRouterHow lists a skill in the router, or keeps it each agent's
// own link (direct); the agents are written again at once.
func SkillRouterHow(name string, direct bool) (*Result, error) {
	return change(func(l *Library) error {
		s := l.skill(name)
		if s == nil {
			return fmt.Errorf("no skill called %s", name)
		}
		s.Router = ""
		if direct {
			s.Router = SkillDirect
		}
		return nil
	})
}

// routerRoot is where magpie writes the router's folders.
func routerRoot() string { return filepath.Join(settings.Dir(), "skill-router") }

// routerDir is the folder the router entry in the skills folder dir leads
// to: one for each folder, since what it lists is that folder's.
func routerDir(dir string) string {
	h := sha256.Sum256([]byte(realDir(dir)))
	return filepath.Join(routerRoot(), hex.EncodeToString(h[:6]))
}

// routerOurs is whether the entry at p is magpie's router entry.
func routerOurs(p string) bool {
	to, ok := linkTarget(p)
	if ok {
		return within(filepath.Clean(to), routerRoot())
	}
	return false
}

// routerWhy is "" for an agent whose skills folder can take the router,
// or why it keeps each skill's own link.
func (l *Library) routerWhy(t *Target, cp bool) string {
	switch {
	case cp || t.Store != "" || t.Desktop != nil:
		return routerCopies
	case t.Agent.ID == "liveagent":
		return routerLiveAgent
	case realDir(t.Skills) == realDir(sharedSkillsDir()):
		return routerShared
	case l.skill(RouterSkill) != nil:
		return routerTaken
	}
	p := filepath.Join(t.Skills, RouterSkill)
	if _, err := os.Lstat(p); err == nil && !routerOurs(p) {
		return routerOwn
	}
	return ""
}

// routable is whether the router may list s: not kept direct, and not one
// for the user alone to type.
func routable(s *Skill) bool {
	if s == nil || s.Router == SkillDirect {
		return false
	}
	m, _ := readMeta(skillDir(s.Name))
	return !m.Manual
}

// routedFor are the skills the router lists in agent t's folder, which it
// then gives no link of their own, for t and the agents reading the same
// folder (sharers); none with the router off, or for a folder that can't
// take it. inShared is whether t finds a skill in ~/.agents/skills anyway.
func (l *Library) routedFor(t *Target, sharers []string, cp bool, inShared func(*Skill) bool) map[string]bool {
	if !l.SkillRouter || t.Gone || l.routerWhy(t, cp) != "" {
		return nil
	}
	out := map[string]bool{}
	for _, s := range l.Skills {
		if !(s.gives(t.Agent.ID) || slices.ContainsFunc(sharers, s.gives)) || !routable(s) || inShared(s) {
			continue
		}
		// the agent's own skill by that name, or one blocked, stays as it
		// is: the user settles it as with the router off
		q := filepath.Join(t.Skills, s.Name)
		if l.blockedSkills[realDir(q)] {
			continue
		}
		if _, err := os.Lstat(q); err == nil && !ours(q, s.Name) && realDir(q) != realDir(skillDir(s.Name)) {
			continue
		}
		out[s.Name] = true
	}
	return out
}

// placeRouter writes the router's folder for t's skills folder with the
// skills routed, and its entry in the folder; with none routed, the entry
// magpie made there goes, and its folder.
func (l *Library) placeRouter(t *Target, routed map[string]bool, a *Applied, res *Result) {
	id := t.Agent.ID
	p := filepath.Join(t.Skills, RouterSkill)
	dir := routerDir(t.Skills)
	if len(routed) == 0 {
		if !a.Router {
			l.routerAllow(t, nil, a, res)
			return
		}
		if routerOurs(p) {
			if err := unlink(p); err != nil {
				res.fail(id, "skill:"+RouterSkill, err)
				return
			}
			res.changed(id)
		}
		os.RemoveAll(dir)
		a.Router = false
		l.routerAllow(t, nil, a, res)
		return
	}
	changed, err := writeRouter(dir, t.Skills, routed)
	if err != nil {
		res.fail(id, "skill:"+RouterSkill, err)
		return
	}
	if !routerOurs(p) || realDir(p) != realDir(dir) {
		unlink(p) // magpie's, to an old folder: routerWhy left a user's alone
		if err := os.MkdirAll(t.Skills, 0o755); err != nil {
			res.fail(id, "skill:"+RouterSkill, err)
			return
		}
		if err := dirLink(dir, p); err != nil {
			res.fail(id, "skill:"+RouterSkill, err)
			return
		}
		changed = true
	}
	a.Router = true
	if changed {
		res.changed(id)
	}
	l.routerAllow(t, routerRules(t, routed), a, res)
}

// routerRules are the rules that let Claude Code read a listed skill's
// files without asking. It asks before reading outside the project, its
// own skills folder too, and checks both the path it is given and the
// one a link leads to (Claude Code 2.1.316, tried in a sandbox HOME), so
// one rule is for the router's entry and one for the library's skills
// folder; a skill whose folder is elsewhere gets its own. Other agents
// have no such setting magpie writes: none.
func routerRules(t *Target, routed map[string]bool) []string {
	if t.Agent == nil || t.Agent.Plugin != "" || t.Settings == "" {
		return nil
	}
	if id, _, _ := strings.Cut(t.Agent.ID, "@"); id != "claude" {
		return nil
	}
	rule := func(dir string) string { return "Read(/" + claudePath(dir) + "/**)" }
	out := []string{rule(filepath.Join(realDir(t.Skills), RouterSkill))}
	lib := realDir(skillsDir())
	for _, n := range slices.Sorted(maps.Keys(routed)) {
		r := rule(lib)
		if d := realDir(skillDir(n)); !within(d, lib) {
			r = rule(d)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// claudePath is an absolute path as Claude Code's permission rules match
// it: with forward slashes, and on Windows C:\x as /c/x
// (code.claude.com/docs/en/permissions, "Read and Edit").
func claudePath(p string) string {
	if v := filepath.VolumeName(p); len(v) == 2 && v[1] == ':' {
		p = "/" + strings.ToLower(v[:1]) + p[2:]
	}
	return filepath.ToSlash(p)
}

// routerAllow writes the rules (want) into Claude Code's permissions.allow,
// beside the user's own, and takes away magpie's that are no longer
// wanted; a rule the user had already is theirs and stays when magpie's
// go.
func (l *Library) routerAllow(t *Target, want []string, a *Applied, res *Result) {
	if len(want) == 0 && len(a.RouterRules) == 0 {
		return
	}
	id := t.Agent.ID
	path := t.Settings
	fail := func(err error) {
		res.fail(id, "skill:"+RouterSkill, fmt.Errorf("reading skills without asking: %w", err))
	}
	if path == "" {
		a.RouterRules = nil
		return
	}
	raw, err := edit.Read(path)
	if err != nil {
		fail(err)
		return
	}
	doc := map[string]any{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(jsonc.ToJSON(raw), &doc); err != nil {
			fail(fmt.Errorf("%s: %w", path, err))
			return
		}
	}
	perms, _ := doc["permissions"].(map[string]any)
	cur, ok := entryTools(doc, []string{"permissions", "allow"})
	if !ok {
		fail(fmt.Errorf("%s: permissions.allow isn't a list of rules, so magpie leaves it as it is", path))
		return
	}
	list, mine := offList(cur, a.RouterRules, want)
	if slices.Equal(cur, list) {
		a.RouterRules = mine
		return
	}
	if l.kept == nil {
		l.kept = newBackups()
	}
	if err := l.kept.keep(id, path); err != nil {
		fail(err)
		return
	}
	if len(list) > 0 {
		err = edit.SetJSON(path, edit.KV{Path: "permissions.allow", Value: list})
	} else {
		// magpie's were the last: the key goes, and permissions too when
		// nothing else is in it
		keys := []string{"permissions.allow"}
		if len(perms) == 1 {
			keys = []string{"permissions"}
		}
		err = edit.DelJSON(path, keys...)
	}
	if err != nil {
		fail(err)
		return
	}
	a.RouterRules = mine
	res.changed(id)
}

// writeRouter writes the router's folder dir for the skills folder at
// skills: a link under .skills to each skill routed, and the SKILL.md
// that lists them. It reports whether anything there changed.
func writeRouter(dir, skills string, routed map[string]bool) (bool, error) {
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range routed {
			if !yield(n) {
				return
			}
		}
	})
	links := filepath.Join(dir, ".skills")
	if err := os.MkdirAll(links, 0o755); err != nil {
		return false, err
	}
	changed := false
	es, _ := os.ReadDir(links)
	for _, e := range es {
		if !routed[e.Name()] {
			if err := os.RemoveAll(filepath.Join(links, e.Name())); err != nil {
				return false, err
			}
			changed = true
		}
	}
	type entry struct{ name, desc, at string }
	var list []entry
	for _, n := range names {
		q := filepath.Join(links, n)
		at := filepath.Join(realDir(skills), RouterSkill, ".skills", n, "SKILL.md")
		if to, ok := linkTarget(q); !ok || filepath.Clean(to) != filepath.Clean(skillDir(n)) {
			os.RemoveAll(q)
			if err := dirLink(skillDir(n), q); err != nil {
				// no link here (Windows without the right to): the library's
				// own folder is read instead
				at = filepath.Join(skillDir(n), "SKILL.md")
			} else {
				changed = true
			}
		}
		m, _ := readMeta(skillDir(n))
		list = append(list, entry{n, m.Description, at})
	}
	var b strings.Builder
	desc := routerDesc(names, func(i int) string { return list[i].desc })
	q, _ := json.Marshal(desc)
	fmt.Fprintf(&b, "---\nname: %s\ndescription: %s\n---\n\n", RouterSkill, q)
	b.WriteString("# The user's skills\n\n")
	b.WriteString("Each skill below is a folder of instructions, and often scripts or templates, for one kind of task. ")
	b.WriteString("When a task matches a skill's description, read that skill's SKILL.md at the exact path given before you start (it is there, so don't search for it), and follow it. ")
	b.WriteString("A file or script a skill names is in that skill's own folder, beside its SKILL.md.\n\n")
	for _, e := range list {
		fmt.Fprintf(&b, "## %s\n\n", e.name)
		if e.desc != "" {
			b.WriteString(e.desc + "\n\n")
		}
		fmt.Fprintf(&b, "SKILL.md: `%s`\n\n", e.at)
	}
	md := filepath.Join(dir, "SKILL.md")
	if old, err := os.ReadFile(md); err == nil && string(old) == b.String() {
		return changed, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	tmp := md + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, md)
}

// routerDesc is the router's description: what it is, then each skill's
// name with the start of its own description, cut to what Codex takes.
func routerDesc(names []string, desc func(int) string) string {
	const head = "The user's skills. Read this before any task one of them may fit, to find the skill and read its SKILL.md: "
	short := func(d string, n int) string {
		if i := strings.IndexAny(d, ".。;；\n"); i > 0 {
			d = d[:i]
		}
		if r := []rune(d); len(r) > n {
			d = strings.TrimSpace(string(r[:n])) + "…"
		}
		return d
	}
	for _, n := range []int{80, 40, 20, 0} {
		parts := make([]string, len(names))
		for i, name := range names {
			parts[i] = name
			if d := short(desc(i), n); n > 0 && d != "" {
				parts[i] += " (" + d + ")"
			}
		}
		if s := head + strings.Join(parts, "; ") + "."; len(s) <= routerDescMax {
			return s
		}
	}
	s := head + strings.Join(names, ", ")
	if len(s) > routerDescMax-1 {
		r := []rune(s[:routerDescMax-len("…")])
		s = strings.TrimRight(string(r[:len(r)-1]), ", ") + "…"
	}
	return s
}
