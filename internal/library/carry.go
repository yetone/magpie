package library

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Bundle is the library as a backup carries it to another computer: the
// instruction sets and the one on, what each agent gets besides, the MCP
// servers, the skills with their files and the subagents. What magpie
// wrote into each agent (Applied, the subagents' files among it) is this
// computer's own and stays out. It is made the
// same way every time — no times, everything in order — so two computers
// with the same library carry the same bundle.
type Bundle struct {
	Instructions Instructions      `json:"instructions"`
	Texts        map[string]string `json:"texts,omitempty"` // set → its text
	Extra        map[string]string `json:"extra,omitempty"` // agent → what it gets besides
	MCP          []*Server         `json:"mcp"`
	Skills       []*CarriedSkill   `json:"skills"`
	Icons        map[string]string `json:"icons,omitempty"`
	// Subagents are nil in a bundle from a magpie before they were
	// carried, which knew nothing of this computer's: Put keeps them. Collect
	// always sets it, empty too, so taking the last one out is carried.
	Subagents *[]*Subagent `json:"subagents,omitempty"`
	// Hooks are nil in a bundle from a magpie before they were carried,
	// as Subagents are: Put keeps this computer's. Applied.Hooks, the
	// entries magpie wrote into each agent here, stays out.
	Hooks *[]*Hook `json:"hooks,omitempty"`
	// Env is nil in a bundle from a magpie before variables were carried:
	// Put keeps this computer's. Applied.Env stays out. A backup made
	// without keys carries the names with empty values (WithoutSecrets).
	Env *[]*EnvVar `json:"env,omitempty"`
}

// CarriedSkill is a skill with its folder's files.
type CarriedSkill struct {
	Name   string            `json:"name"`
	Source *Source           `json:"source,omitempty"`
	Agents []string          `json:"agents"`
	Files  map[string][]byte `json:"files"`          // by path in the folder, with /
	Exec   []string          `json:"exec,omitempty"` // the files that run
	Left   []string          `json:"left,omitempty"` // files too big to carry
	// Called is the name its SKILL.md gives, when that isn't Name
	Called string `json:"called,omitempty"`
	// Note is the user's note on it (Skill.Note)
	Note string `json:"note,omitempty"`
	// Router is Skill.Router: direct, kept out of the skill router
	Router string `json:"router,omitempty"`
	// From is the repository the skill was traced to on the computer that
	// made the bundle (Skill.From, skill_from.go), when it isn't a source
	// the skill carries itself: the lock, a checkout, CC Switch's record or
	// an install-again there. What traced it is that computer's own files,
	// so without this the other computer groups the skill under "this
	// computer". Bundles from before this have none.
	From string `json:"from,omitempty"`
}

// A skill's file bigger than maxCarried is left out, and so are the
// biggest once the skills come to maxCarriedAll. A skill's own files go
// with it, its scripts and its templates (a PowerPoint template with its
// pictures is several MB), but a sync reads at most 64 MiB of a backup
// (davsync), and a skill's bytes are base64 twice in one (the bundle's
// JSON, then the sealed envelope's): 32 MiB of files is about 57 MiB.
const (
	maxCarried    = 16 << 20
	maxCarriedAll = 32 << 20
)

// Collect is the library as it is now; nil when there is none yet.
func Collect() (*Bundle, error) {
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(path()); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil { // there, but unknown: never carried as no library (#2269)
		return nil, err
	}
	l, err := load()
	if err != nil {
		return nil, err
	}
	b := &Bundle{Instructions: l.Instructions, Texts: map[string]string{}, Extra: extras(), MCP: []*Server{}, Skills: []*CarriedSkill{}, Icons: l.Icons}
	b.Instructions.Agents = orNone(b.Instructions.Agents)
	// a set's own file is this computer's: its text goes, where it is
	// doesn't
	b.Instructions.Sets = slices.Clone(b.Instructions.Sets)
	for i := range b.Instructions.Sets {
		b.Instructions.Sets[i].File = ""
	}
	for _, s := range l.sets() {
		if text := setText(l.textPath(s.ID)); text != "" {
			b.Texts[s.ID] = text
		}
	}
	if len(b.Texts) == 0 {
		b.Texts = nil
	}
	if len(b.Extra) == 0 {
		b.Extra = nil
	}
	// a server kept to this computer stays out; another computer's by its
	// name, which a sync brought, goes on as it came (mcp_local.go)
	for _, s := range sorted(l.carriedServers(), func(s *Server) string { return s.Name }) {
		// magpie's own by name, each computer's own binary; a server
		// started its own way here goes with the synced start (mcp_here.go)
		c := *carried(carriedStart(s))
		c.Agents = orNone(slices.Sorted(slices.Values(s.Agents)))
		b.MCP = append(b.MCP, &c)
	}
	skills := sorted(l.Skills, func(s *Skill) string { return s.Name })
	dirs := []string{}
	for _, s := range skills {
		dirs = append(dirs, realDir(skillDir(s.Name)))
	}
	tr := &tracer{}
	for i, c := range readSkills(dirs) {
		s := skills[i]
		if s.Source == nil || s.Source.Kind == "folder" {
			c.From = tr.repoOf(s)
		}
		c.Name, c.Source, c.Agents, c.Called, c.Note, c.Router = s.Name, s.Source, orNone(slices.Sorted(slices.Values(s.Agents))), s.Called, s.Note, s.Router
		b.Skills = append(b.Skills, c)
	}
	subs := []*Subagent{}
	for _, s := range sorted(l.Subagents, func(s *Subagent) string { return s.Name }) {
		c := *s
		c.Skills, c.MCP = slices.Sorted(slices.Values(s.Skills)), slices.Sorted(slices.Values(s.MCP))
		c.Agents = orNone(slices.Sorted(slices.Values(s.Agents)))
		subs = append(subs, &c)
	}
	b.Subagents = &subs
	hooks := []*Hook{}
	for _, h := range sorted(l.Hooks, func(h *Hook) string { return h.Name }) {
		c := *h
		c.Agents = orNone(slices.Sorted(slices.Values(h.Agents)))
		hooks = append(hooks, &c)
	}
	b.Hooks = &hooks
	envs := []*EnvVar{}
	for _, e := range sorted(l.Env, func(e *EnvVar) string { return e.Name }) {
		c := *e
		c.Agents = orNone(slices.Sorted(slices.Values(e.Agents)))
		envs = append(envs, &c)
	}
	b.Env = &envs
	return b, nil
}

func sorted[T any](xs []T, key func(T) string) []T {
	return slices.SortedFunc(slices.Values(xs), func(a, b T) int { return strings.Compare(key(a), key(b)) })
}

func orNone(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// readSkills is each skill folder's files. Links in it, .git and what
// isn't a plain file stay out. What goes is chosen smallest first across
// every skill, so the text of them all comes before any big file.
func readSkills(dirs []string) []*CarriedSkill { return readSkillsMax(dirs, maxCarried, maxCarriedAll) }

// readSkillsMax is readSkills with a file's and the whole's budget given.
func readSkillsMax(dirs []string, perFile, all int64) []*CarriedSkill {
	type file struct {
		skill  int
		rel, p string
		size   int64
		runs   bool
	}
	var files []file
	out := make([]*CarriedSkill, len(dirs))
	for i, dir := range dirs {
		out[i] = &CarriedSkill{Files: map[string][]byte{}}
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != dir && (d.Name() == ".git" || d.Name() == marker) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || d.Name() == marker {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			files = append(files, file{i, filepath.ToSlash(rel), p, fi.Size(), fi.Mode().Perm()&0o111 != 0})
			return nil
		})
	}
	// smallest first, and the same order for the same files every time
	slices.SortFunc(files, func(a, b file) int {
		return cmp.Or(cmp.Compare(a.size, b.size), cmp.Compare(a.skill, b.skill), strings.Compare(a.rel, b.rel))
	})
	budget := all
	for _, f := range files {
		c := out[f.skill]
		if f.size > perFile || f.size > budget {
			c.Left = append(c.Left, f.rel)
			continue
		}
		data, err := os.ReadFile(f.p)
		if err != nil {
			continue
		}
		if n := int64(len(data)); n > perFile || n > budget { // grew since
			c.Left = append(c.Left, f.rel)
			continue
		}
		budget -= int64(len(data))
		c.Files[f.rel] = data
		if f.runs {
			c.Exec = append(c.Exec, f.rel)
		}
	}
	for _, c := range out {
		slices.Sort(c.Exec)
		slices.Sort(c.Left)
	}
	return out
}

// check refuses a bundle that would write outside the library: a name
// that isn't one, a file's path that climbs out of its skill.
func (b *Bundle) check() error {
	for _, s := range b.Instructions.Sets {
		if err := checkName("set", s.ID); err != nil {
			return err
		}
	}
	for id := range b.Texts {
		if id != defaultSet && !slices.ContainsFunc(b.Instructions.Sets, func(s InstrSet) bool { return s.ID == id }) {
			return fmt.Errorf("a text for no set, %q", id)
		}
	}
	for id := range b.Extra {
		if err := checkAgent(id); err != nil {
			return err
		}
	}
	for _, s := range b.MCP {
		if s == nil {
			return fmt.Errorf("an empty server")
		}
		if err := s.check(); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, s := range b.Skills {
		if s == nil {
			return fmt.Errorf("an empty skill")
		}
		if err := checkName("skill", s.Name); err != nil {
			return err
		}
		if seen[s.Name] {
			return fmt.Errorf("the skill %s twice", s.Name)
		}
		seen[s.Name] = true
		for _, rel := range slices.Concat(slices.Collect(maps.Keys(s.Files)), s.Left) {
			if !localFile(rel) {
				return fmt.Errorf("skill %s: %q is not a file in its folder", s.Name, rel)
			}
		}
	}
	if b.Subagents != nil {
		seen := map[string]bool{}
		for _, s := range *b.Subagents {
			if s == nil {
				return fmt.Errorf("an empty subagent")
			}
			// the name is the file's in every agent's folder; the agents
			// are left as they come, as a skill's are: one a newer magpie
			// gives subagents to is no target here and gets nothing
			if !subagentNameRe.MatchString(s.Name) || slices.Contains(reservedSubagents, s.Name) {
				return fmt.Errorf("the subagent name %q", s.Name)
			}
			if seen[s.Name] {
				return fmt.Errorf("the subagent %s twice", s.Name)
			}
			seen[s.Name] = true
		}
	}
	if b.Env != nil {
		seen := map[string]bool{}
		for _, e := range *b.Env {
			if e == nil {
				return fmt.Errorf("an empty variable")
			}
			if err := e.check(); err != nil {
				return err
			}
			if seen[e.Name] {
				return fmt.Errorf("the variable %s twice", e.Name)
			}
			seen[e.Name] = true
		}
	}
	if b.Hooks != nil {
		seen := map[string]bool{}
		for _, h := range *b.Hooks {
			if h == nil {
				return fmt.Errorf("an empty hook")
			}
			// the event and agents are left as they come: one a newer
			// magpie knows and this one doesn't is a problem on the
			// agent it is given to (syncHooks), not a refused library
			if err := checkName("hook", h.Name); err != nil {
				return err
			}
			if strings.TrimSpace(h.Command) == "" {
				return fmt.Errorf("the hook %s has no command", h.Name)
			}
			if seen[h.Name] {
				return fmt.Errorf("the hook %s twice", h.Name)
			}
			seen[h.Name] = true
		}
	}
	return nil
}

// localFile reports whether rel, with /, names a file inside a folder:
// not absolute, no .., no \ to be read as a separator elsewhere.
func localFile(rel string) bool {
	return rel != "" && !strings.ContainsAny(rel, "\\:\x00") && filepath.IsLocal(filepath.FromSlash(rel)) &&
		!slices.Contains(strings.Split(rel, "/"), "..")
}

// WithoutSecrets empties the values of the servers' environment variables
// and headers that secret says hold one; their names stay, to be filled in.
func (b *Bundle) WithoutSecrets(secret func(string) bool) {
	// a variable's name says nothing of what its value is: every value
	// goes, the names stay
	if b.Env != nil {
		envs := []*EnvVar{}
		for _, e := range *b.Env {
			c := *e
			c.Value = ""
			envs = append(envs, &c)
		}
		b.Env = &envs
	}
	for i, s := range b.MCP {
		c := *s
		c.Env, c.Headers = blank(s.Env, secret), blank(s.Headers, secret)
		c.Accounts = nil
		for _, a := range s.Accounts {
			a.Headers = blank(a.Headers, secret)
			c.Accounts = append(c.Accounts, a)
		}
		// an OAuth app's secret is a key too; its client ID isn't
		if s.OAuth != nil && s.OAuth.ClientSecret != "" {
			a := *s.OAuth
			a.ClientSecret = ""
			c.OAuth = &a
		}
		b.MCP[i] = &c
	}
}

func blank(m map[string]string, secret func(string) bool) map[string]string {
	if m == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		if secret(k) {
			v = ""
		}
		out[k] = v
	}
	return out
}

// WithSecrets is b with the values WithoutSecrets emptied filled from the
// servers of the same name in have, so a bundle made without keys doesn't
// take away the ones kept here.
func (b *Bundle) WithSecrets(have *Bundle, secret func(string) bool) *Bundle {
	if have == nil {
		return b
	}
	out := *b
	out.MCP = nil
	if b.Env != nil && have.Env != nil {
		envs := []*EnvVar{}
		for _, e := range *b.Env {
			c := *e
			if i := slices.IndexFunc(*have.Env, func(h *EnvVar) bool { return h.Name == e.Name }); i >= 0 && c.Value == "" {
				c.Value = (*have.Env)[i].Value
			}
			envs = append(envs, &c)
		}
		out.Env = &envs
	}
	for _, s := range b.MCP {
		c := *s
		i := slices.IndexFunc(have.MCP, func(h *Server) bool { return h.Name == s.Name })
		if i >= 0 {
			c.Env, c.Headers = filled(s.Env, have.MCP[i].Env, secret), filled(s.Headers, have.MCP[i].Headers, secret)
			c.Accounts = nil
			for _, a := range s.Accounts {
				if j := slices.IndexFunc(have.MCP[i].Accounts, func(h Account) bool { return h.ID == a.ID }); j >= 0 {
					a.Headers = filled(a.Headers, have.MCP[i].Accounts[j].Headers, secret)
				}
				c.Accounts = append(c.Accounts, a)
			}
			// the same app's secret, kept here
			if h := have.MCP[i].OAuth; s.OAuth != nil && s.OAuth.ClientSecret == "" && h != nil && h.ClientID == s.OAuth.ClientID {
				a := *s.OAuth
				a.ClientSecret = h.ClientSecret
				c.OAuth = &a
			}
		}
		out.MCP = append(out.MCP, &c)
	}
	return &out
}

func filled(m, have map[string]string, secret func(string) bool) map[string]string {
	if m == nil {
		return nil
	}
	out := maps.Clone(m)
	for k, v := range m {
		if v == "" && secret(k) && have[k] != "" {
			out[k] = have[k]
		}
	}
	return out
}

// Put makes the library what b says — its instructions, servers and
// skills in place of the ones here — and writes it into the agents, which
// keep whatever of theirs isn't magpie's. The skills are written aside
// first, and only once all of them are there does any go in; a skill
// replaced or taken out is kept with the backups, and so is an
// instructions file that changes. A skill that comes linked to a folder
// this computer has too is linked to it; else it comes as its files. A
// skill whose files are as they are here is left alone. The subagents
// replace the ones here too, unless b is from a magpie that didn't carry
// them.
func Put(b *Bundle) (*Result, error) {
	if err := b.check(); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if err := os.MkdirAll(skillsDir(), 0o755); err != nil {
			return err
		}
		stage, err := os.MkdirTemp(Dir(), ".incoming-") // beside skills, to be renamed in
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)

		keep, link := map[string]bool{}, map[string]string{}
		for _, s := range b.Skills {
			if cur := l.skill(s.Name); cur != nil && sameSource(cur.Source, s.Source) {
				if have := readSkills([]string{realDir(skillDir(s.Name))})[0]; sameFiles(have, s) {
					keep[s.Name] = true
					continue
				}
			}
			if src := s.Source; src != nil && src.Kind == "folder" && src.Dir != "" && isDir(src.Dir) {
				if to, err := os.Readlink(skillDir(s.Name)); err == nil && filepath.Clean(to) == filepath.Clean(src.Dir) {
					keep[s.Name] = true
				} else {
					link[s.Name] = src.Dir
				}
				continue
			}
			if err := writeSkill(filepath.Join(stage, s.Name), s); err != nil {
				return fmt.Errorf("skill %s: %w", s.Name, err)
			}
			if err := keepLeft(filepath.Join(stage, s.Name), realDir(skillDir(s.Name)), s); err != nil {
				return fmt.Errorf("skill %s: %w", s.Name, err)
			}
		}

		// every skill's files are ready: out with the old ones
		aside := filepath.Join(BackupDir(), time.Now().Format("2006-01-02_15-04-05.000"), "skills")
		out := func(name string) error {
			p := skillDir(name)
			fi, err := os.Lstat(p)
			if err != nil {
				return nil
			}
			if linkEntry(fi) {
				return os.Remove(p) // a folder of the user's: only the link goes
			}
			if err := os.MkdirAll(aside, 0o700); err != nil {
				return err
			}
			return move(p, filepath.Join(aside, name))
		}
		for _, s := range l.Skills {
			if !keep[s.Name] {
				if err := out(s.Name); err != nil {
					return err
				}
			}
		}
		skills := []*Skill{}
		for _, s := range b.Skills {
			// turned off here stays off here: that is this computer's own
			off := l.skill(s.Name) != nil && l.skill(s.Name).OffHere
			n := &Skill{Name: s.Name, Source: s.Source, Agents: slices.Clone(orNone(s.Agents)), OffHere: off, Called: s.Called, Note: s.Note, Router: s.Router}
			// where it came from: the other computer's trace, else what
			// this one has found itself (Group again) for the same skill,
			// which a bundle from before From was carried says nothing of
			n.From = s.From
			if cur := l.skill(s.Name); cur != nil && sameSource(cur.Source, s.Source) {
				if n.From == "" {
					n.From = cur.From
				}
				n.Gone = cur.Gone // kept on this computer only
			}
			skills = append(skills, n)
			if keep[s.Name] {
				continue
			}
			if err := out(s.Name); err != nil { // one of no skill's, in the way
				return err
			}
			if dir, ok := link[s.Name]; ok {
				if err := dirLink(dir, skillDir(s.Name)); err != nil {
					if err := copyDir(dir, skillDir(s.Name)); err != nil {
						return err
					}
				}
				continue
			}
			if err := os.Rename(filepath.Join(stage, s.Name), skillDir(s.Name)); err != nil {
				return err
			}
		}
		l.Skills = skills

		// the instructions: every text here that b hasn't goes, kept first
		kept := newBackups()
		l.kept = kept
		texts := map[string]string{}
		// a set linked here to a file of the user's keeps it: the file is
		// given the text b has for the set, and never emptied for a text
		// b hasn't
		files := map[string]string{}
		for _, s := range l.sets() {
			if s.File != "" {
				files[s.ID] = s.File
			} else {
				texts[setPath(s.ID)] = ""
			}
		}
		for id := range extras() {
			texts[extraPath(id)] = ""
		}
		for id, text := range b.Texts {
			if files[id] == "" {
				texts[setPath(id)] = text
			}
		}
		for id, text := range b.Extra {
			texts[extraPath(id)] = text
		}
		for _, p := range slices.Sorted(maps.Keys(texts)) {
			if readText(p) == strings.TrimSpace(texts[p]) {
				continue
			}
			if err := kept.keep("library", p); err != nil {
				return err
			}
			if err := writeText(p, texts[p]); err != nil {
				return err
			}
		}
		l.Instructions = Instructions{Agents: slices.Sorted(slices.Values(orNone(b.Instructions.Agents))),
			Sets: slices.Clone(b.Instructions.Sets), Active: b.Instructions.Active}
		for i := range l.Instructions.Sets {
			s := &l.Instructions.Sets[i]
			s.File = files[s.ID]
			if text := strings.TrimSpace(b.Texts[s.ID]); s.File != "" && text != "" {
				if err := l.putText(s.File, text); err != nil {
					return err
				}
			}
		}

		l.putServers(b.MCP)
		l.Icons = maps.Clone(b.Icons)
		// the agents are written on the sync change runs: what magpie
		// wrote here (Applied) decides, file by file, what is its own
		if b.Subagents != nil {
			subs := []*Subagent{}
			for _, s := range *b.Subagents {
				c := *s
				c.Skills, c.MCP, c.Agents = slices.Clone(s.Skills), slices.Clone(s.MCP), slices.Clone(orNone(s.Agents))
				subs = append(subs, &c)
			}
			l.Subagents = subs
		}
		// the same for hooks: Applied.Hooks decides, entry by entry,
		// what magpie takes out of each agent's file here
		if b.Hooks != nil {
			hooks := []*Hook{}
			for _, h := range *b.Hooks {
				c := *h
				c.Agents = slices.Clone(orNone(h.Agents))
				hooks = append(hooks, &c)
			}
			l.Hooks = hooks
		}
		// variables the same way: Applied.Env decides what is taken out
		if b.Env != nil {
			envs := []*EnvVar{}
			for _, e := range *b.Env {
				c := *e
				c.Agents = slices.Clone(orNone(e.Agents))
				envs = append(envs, &c)
			}
			l.Env = envs
		}
		return nil
	})
}

func sameSource(a, b *Source) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// sameFiles reports whether a folder here has what c carries.
func sameFiles(have, c *CarriedSkill) bool {
	return maps.EqualFunc(have.Files, c.Files, bytes.Equal) && slices.Equal(have.Exec, sorted(c.Exec, func(s string) string { return s }))
}

// writeSkill writes a carried skill's files into a new folder.
func writeSkill(dir string, s *CarriedSkill) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, rel := range slices.Sorted(maps.Keys(s.Files)) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if slices.Contains(s.Exec, rel) {
			mode = 0o755
		}
		if err := os.WriteFile(p, s.Files[rel], mode); err != nil {
			return err
		}
	}
	return nil
}

// keepLeft copies into a skill being put (at dir) the files the other
// computer left out as too big to carry that the skill here has, so it
// is put with them, not without.
func keepLeft(dir, here string, s *CarriedSkill) error {
	for _, rel := range s.Left {
		if _, ok := s.Files[rel]; ok {
			continue
		}
		from := filepath.Join(here, filepath.FromSlash(rel))
		fi, err := os.Lstat(from)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		to := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		in, err := os.Open(from)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
		if err == nil {
			_, err = io.Copy(out, in)
			err = errors.Join(err, out.Close())
		}
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Changed is when the library was last changed: library.json, or a file
// of its own (links not followed).
func Changed() time.Time {
	var t time.Time
	if fi, err := os.Stat(path()); err == nil {
		t = fi.ModTime()
	}
	filepath.WalkDir(Dir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
		return nil
	})
	return t
}
