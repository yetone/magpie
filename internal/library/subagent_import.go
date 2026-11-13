package library

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	slash "path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Subagents imported from a repository (CHENGJIAHEGOXIS on X): a link to
// a GitHub or git repository, a folder in one, one file, or a folder on
// this computer, such as msitarzewski/agency-agents and its ~280 agents in
// a folder for each division. Every Markdown file there whose front matter
// names the subagent and says what it is for is one: Claude Code's own
// shape, which agency-agents, wshobson/agents and the Claude Code plugins
// write. A file in a folder named agents (or droids) may leave its name to
// its file's, as OpenCode's do. A skill's folder (SKILL.md) is no
// subagent's.
//
// The subagent gets the file's name, description and prompt (the body).
// Every other key is left out: an agent's tools, model, colour, emoji and
// the like are not magpie's to write into every agent alike, and the
// subagent then uses what its agent has.
//
// Each one remembers the file it came from (From) and what it was then
// (FromHash), so importing the same files again updates the ones still
// as they came, leaves the ones changed here alone, and adds none twice.
// A name the library has for a subagent of its own, or from elsewhere, is
// never written over: the import takes the next free name (name-2).

// SubagentProbe is what was found at an address.
type SubagentProbe struct {
	Source     string              `json:"source"`
	Kind       string              `json:"kind"`
	Candidates []SubagentCandidate `json:"candidates"`
	// Left are the files that look like subagents and can't be one, with why
	Left []SubagentLeft `json:"left,omitempty"`
	src  Source         // the repository, Path the folder or file linked to
	root string
}

// SubagentCandidate is one subagent found.
type SubagentCandidate struct {
	Path        string `json:"path"` // the file's, in the repository or the folder
	Name        string `json:"name"`
	Description string `json:"description"`
	// State is, against the library: "" a new one, "have" it has this one
	// as it is there, "update" it has it from here as it was before, to
	// be updated, "edited" it has it from here changed since, left alone
	State string `json:"state,omitempty"`
	// As is the name the library has it by, or adds it by, when not Name
	As string `json:"as,omitempty"`
	s  *Subagent
}

// SubagentLeft is a file left out.
type SubagentLeft struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// SubagentsImported is what an import did, by the library's names.
type SubagentsImported struct {
	Added   []string `json:"added,omitempty"`
	Updated []string `json:"updated,omitempty"`
	Had     []string `json:"had,omitempty"`
	// Kept are the ones changed here since, left as they are
	Kept []string `json:"kept,omitempty"`
}

const (
	// maxSubagentFiles is the most Markdown files read at an address,
	// maxSubagentDepth how deep in it
	maxSubagentFiles = 5000
	maxSubagentDepth = 8
	maxSubagentFile  = 1 << 20
)

// ProbeSubagents is the subagents at input; nothing is written.
func ProbeSubagents(input string) (*SubagentProbe, error) {
	input = strings.TrimSpace(input)
	p, tmp, err := findSubagentsAt(input)
	if err != nil {
		return nil, err
	}
	if l, err := loadLocked(); err == nil {
		l.markSubagents(p)
	}
	if tmp != "" {
		// the import that follows uses this download, as a skill's does
		keepProbe("subagent:"+input, &Probe{Source: input, root: p.root, src: p.src}, tmp)
	}
	return p, nil
}

// ImportSubagents adds the subagents at those paths of input (every one
// with none named) and gives the new ones to the agents; one the library
// has from there is updated, keeping its name and agents.
func ImportSubagents(input string, paths, agents []string) (*Result, error) {
	input = strings.TrimSpace(input)
	p, cleanup, err := subagentProbeFor(input)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var picked []SubagentCandidate
	if len(paths) == 0 {
		picked = p.Candidates
	} else {
		for _, want := range paths {
			i := slices.IndexFunc(p.Candidates, func(c SubagentCandidate) bool { return c.Path == want })
			if i < 0 {
				return nil, fmt.Errorf("no subagent at %s there", want)
			}
			picked = append(picked, p.Candidates[i])
		}
	}
	if len(picked) == 0 {
		return nil, errors.New("no subagents to import")
	}
	given := []string{}
	for _, a := range agents {
		if slices.Contains(SubagentAgents, a) && !slices.Contains(given, a) {
			given = append(given, a)
		}
	}
	slices.Sort(given)
	done := &SubagentsImported{}
	res, err := change(func(l *Library) error {
		*done = SubagentsImported{}
		q := *p
		q.Candidates = picked
		l.markSubagents(&q)
		for _, c := range q.Candidates {
			name := c.Name
			if c.As != "" {
				name = c.As
			}
			switch c.State {
			case "have":
				done.Had = append(done.Had, name)
			case "edited":
				done.Kept = append(done.Kept, name)
			case "update":
				s := l.subagent(name)
				s.Description, s.Prompt = c.s.Description, c.s.Prompt
				s.From, s.FromHash = c.s.From.clone(), c.s.FromHash
				done.Updated = append(done.Updated, name)
			default:
				s := *c.s
				s.Name, s.Agents, s.From = name, slices.Clone(given), c.s.From.clone()
				l.Subagents = append(l.Subagents, &s)
				done.Added = append(done.Added, name)
			}
		}
		return nil
	})
	if res != nil {
		res.Subagents = done
	}
	return res, err
}

func (s *Source) clone() *Source {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

// subagentProbeFor is the probe of input from the download the page's
// probe kept, when it is still there; cleanup removes one made here.
func subagentProbeFor(input string) (*SubagentProbe, func(), error) {
	if dir := expand(input); !filepath.IsAbs(dir) {
		probes.Lock()
		e := probes.m["subagent:"+input]
		probes.Unlock()
		if e != nil {
			if _, err := os.Stat(e.tmp); err == nil {
				p := &SubagentProbe{Source: input, Kind: e.p.src.Kind, src: e.p.src, root: e.p.root}
				if err := p.read(); err == nil {
					return p, func() {}, nil
				}
			}
		}
	}
	p, tmp, err := findSubagentsAt(input)
	if err != nil {
		return nil, nil, err
	}
	return p, func() {
		if tmp != "" {
			os.RemoveAll(tmp)
		}
	}, nil
}

// findSubagentsAt fetches input and reads the subagents in it; tmp is the
// download, "" for a folder here.
func findSubagentsAt(input string) (p *SubagentProbe, tmp string, err error) {
	if input == "" {
		return nil, "", errors.New("paste a GitHub or git repository's link, or a folder's full path")
	}
	if strings.HasPrefix(input, "-") {
		return nil, "", errors.New("an address can't start with -")
	}
	p = &SubagentProbe{Source: input}
	if dir := expand(input); filepath.IsAbs(dir) {
		if _, err := os.Stat(dir); err != nil {
			return nil, "", fmt.Errorf("%s isn't a folder", input)
		}
		p.root, p.src = dir, Source{Kind: "folder", Dir: realDir(dir)}
	} else {
		root, t, src, err := fetchSourceRoot(input)
		if err != nil {
			return nil, "", err
		}
		if tooBig.has(src) {
			// only its skills' folders were fetched (skilltree.go)
			os.RemoveAll(t)
			return nil, "", fmt.Errorf("%s is too big to download whole: paste the link to the folder its subagents are in", src.Repo)
		}
		p.root, p.src, tmp = root, src, t
	}
	p.Kind = p.src.Kind
	if err := p.read(); err != nil {
		if tmp != "" {
			os.RemoveAll(tmp)
		}
		return nil, "", err
	}
	return p, tmp, nil
}

// read finds the subagents under p.root.
func (p *SubagentProbe) read() error {
	p.Candidates, p.Left = []SubagentCandidate{}, nil
	// the path in the repository (or the folder) of p.root
	base := p.src.Path
	if p.src.Kind == "folder" {
		base = ""
	}
	fi, err := os.Stat(p.root)
	if err != nil {
		return err
	}
	var files []string // slash paths under base
	if !fi.IsDir() {
		files = []string{""}
	} else {
		n := 0
		err = filepath.WalkDir(p.root, func(f string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(p.root, f)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel == "." {
					return nil
				}
				switch d.Name() {
				case ".git", ".github", "node_modules":
					return fs.SkipDir
				}
				if strings.Count(rel, "/") >= maxSubagentDepth {
					return fs.SkipDir
				}
				if _, err := os.Stat(filepath.Join(f, "SKILL.md")); err == nil {
					return fs.SkipDir // a skill's folder, its files the skill's
				}
				return nil
			}
			if !d.Type().IsRegular() || !strings.EqualFold(slash.Ext(rel), ".md") || strings.EqualFold(d.Name(), "SKILL.md") {
				return nil
			}
			if n++; n > maxSubagentFiles {
				return fs.SkipAll
			}
			files = append(files, rel)
			return nil
		})
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(p.root, "SKILL.md")); err == nil {
			files = nil // the folder is a skill
		}
	}
	// the shallowest file of a name is the one, then the first by path
	sort.SliceStable(files, func(i, j int) bool {
		if a, b := strings.Count(files[i], "/"), strings.Count(files[j], "/"); a != b {
			return a < b
		}
		return files[i] < files[j]
	})
	seen := map[string]string{}
	for _, rel := range files {
		at := slash.Join(base, rel)
		if rel == "" {
			if at = base; at == "" {
				at = filepath.Base(p.root) // a file here
			}
		}
		f := filepath.Join(p.root, filepath.FromSlash(rel))
		s, why := readSubagentAt(f)
		if s == nil && why == "" {
			continue // not a subagent
		}
		if s == nil {
			p.Left = append(p.Left, SubagentLeft{Path: at, Error: why})
			continue
		}
		if first, ok := seen[s.Name]; ok {
			p.Left = append(p.Left, SubagentLeft{Path: at, Error: "the same name as " + first})
			continue
		}
		seen[s.Name] = at
		from := p.src
		from.Path = at
		if p.src.Kind == "folder" {
			from.Dir = realDir(p.root)
			if !fi.IsDir() {
				from.Dir, from.Path = realDir(filepath.Dir(p.root)), filepath.Base(p.root)
			}
		}
		s.From, s.FromHash = &from, importHash(s)
		p.Candidates = append(p.Candidates, SubagentCandidate{Path: at, Name: s.Name, Description: s.Description, s: s})
	}
	sort.Slice(p.Candidates, func(i, j int) bool { return p.Candidates[i].Path < p.Candidates[j].Path })
	sort.Slice(p.Left, func(i, j int) bool { return p.Left[i].Path < p.Left[j].Path })
	if len(p.Candidates) == 0 && len(p.Left) == 0 {
		return errors.New("no subagents there: a subagent is a Markdown file whose front matter has its name and description")
	}
	return nil
}

// readSubagentAt is the subagent in a Markdown file; nil with no why for
// a file that isn't one.
func readSubagentAt(f string) (*Subagent, string) {
	fi, err := os.Stat(f)
	if err != nil {
		return nil, ""
	}
	if fi.Size() > maxSubagentFile {
		return nil, "the file is over 1 MB"
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, ""
	}
	front, body := splitFront(b)
	desc := strings.Join(strings.Fields(plStr(front["description"])), " ")
	if front == nil || desc == "" {
		return nil, ""
	}
	name := subagentName(plStr(front["name"]))
	if name == "" {
		// OpenCode's are named by their file, in its agents folder
		switch strings.ToLower(filepath.Base(filepath.Dir(f))) {
		case "agents", "droids":
			stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
			name = subagentName(strings.TrimSuffix(stem, ".agent"))
		}
		if name == "" {
			return nil, ""
		}
	}
	if utf8.RuneCountInString(desc) > maxSubagentDescription {
		desc = string([]rune(desc)[:maxSubagentDescription-1]) + "…"
	}
	s := &Subagent{
		Name:        name,
		Description: desc,
		Prompt:      strings.TrimRight(strings.TrimLeft(body, "\n"), " \n\t"),
		Agents:      []string{},
	}
	if len(s.Name) < 2 {
		return nil, "a name of one character"
	}
	if err := s.check(); err != nil {
		return nil, err.Error()
	}
	return s, ""
}

// importHash is what a subagent says, as it came from its file.
func importHash(s *Subagent) string {
	h := sha256.Sum256([]byte(s.Description + "\x00" + s.Prompt))
	return hex.EncodeToString(h[:8])
}

// sameFile is whether a subagent came from the file at from.
func (a *Source) sameFile(b *Source) bool {
	if a == nil || b == nil || a.Kind != b.Kind || a.Path != b.Path {
		return false
	}
	switch a.Kind {
	case "github":
		return strings.EqualFold(a.Repo, b.Repo)
	case "git":
		return sameGitRepo(a.Repo, b.Repo)
	case "folder":
		return realDir(a.Dir) == realDir(b.Dir)
	}
	return false
}

// markSubagents sets each candidate's state and name against l.
func (l *Library) markSubagents(p *SubagentProbe) {
	taken := map[string]bool{}
	for _, s := range l.Subagents {
		taken[s.Name] = true
	}
	for i := range p.Candidates {
		c := &p.Candidates[i]
		c.State, c.As = "", ""
		var had *Subagent
		for _, s := range l.Subagents {
			if s.From.sameFile(c.s.From) {
				had = s
				break
			}
		}
		if had != nil {
			switch {
			case importHash(had) == c.s.FromHash:
				c.State = "have"
			case importHash(had) == had.FromHash:
				c.State = "update"
			default:
				c.State = "edited"
			}
			if had.Name != c.Name {
				c.As = had.Name
			}
			continue
		}
		name := c.Name
		for n := 2; taken[name]; n++ {
			suffix := "-" + strconv.Itoa(n)
			name = strings.Trim(c.Name[:min(len(c.Name), 50-len(suffix))], "-") + suffix
		}
		taken[name] = true
		if name != c.Name {
			c.As = name
		}
	}
}
