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
func (p *Project) UnmarshalJSON(b []byte) error {
	type plain Project
	x := struct {
		*plain
		ClaudePlugins json.RawMessage `json:"claudePlugins,omitempty"`
		Pi            json.RawMessage `json:"pi,omitempty"`
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
	if pi, other, err := readProjectPi(x.Pi); err != nil {
		return fmt.Errorf("project %s: pi: %w", p.Dir, err)
	} else {
		p.Pi, p.reshaped = pi, p.reshaped || other
	}
	return nil
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
// for (a list), or to on (a boolean)
func readProjectPi(b json.RawMessage) (pi map[string]string, other bool, err error) {
	if nullish(b) {
		return nil, false, nil
	}
	if json.Unmarshal(b, &pi) == nil {
		return pi, false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false, fmt.Errorf("not a map of Pi source to its state")
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
			return nil, false, fmt.Errorf("%s: %s is neither a state, a list of agents nor on/off", src, v)
		}
	}
	return pi, true, nil
}

// reshapedCopy keeps the library file's bytes before the first write that
// would put a project read in another shape (Project.UnmarshalJSON) in
// magpie's own, unless a copy is there already. A copy that can't be made
// stops the write: the file stays as it was.
func (l *Library) reshapedCopy() error {
	if !slices.ContainsFunc(l.Projects, func(p *Project) bool { return p.reshaped }) {
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
