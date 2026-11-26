package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// A project can be given instructions of its own (VictorHong1022 on X), so
// the user-wide ones keep only what holds everywhere: written into the
// file each picked agent reads a project's instructions from, between the
// same two marker lines the user-wide part has (instructions.go). The rest
// of the file is the user's and is never touched; turned off, the part
// comes out, and a file magpie made goes once nothing is left in it.
//
// The names each agent reads, in a project's folder, the first one there
// being the one it reads (and the first name the one made when none is):
//
//	Claude Code   CLAUDE.md, .claude/CLAUDE.md          code.claude.com/docs/en/memory
//	Codex         AGENTS.override.md over AGENTS.md     Codex docs, AGENTS.md discovery
//	Pi            AGENTS.override.md, AGENTS.md, AGENTS.MD, CLAUDE.md, CLAUDE.MD
//	              (loadContextFileFromDir, @earendil-works/pi-coding-agent)
//	OpenCode      AGENTS.md, else CLAUDE.md             session/instruction.ts
//	Gemini CLI    GEMINI.md, or its settings' context.fileName
//	Qwen Code     QWEN.md, or its settings' context.fileName
//	Qoder         AGENTS.md, or its settings' context.fileName
//	CodeBuddy     CODEBUDDY.md
//	Cursor, Copilot CLI, Kimi Code, Droid, Crush, Grok Build, Cline,
//	Command Code, Goose and Dim read AGENTS.md at the project's root
//	(Dim: the nearest AGENTS.md above the folder it runs in,
//	read_instructions in arcships/dim's prompt_sources.rs).
//
// A file several picked agents read is written once.
var projectInstrNames = map[string][]string{
	"claude":      {"CLAUDE.md", ".claude/CLAUDE.md"},
	"codex":       {"AGENTS.md", "AGENTS.override.md"},
	"pi":          {"AGENTS.md", "AGENTS.override.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"},
	"opencode":    {"AGENTS.md", "CLAUDE.md"},
	"gemini":      {"GEMINI.md"},
	"qwen":        {"QWEN.md"},
	"qoder":       {"AGENTS.md"},
	"qoder-cn":    {"AGENTS.md"},
	"codebuddy":   {"CODEBUDDY.md"},
	"cursor":      {"AGENTS.md"},
	"copilot":     {"AGENTS.md"},
	"kimi":        {"AGENTS.md"},
	"droid":       {"AGENTS.md"},
	"crush":       {"AGENTS.md"},
	"grok":        {"AGENTS.md"},
	"cline":       {"AGENTS.md"},
	"commandcode": {"AGENTS.md"},
	"goose":       {"AGENTS.md"},
	"dim":         {"AGENTS.md"},
}

// the order an agent looks for its names in, where that isn't the order
// above (whose first is the one made): an override is read in place of
// AGENTS.md when it is there
var projectInstrOrder = map[string][]string{
	"codex": {"AGENTS.override.md", "AGENTS.md"},
	"pi":    {"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"},
}

// ProjectInstrFile is the file, in a project, an agent reads the
// project's instructions from when there is none yet: "" for one magpie
// can't give a project's instructions to.
func ProjectInstrFile(agentID string) string {
	if n := projectInstrNames[agentID]; len(n) > 0 {
		return n[0]
	}
	return ""
}

// projectInstrRel is the file, relative to dir, agent id reads the
// project's instructions from there: the first of its names there is,
// else the first. renamed is the name its settings' context.fileName
// gives (Gemini CLI, Qwen Code, Qoder), "" for none.
func projectInstrRel(dir, id, renamed string) string {
	names := projectInstrNames[id]
	if renamed != "" {
		return filepath.ToSlash(renamed)
	}
	if len(names) == 0 {
		return ""
	}
	order := projectInstrOrder[id]
	if order == nil {
		order = names
	}
	for _, n := range order {
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(n))); err == nil && !fi.IsDir() {
			return n
		}
	}
	return names[0]
}

// projectInstrRenames are, for the picked agents whose settings can name
// their instructions file, the name there; found only when one is picked,
// as finding the agents looks through every agent's folders.
func projectInstrRenames(agents []string) map[string]string {
	out := map[string]string{}
	if !slices.ContainsFunc(agents, func(id string) bool {
		return id == "gemini" || id == "qwen" || id == "qoder" || id == "qoder-cn"
	}) {
		return out
	}
	for _, t := range everyTarget() {
		id := t.Agent.ID
		if !slices.Contains(agents, id) || t.Instructions == "" {
			continue
		}
		settings := t.Agent.Path
		if id == "gemini" && t.MCP != nil {
			settings = t.MCP.Path
		}
		def := ProjectInstrFile(id)
		if n := contextFile(settings, def); n != def {
			out[id] = n
		}
	}
	return out
}

// InstrFile is a project file magpie wrote the project's instructions
// into: the hash of the part as written, and whether magpie made the file.
type InstrFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Made bool   `json:"made,omitempty"`
}

// syncProjectInstr writes the project's instructions into the file of
// each agent picked for them, and takes them out of a file no longer
// wanted. A part the user edited in the file is left alone until the
// project's text changes; one taken out of the file is written again.
func (l *Library) syncProjectInstr(p *Project, fail func(what string, err error)) {
	text := markersOut(strings.TrimSpace(strings.ReplaceAll(p.Instr, "\r\n", "\n")))
	wanted := map[string]bool{}
	if text != "" {
		renames := projectInstrRenames(p.InstrAgents)
		for _, id := range p.InstrAgents {
			if rel := projectInstrRel(p.Dir, id, renames[id]); rel != "" {
				wanted[rel] = true
			}
		}
	}
	rec := map[string]InstrFile{}
	for _, f := range p.InstrFiles {
		rec[f.Path] = f
	}
	var globals map[string]bool // the agents' user-wide files, found once
	var kept []InstrFile
	// what's no longer wanted: magpie's part out, and a file it made away
	// once nothing of the user's is in it
	for _, f := range p.InstrFiles {
		if wanted[f.Path] {
			continue
		}
		abs := filepath.Join(p.Dir, filepath.FromSlash(f.Path))
		if err := p.inside(abs); err != nil {
			fail("instructions", err)
			kept = append(kept, f)
			continue
		}
		raw, err := os.ReadFile(abs)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			fail("instructions", err)
			kept = append(kept, f)
			continue
		}
		file := string(raw)
		if _, _, _, ok := split(file); !ok {
			continue // the user took it out themselves
		}
		rest := own(strings.ReplaceAll(file, "\r\n", "\n"))
		if rest == "" && f.Made {
			err = os.Remove(abs)
		} else {
			err = edit.WriteAtomic(abs, []byte(withEnds(withBlock(file, ""), file)))
		}
		if err != nil {
			fail("instructions", err)
			kept = append(kept, f)
		}
	}
	for _, rel := range slices.Sorted(func(yield func(string) bool) {
		for r := range wanted {
			if !yield(r) {
				return
			}
		}
	}) {
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		r, had := rec[rel]
		if globals == nil {
			globals = l.globalInstrFiles()
		}
		if err := p.inside(abs); err != nil {
			fail("instructions", err)
			if had {
				kept = append(kept, r)
			}
			continue
		}
		if globals[realBelow(abs)] {
			fail("instructions", fmt.Errorf("%s is an agent's user-wide instructions file, which magpie writes from the shared instructions: it's left as it is", rel))
			if had {
				kept = append(kept, r)
			}
			continue
		}
		raw, err := os.ReadFile(abs)
		made := r.Made
		switch {
		case errors.Is(err, os.ErrNotExist):
			made = !had || r.Made
		case err != nil:
			fail("instructions", err)
			if had {
				kept = append(kept, r)
			}
			continue
		}
		file := string(raw)
		_, have, _, ok := split(file)
		switch {
		case ok && have == text:
			kept = append(kept, InstrFile{Path: rel, Hash: hash(text), Made: made})
			continue
		case ok && had && hash(have) != r.Hash && hash(text) == r.Hash:
			// edited in the file since, and the project's text hasn't
			// changed: the user's edit stays until it does
			kept = append(kept, r)
			continue
		}
		if raw == nil {
			if err := p.mkdirs(filepath.Dir(abs)); err != nil {
				fail("instructions", err)
				continue
			}
		}
		if err := edit.WriteAtomic(abs, []byte(withEnds(withBlock(file, text), file))); err != nil {
			fail("instructions", err)
			if had {
				kept = append(kept, r)
			}
			continue
		}
		kept = append(kept, InstrFile{Path: rel, Hash: hash(text), Made: made})
	}
	slices.SortFunc(kept, func(x, y InstrFile) int { return strings.Compare(x.Path, y.Path) })
	p.InstrFiles = kept
}

// withEnds gives next the line ends the file it was made from has: a
// file of the user's written with CRLF keeps them.
func withEnds(next, file string) string {
	if strings.Contains(file, "\r\n") {
		return strings.ReplaceAll(next, "\n", "\r\n")
	}
	return next
}

// globalInstrFiles are, links followed, the agents' user-wide
// instructions files and the file the shared set is kept in: a project
// file that is one of them (a project at the home folder, or a file
// linked to one) would have the project's part and the shared one fight
// over the same lines, so it is left alone.
func (l *Library) globalInstrFiles() map[string]bool {
	out := map[string]bool{realBelow(l.sharedPath()): true}
	for _, t := range everyTarget() {
		if t.Instructions != "" {
			out[realBelow(t.Instructions)] = true
		}
	}
	return out
}

// ProjectInstr is a change to a project's instructions: its text, the
// agents it is written for, or both; nil leaves that as it is.
type ProjectInstr struct {
	Text   *string
	Agents *[]string
}

// ProjectInstructions sets a project's own instructions, and the agents
// whose project file they are written into; no agents, or no text, takes
// them out of the project's files.
func ProjectInstructions(dir string, c ProjectInstr) (*Result, error) {
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		if c.Agents != nil {
			var ids []string
			for _, id := range *c.Agents {
				if ProjectInstrFile(id) == "" {
					return fmt.Errorf("magpie knows of no file %s reads a project's instructions from", id)
				}
				ids = set(ids, id, true)
			}
			p.InstrAgents = ids
		}
		if c.Text != nil {
			p.Instr = strings.TrimSpace(strings.ReplaceAll(*c.Text, "\r\n", "\n"))
		}
		return nil
	})
}
