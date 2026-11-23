package library

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// OwnResource is something the project's own files already give an agent,
// that magpie didn't place (#7245, #7247): shown read-only, never changed
// or taken away. Credentials never go in it: a server's URL, arguments and
// environment stay in the project's file, only its transport is read.
type OwnResource struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Agent       string `json:"agent,omitempty"`
	Event       string `json:"event,omitempty"`
	Description string `json:"description,omitempty"`
	Transport   string `json:"transport,omitempty"`
}

// ownResources reads, by kind (skills, servers, hooks, subagents), what the
// project's folder has that magpie didn't write. A file it can't read or a
// link out of the folder is left out: unknown, never a wrong list.
func (p *Project) ownResources() map[string][]OwnResource {
	out := map[string][]OwnResource{}
	var dirs []string
	for _, rel := range projectSkillsDirs {
		if !slices.Contains(dirs, rel) {
			dirs = append(dirs, rel)
		}
	}
	sort.Strings(dirs)
	for _, rel := range dirs {
		out["skills"] = append(out["skills"], p.ownSkills(rel)...)
	}
	ids := make([]string, 0, len(projectMCPFiles))
	for id := range projectMCPFiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen := map[string]bool{}
	for _, id := range ids {
		rel, format := p.mcpRel(id)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		out["servers"] = append(out["servers"], p.ownServers(id, rel, format)...)
	}
	for _, id := range []string{"agy", "claude", "codex"} {
		out["hooks"] = append(out["hooks"], p.ownHooks(id)...)
	}
	ids = ids[:0]
	for id := range projectSubagentDirs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out["subagents"] = append(out["subagents"], p.ownSubagents(id)...)
	}
	out["pi"] = p.ownPi()
	for k, rows := range out {
		if len(rows) == 0 {
			delete(out, k)
		}
	}
	return out
}

func (p *Project) ownSkills(rel string) []OwnResource {
	dir := filepath.Join(p.Dir, filepath.FromSlash(rel))
	if p.inside(dir) != nil {
		return nil
	}
	var out []OwnResource
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		path := rel + "/" + e.Name()
		abs := filepath.Join(dir, e.Name())
		if slices.Contains(p.Placed, path) || ours(abs, e.Name()) || p.inside(abs) != nil {
			continue
		}
		if m, ok := readMeta(abs); ok {
			name := e.Name()
			if m.Name != "" {
				name = m.Name
			}
			out = append(out, OwnResource{Name: name, Path: path, Description: m.Description})
		}
	}
	return out
}

func (p *Project) ownServers(id, rel string, format mcpFormat) []OwnResource {
	abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
	if p.inside(abs) != nil || !exists(abs) {
		return nil
	}
	f := &mcpFile{Path: abs, Format: format}
	entries, err := f.entries()
	if err != nil {
		return nil
	}
	var out []OwnResource
	for name, entry := range entries {
		if slices.Contains(p.Wrote[rel], name) || id == "codex" && slices.Contains(p.CodexOff, name) {
			continue
		}
		row := OwnResource{Name: name, Path: rel, Agent: id}
		if s, ok := f.decode(name, entry); ok {
			row.Transport = s.Transport
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (p *Project) ownHooks(id string) []OwnResource {
	rels := []string{projectHookFiles[id]}
	if id == "claude" {
		rels = []string{".claude/settings.json", projectHookFiles[id]}
	}
	var out []OwnResource
	for _, rel := range rels {
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if p.inside(abs) != nil || !exists(abs) {
			continue
		}
		doc, err := readHooksFile(abs)
		if err != nil {
			continue
		}
		if id == "agy" {
			out = append(out, p.ownAgyHooks(doc, rel)...)
			continue
		}
		events := gjson.GetBytes(doc, "hooks").Map()
		names := make([]string, 0, len(events))
		for ev := range events {
			names = append(names, ev)
		}
		sort.Strings(names)
		for _, ev := range names {
			list, err := hookList(doc, abs, ev)
			if err != nil {
				continue
			}
			for _, raw := range list {
				if p.wroteHook(id, ev, raw) {
					continue
				}
				for _, c := range ownHookCommands(raw) {
					out = append(out, OwnResource{Name: c, Path: rel, Agent: id, Event: ev})
				}
			}
		}
	}
	return out
}

// ownAgyHooks are the commands an Antigravity hooks.json runs that magpie
// didn't write: every named hook's, and those in magpie's own ("magpie")
// it has no record of.
func (p *Project) ownAgyHooks(doc []byte, rel string) []OwnResource {
	var out []OwnResource
	top := gjson.ParseBytes(doc).Map()
	for _, name := range slices.Sorted(maps.Keys(top)) {
		v := top[name]
		if !v.IsObject() {
			continue
		}
		events := v.Map()
		for _, ev := range slices.Sorted(maps.Keys(events)) {
			if ev == "enabled" || !events[ev].IsArray() {
				continue
			}
			for _, e := range events[ev].Array() {
				raw := json.RawMessage(e.Raw)
				if name == "magpie" && p.wroteHook("agy", ev, raw) {
					continue
				}
				for _, c := range ownHookCommands(raw) {
					out = append(out, OwnResource{Name: c, Path: rel, Agent: "agy", Event: ev})
				}
			}
		}
	}
	return out
}

// wroteHook is whether raw is an entry magpie wrote, byte for byte.
func (p *Project) wroteHook(id, ev string, raw json.RawMessage) bool {
	return slices.ContainsFunc(p.HookRecs[id], func(r HookEntry) bool {
		return r.Event == ev && sameEntry(r.Entry, raw)
	})
}

func sameEntry(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return false
	}
	return bytes.Equal(x.Bytes(), y.Bytes())
}

// ownHookCommands are the commands an entry runs: its own, or those of the
// group it holds (Claude Code's { matcher, hooks: [...] }).
func ownHookCommands(raw json.RawMessage) []string {
	var e struct {
		Command string `json:"command"`
		Hooks   []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	var out []string
	if e.Command != "" {
		out = append(out, e.Command)
	}
	for _, h := range e.Hooks {
		if h.Command != "" {
			out = append(out, h.Command)
		}
	}
	return out
}

func (p *Project) ownSubagents(id string) []OwnResource {
	sp := projectSubagentSpec(p, id)
	if sp == nil || p.inside(sp.dir) != nil {
		return nil
	}
	var out []OwnResource
	entries, _ := os.ReadDir(sp.dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), sp.ext) {
			continue
		}
		abs := filepath.Join(sp.dir, e.Name())
		rel := p.rel(abs)
		body, err := os.ReadFile(abs)
		if err != nil || slices.ContainsFunc(p.AgentFiles, func(f SubagentFile) bool {
			return f.Path == rel && f.Hash == subagentHash(body)
		}) {
			continue
		}
		m := parseMeta(body)
		name := m.Name
		if name == "" {
			name = strings.TrimSuffix(e.Name(), sp.ext)
		}
		out = append(out, OwnResource{Name: name, Path: rel, Agent: id, Description: m.Description})
	}
	return out
}

// ownPi are the packages the project's .pi/settings.json declares that magpie
// didn't write: its own, which can be adopted (#7249).
func (p *Project) ownPi() []OwnResource {
	abs := filepath.Join(p.Dir, filepath.FromSlash(piSettingsRel))
	if p.inside(abs) != nil {
		return nil
	}
	list, err := projectPiList(abs)
	if err != nil {
		return nil
	}
	var out []OwnResource
	for _, r := range list {
		pk, ok := piPackageOf(r)
		if !ok || slices.ContainsFunc(p.PiRecs, func(x PiRec) bool { return piIDOf(x.Source) == piIDOf(pk.Source) }) {
			continue
		}
		row := OwnResource{Name: pk.Source, Path: piSettingsRel, Agent: "pi"}
		if !pk.On {
			row.Event = "off"
		}
		if pk.Filtered {
			row.Description = "filtered"
		}
		out = append(out, row)
	}
	return out
}
