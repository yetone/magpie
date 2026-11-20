package library

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// A project's choices carried to another folder, on this computer or
// another (#7278): magpie library project export writes them, apply gives
// them to a folder named there. Only what the project wants travels: the
// library's skills, servers, subagents, hooks and plugins it has and for
// which agents, the Claude Code plugins and Pi packages it turns on, its
// own instructions. What magpie placed and wrote in the first folder (its
// ownership records) stays behind: the second folder's are made there, by
// the same writers the Library page uses. No key, sign-in or other project
// goes with it.
//
// What the choices need from the library goes along too, as far as it can
// without a secret: a hook's and a subagent's definition, a skill's and a
// plugin's source, a server's name only (its settings can hold a key). A
// hook or subagent the other library lacks is saved there from that, given
// to no agent but the project's; anything else it lacks is named, with its
// source, and left out.

// ProjectExportKind is what an export file says it is.
const ProjectExportKind = "magpie-project"

type ProjectExport struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`
	// Dir is the folder it was taken from, for the reader alone: apply
	// gives the choices to the folder it's told
	Dir           string              `json:"dir,omitempty"`
	Copy          bool                `json:"copy,omitempty"`
	Skills        map[string][]string `json:"skills,omitempty"`
	Servers       map[string][]string `json:"servers,omitempty"`
	Subagents     map[string][]string `json:"subagents,omitempty"`
	Hooks         map[string][]string `json:"hooks,omitempty"`
	Plugins       map[string][]string `json:"plugins,omitempty"`
	ClaudePlugins []string            `json:"claudePlugins,omitempty"`
	Pi            map[string]string   `json:"pi,omitempty"`
	ServersOff    map[string][]string `json:"serversOff,omitempty"`
	SkillsOff     map[string][]string `json:"skillsOff,omitempty"`
	Instr         string              `json:"instr,omitempty"`
	InstrAgents   []string            `json:"instrAgents,omitempty"`
	Need          ExportNeeds         `json:"need"`
}

// ExportNeeds are the library's things an export's choices name.
type ExportNeeds struct {
	// Skills and Plugins are their sources, "" for one kept only here
	Skills  map[string]string `json:"skills,omitempty"`
	Plugins map[string]string `json:"plugins,omitempty"`
	// Bundles are the plugins the hooks and subagents named came with,
	// which bring them
	Hooks     []ExportHook     `json:"hooks,omitempty"`
	Subagents []ExportSubagent `json:"subagents,omitempty"`
}

// ExportHook is a hook as an export carries it: a plugin's names its
// plugin, which brings it, and nothing else.
type ExportHook struct {
	Name        string `json:"name"`
	Bundle      string `json:"bundle,omitempty"`
	Description string `json:"description,omitempty"`
	Event       string `json:"event,omitempty"`
	Matcher     string `json:"matcher,omitempty"`
	Command     string `json:"command,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

// ExportSubagent is a subagent as an export carries it.
type ExportSubagent struct {
	Name        string   `json:"name"`
	Bundle      string   `json:"bundle,omitempty"`
	Description string   `json:"description,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
	Model       string   `json:"model,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	MCP         []string `json:"mcp,omitempty"`
}

// ExportProject is a project's choices and what they need of the library.
func ExportProject(dir string) (*ProjectExport, error) {
	l, err := loadLocked()
	if err != nil {
		return nil, err
	}
	l.followSubagentFiles()
	p := l.project(dir)
	if p == nil {
		return nil, fmt.Errorf("no project %s", dir)
	}
	e := &ProjectExport{
		Kind: ProjectExportKind, Version: 1, Dir: p.Dir, Copy: p.Copy,
		Skills: maps.Clone(p.Skills), Servers: maps.Clone(p.Servers),
		Subagents: maps.Clone(p.Subagents), Hooks: maps.Clone(p.Hooks),
		Plugins: maps.Clone(p.Plugins), ClaudePlugins: slices.Clone(p.ClaudePlugins),
		Pi: maps.Clone(p.Pi), ServersOff: maps.Clone(p.ServersOff), SkillsOff: maps.Clone(p.SkillsOff),
		Instr: p.Instr, InstrAgents: slices.Clone(p.InstrAgents),
	}
	for _, n := range slices.Sorted(maps.Keys(p.Skills)) {
		if e.Need.Skills == nil {
			e.Need.Skills = map[string]string{}
		}
		e.Need.Skills[n] = ""
		if s := l.skill(n); s != nil && s.Source.remote() {
			e.Need.Skills[n] = s.Source.String()
		}
	}
	for _, n := range slices.Sorted(maps.Keys(p.Plugins)) {
		if e.Need.Plugins == nil {
			e.Need.Plugins = map[string]string{}
		}
		e.Need.Plugins[n] = ""
		if pl := l.plugin(n); pl != nil && !isLocalPath(pl.Source) {
			e.Need.Plugins[n] = pl.Source
		}
	}
	for _, n := range slices.Sorted(maps.Keys(p.Hooks)) {
		h := l.hook(n)
		if h == nil {
			continue
		}
		x := ExportHook{Name: h.Name, Bundle: h.Bundle}
		if h.Bundle == "" {
			x.Description, x.Event, x.Matcher, x.Command, x.Timeout = h.Description, h.Event, h.Matcher, h.Command, h.Timeout
		}
		e.Need.Hooks = append(e.Need.Hooks, x)
	}
	for _, n := range slices.Sorted(maps.Keys(p.Subagents)) {
		s := l.subagent(n)
		if s == nil {
			continue
		}
		x := ExportSubagent{Name: s.Name, Bundle: s.Bundle}
		if s.Bundle == "" {
			x.Description, x.Prompt, x.Model = s.Description, s.Prompt, s.Model
			x.Skills, x.MCP = slices.Clone(s.Skills), slices.Clone(s.MCP)
		}
		e.Need.Subagents = append(e.Need.Subagents, x)
	}
	return e, nil
}

// isLocalPath is whether a source names a folder on this computer, which
// another can't reach.
func isLocalPath(s string) bool {
	return s == "" || filepath.IsAbs(s) || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") ||
		len(s) > 1 && s[1] == ':' // C:\…
}

// ProjectApplied is what ApplyProject did: Saved the hooks and subagents it put
// in the library from the export, Missing what it left out and why, Res
// each write's outcome.
type ProjectApplied struct {
	Saved   []string
	Missing []string
	Res     []*Result
}

// ApplyProject gives a folder the choices of an export, adding it as a
// project first when it isn't one. A choice the folder has already keeps
// its agents and gains the export's; nothing it has is taken away, so
// applying again changes nothing.
func ApplyProject(dir string, e *ProjectExport) (*ProjectApplied, error) {
	if e == nil || e.Kind != ProjectExportKind {
		return nil, errors.New("not a project export (magpie library project export writes one)")
	}
	if e.Version != 1 {
		return nil, fmt.Errorf("a project export of version %d, newer than this magpie reads", e.Version)
	}
	out := &ProjectApplied{}
	add := func(res *Result, err error, what string) {
		if res != nil {
			out.Res = append(out.Res, res)
		}
		if err != nil {
			out.Missing = append(out.Missing, what+": "+err.Error())
		}
	}
	if at, err := ProjectAt(dir); err != nil {
		return nil, err
	} else if at == "" {
		res, err := AddProject(dir)
		if err != nil {
			return nil, err
		}
		out.Res = append(out.Res, res)
	}
	l, err := loadLocked()
	if err != nil {
		return nil, err
	}
	// the hooks and subagents the export defines and this library lacks
	for _, h := range e.Need.Hooks {
		if l.hook(h.Name) != nil || h.Bundle != "" || h.Command == "" {
			continue
		}
		res, err := SaveHook("", Hook{Name: h.Name, Description: h.Description, Event: h.Event, Matcher: h.Matcher, Command: h.Command, Timeout: h.Timeout})
		add(res, err, "hook "+h.Name)
		if err == nil {
			out.Saved = append(out.Saved, "hook "+h.Name)
		}
	}
	for _, s := range e.Need.Subagents {
		if l.subagent(s.Name) != nil || s.Bundle != "" || s.Prompt == "" {
			continue
		}
		res, err := SaveSubagent("", Subagent{Name: s.Name, Description: s.Description, Prompt: s.Prompt, Model: s.Model, Skills: s.Skills, MCP: s.MCP, Agents: []string{}})
		add(res, err, "subagent "+s.Name)
		if err == nil {
			out.Saved = append(out.Saved, "subagent "+s.Name)
		}
	}
	if l, err = loadLocked(); err != nil {
		return nil, err
	}
	p := l.project(dir)
	if p == nil {
		return nil, fmt.Errorf("no project %s", dir)
	}
	// the export's agents, and those the folder has the thing for already
	both := func(have map[string][]string, name string, want []string) []string {
		a := slices.Clone(have[name])
		for _, id := range want {
			a = set(a, id, true)
		}
		return a
	}
	lacks := func(kind, name, source string) {
		m := fmt.Sprintf("%s %s: not in this library", kind, name)
		if source != "" {
			m += " (install it from " + source + ")"
		}
		out.Missing = append(out.Missing, m)
	}
	if e.Copy && !p.Copy {
		res, err := ProjectCopy(dir, true)
		add(res, err, "copies")
	}
	for _, n := range slices.Sorted(maps.Keys(e.Skills)) {
		if l.skill(n) == nil {
			lacks("skill", n, e.Need.Skills[n])
			continue
		}
		res, err := ProjectSkills(dir, []string{n}, both(p.Skills, n, e.Skills[n]))
		add(res, err, "skill "+n)
	}
	for _, n := range slices.Sorted(maps.Keys(e.Servers)) {
		if l.server(n) == nil {
			lacks("server", n, "")
			continue
		}
		res, err := ProjectServer(dir, n, both(p.Servers, n, e.Servers[n]))
		add(res, err, "server "+n)
	}
	for _, n := range slices.Sorted(maps.Keys(e.Plugins)) {
		if l.plugin(n) == nil {
			lacks("plugin", n, e.Need.Plugins[n])
			continue
		}
		res, err := ProjectPlugin(dir, n, both(p.Plugins, n, e.Plugins[n]))
		add(res, err, "plugin "+n)
	}
	bundle := func(kind, n string) string {
		for _, h := range e.Need.Hooks {
			if kind == "hook" && h.Name == n && h.Bundle != "" {
				return " (it comes with the plugin " + h.Bundle + ")"
			}
		}
		for _, s := range e.Need.Subagents {
			if kind == "subagent" && s.Name == n && s.Bundle != "" {
				return " (it comes with the plugin " + s.Bundle + ")"
			}
		}
		return ""
	}
	for _, n := range slices.Sorted(maps.Keys(e.Subagents)) {
		if l.subagent(n) == nil {
			out.Missing = append(out.Missing, "subagent "+n+": not in this library"+bundle("subagent", n))
			continue
		}
		res, err := ProjectSubagents(dir, []string{n}, both(p.Subagents, n, e.Subagents[n]))
		add(res, err, "subagent "+n)
	}
	for _, n := range slices.Sorted(maps.Keys(e.Hooks)) {
		if l.hook(n) == nil {
			out.Missing = append(out.Missing, "hook "+n+": not in this library"+bundle("hook", n))
			continue
		}
		res, err := ProjectHooks(dir, []string{n}, both(p.Hooks, n, e.Hooks[n]))
		add(res, err, "hook "+n)
	}
	for _, id := range e.ClaudePlugins {
		if slices.Contains(p.ClaudePlugins, id) {
			continue
		}
		res, err := ProjectClaudePlugin(dir, id, true)
		add(res, err, "Claude Code plugin "+id)
	}
	for _, src := range slices.Sorted(maps.Keys(e.Pi)) {
		if _, ok := p.Pi[src]; ok {
			continue // the folder's own say stays
		}
		res, err := ProjectPi(dir, src, e.Pi[src])
		add(res, err, "Pi package "+src)
	}
	for _, n := range slices.Sorted(maps.Keys(e.ServersOff)) {
		if l.server(n) == nil {
			continue // nothing here to turn off
		}
		res, err := ProjectServerOff(dir, n, both(p.ServersOff, n, e.ServersOff[n]))
		add(res, err, "server "+n+" off")
	}
	for _, n := range slices.Sorted(maps.Keys(e.SkillsOff)) {
		if l.skill(n) == nil {
			continue
		}
		res, err := ProjectSkillsOff(dir, []string{n}, both(p.SkillsOff, n, e.SkillsOff[n]))
		add(res, err, "skill "+n+" off")
	}
	if e.Instr != "" && p.Instr == "" {
		text, agents := e.Instr, slices.Clone(e.InstrAgents)
		res, err := ProjectInstructions(dir, ProjectInstr{Text: &text, Agents: &agents})
		add(res, err, "instructions")
	}
	return out, nil
}
