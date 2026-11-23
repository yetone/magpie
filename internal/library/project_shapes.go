package library

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
)

// A project's claudePlugins and pi read in the shapes another build of
// magpie wrote them in too (#7282, jinpwng): claudePlugins as a map of
// plugin id to on (true) or off, pi as a map of source to the agents it is
// for. One of them used to fail the whole library's read, so nothing could
// be changed. They're read as magpie's own shapes: a plugin on is in the
// list, one off is left out, as magpie turns on only and writes nothing for
// it; a Pi source for some agent is "on", for none "off". The file is
// untouched until magpie next writes it, and then the bytes it had are
// kept beside it first, as library.json.before-reshape (reshapedCopy).
//
// A build that kept pi by category (v0.1.4517+r11) wrote
// {"package": [source, ...], "extension": [path, ...]} with the packages
// turned off in piOff (#7282, jinpwng). Read as a map of source to agents,
// its two category names were two package sources, and v0.1.4702's first
// write put the literal strings "package" and "extension" in the
// project's .pi/settings.json, and its worktrees'. Each package source is
// now read with its state, and a standalone extension, which magpie has
// no place for, is kept as PiNotCarried and shown, never written. The two
// literals a library written by v0.1.4702 holds are taken out of it again
// (piStrays), so the next sync takes the entries it wrote out of the
// projects' files, and the sources are read back from the copy kept
// before that write (strayPiBack).
func (p *Project) UnmarshalJSON(b []byte) error {
	type plain Project
	x := struct {
		*plain
		ClaudePlugins json.RawMessage `json:"claudePlugins,omitempty"`
		Pi            json.RawMessage `json:"pi,omitempty"`
		PiOff         json.RawMessage `json:"piOff,omitempty"`
	}{plain: (*plain)(p)}
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	p.ClaudePlugins, p.Pi = nil, nil
	if on, other, err := readClaudePlugins(x.ClaudePlugins); err != nil {
		return fmt.Errorf("project %s: claudePlugins: %w", p.Dir, err)
	} else {
		p.ClaudePlugins, p.reshaped = on, p.reshaped || other
	}
	if pi, left, other, err := readProjectPi(x.Pi, x.PiOff); err != nil {
		return fmt.Errorf("project %s: pi: %w", p.Dir, err)
	} else {
		p.Pi, p.reshaped = pi, p.reshaped || other
		if len(left) > 0 {
			p.PiNotCarried = left
		}
		if other && len(pi) > 0 {
			// sources another build declared in the project's file: the
			// project's own entries, which the next sync adopts
			p.piFromOld = map[string]bool{}
			for s := range pi {
				p.piFromOld[s] = true
			}
		}
	}
	if !p.reshaped {
		p.dropPiStrays()
	}
	return nil
}

// piCategories are the keys a category-keyed pi has (v0.1.4517+r11).
var piCategories = []string{"package", "extension"}

// PiNotCarried is an entry another build kept in a project's pi that
// magpie has no place for: Kind "extension", a standalone extension's
// path. It is kept in library.json as read and shown with the project;
// its line in .pi/settings.json, if any, stays the user's.
type PiNotCarried struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

// dropPiStrays takes the literals "package" and "extension" out of a pi
// in magpie's shape: no Pi source is named so (ProjectPi refuses both),
// and only v0.1.4702's read of a category-keyed pi put them there. The
// next sync takes the entries it wrote for them (PiRecs) out of the
// project's .pi/settings.json, as for any source no longer wanted.
func (p *Project) dropPiStrays() {
	for _, k := range piCategories {
		if st, ok := p.Pi[k]; ok && (st == "on" || st == "off") {
			delete(p.Pi, k)
			p.piStrays = true
		}
	}
	if p.piStrays && len(p.Pi) == 0 {
		p.Pi = nil
	}
}

func nullish(b json.RawMessage) bool { return len(b) == 0 || string(b) == "null" }

// readClaudePlugins: a list of ids, or a map of id to whether it's on
func readClaudePlugins(b json.RawMessage) (on []string, other bool, err error) {
	if nullish(b) {
		return nil, false, nil
	}
	if json.Unmarshal(b, &on) == nil {
		return on, false, nil
	}
	var m map[string]bool
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false, fmt.Errorf("neither a list of plugin ids nor a map of id to on")
	}
	for id, yes := range m {
		if yes {
			on = append(on, id)
		}
	}
	sort.Strings(on)
	return on, true, nil
}

// readProjectPi: a map of source to "on" or "off", or to the agents it is
// for (a list), or to on (a boolean), or of category to its sources, with
// off the packages turned off
func readProjectPi(b, off json.RawMessage) (pi map[string]string, left []PiNotCarried, other bool, err error) {
	if nullish(b) {
		return nil, nil, false, nil
	}
	if json.Unmarshal(b, &pi) == nil {
		return pi, nil, false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, false, fmt.Errorf("not a map of Pi source to its state")
	}
	if pi, left, ok := readPiCategories(m, off); ok {
		return pi, left, true, nil
	}
	pi = make(map[string]string, len(m))
	for src, v := range m {
		var s string
		var agents []string
		var yes bool
		switch {
		case json.Unmarshal(v, &s) == nil:
			pi[src] = s
		case json.Unmarshal(v, &agents) == nil:
			pi[src] = map[bool]string{true: "on", false: "off"}[len(slices.DeleteFunc(agents, func(a string) bool { return a == "" })) > 0]
		case json.Unmarshal(v, &yes) == nil:
			pi[src] = map[bool]string{true: "on", false: "off"}[yes]
		default:
			return nil, nil, false, fmt.Errorf("%s: %s is neither a state, a list of agents nor on/off", src, v)
		}
	}
	return pi, nil, true, nil
}

// readPiCategories reads a pi keyed by category: every key is "package"
// or "extension", each a list of sources. A package is on unless off
// lists it; an extension is not carried. ok is false for any other map.
func readPiCategories(m map[string]json.RawMessage, off json.RawMessage) (pi map[string]string, left []PiNotCarried, ok bool) {
	if len(m) == 0 {
		return nil, nil, false
	}
	lists := map[string][]string{}
	for k, v := range m {
		if !slices.Contains(piCategories, k) {
			return nil, nil, false
		}
		var srcs []string
		if json.Unmarshal(v, &srcs) != nil {
			return nil, nil, false
		}
		lists[k] = srcs
	}
	var offs []string
	_ = json.Unmarshal(off, &offs) // anything but a list turns none off
	pi = map[string]string{}
	for _, s := range lists["package"] {
		if s == "" || slices.Contains(piCategories, s) {
			continue
		}
		pi[s] = "on"
		if slices.Contains(offs, s) {
			pi[s] = "off"
		}
	}
	for _, s := range lists["extension"] {
		if s != "" {
			left = append(left, PiNotCarried{Kind: "extension", Source: s})
		}
	}
	sort.Slice(left, func(i, j int) bool { return left[i].Source < left[j].Source })
	if len(pi) == 0 {
		pi = nil
	}
	return pi, left, true
}

// strayPiBack reads the sources of each project whose pi held v0.1.4702's
// literals (dropPiStrays) back from the copy of the library kept before
// that build's first write, when that copy has them by category: they
// are the project's packages, and PiNotCarried what isn't carried. A
// source the project has since is left as it is. Without the copy, the
// literals still go and the project keeps the sources it has.
func (l *Library) strayPiBack() {
	var strays []*Project
	for _, p := range l.Projects {
		if p.piStrays {
			strays = append(strays, p)
		}
	}
	if len(strays) == 0 {
		return
	}
	b, err := os.ReadFile(path() + ".before-reshape")
	if err != nil {
		return
	}
	var old struct {
		Projects []*Project `json:"projects"`
	}
	if json.Unmarshal(b, &old) != nil {
		return
	}
	for _, p := range strays {
		i := slices.IndexFunc(old.Projects, func(o *Project) bool { return o.Dir == p.Dir })
		if i < 0 || !old.Projects[i].reshaped {
			continue
		}
		o := old.Projects[i]
		for s, st := range o.Pi {
			if _, had := p.Pi[s]; had {
				continue
			}
			if p.Pi == nil {
				p.Pi = map[string]string{}
			}
			p.Pi[s] = st
			if p.piFromOld == nil {
				p.piFromOld = map[string]bool{}
			}
			p.piFromOld[s] = true
		}
		if len(p.PiNotCarried) == 0 {
			p.PiNotCarried = o.PiNotCarried
		}
	}
}

// reshapedCopy keeps the library file's bytes before the first write that
// would put a project read in another shape (Project.UnmarshalJSON) in
// magpie's own, unless a copy is there already. A copy that can't be made
// stops the write: the file stays as it was.
func (l *Library) reshapedCopy() error {
	// a worktree's record is read the same way as its project's
	if !slices.ContainsFunc(l.Projects, func(p *Project) bool {
		return p.reshaped || slices.ContainsFunc(p.Trees, func(t *Project) bool { return t.reshaped })
	}) {
		return nil
	}
	to := path() + ".before-reshape"
	if _, err := os.Stat(to); err == nil {
		return nil
	}
	b, err := os.ReadFile(path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(to, b, 0o600)
}
