package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/gateway"
)

// projectMCPFiles is the file, in a project, each agent reads the project's
// own MCP servers from — only those whose docs say so: Claude Code's
// .mcp.json (its project scope), Codex's .codex/config.toml (read in a
// trusted project only), Gemini CLI's .gemini/settings.json (its workspace
// settings), Cursor's .cursor/mcp.json, OpenCode's opencode.json at the
// project's root, ZCode's .zcode/config.json (its workspace scope:
// mcp.servers, as its user-wide cli/config.json has them; zcode.z.ai/en/docs/
// mcp-services, "Configuration Files and Default Load Paths") and Pi's
// .pi/mcp.json (mcpServers, read once the project is trusted; pi.dev/docs/
// latest/mcp, "Configure servers" — Pi's own MCP, 0.99 on, whatever
// extension the user's file is written for). Qoder's CLI (and Qoder CN's)
// reads .qoder/settings.json's mcpServers as its project level, beside
// .mcp.json (docs.qoder.com/cli/mcp-reference; @qoder-ai/qodercli and
// @qodercn-ai/qoderclicn 1.1.69 name the project folder .qoder alike).
// CodeBuddy Code reads the first there is of .mcp.json and mcp.json at the
// project's root (getMcpCandidatePaths for the project scope,
// @tencent-ai/codebuddy-code 2.164.0), and Droid .factory/mcp.json
// (docs.factory.com/cli/configuration/mcp). Qoder and CodeBuddy, like
// Claude Code, ask the user to approve a project's server before using
// it. Each is written in the shape the agent's user-wide file has.
// DeepSeek Harness has none: its servers are @deepseek-ai/dsh-mcp-client
// rows in a profile's patch layers (bundles, the profile's and
// $DSH_HOME's cordis.patch.yml, --patch overlays; composeProfile in
// @deepseek-ai/dsh 0.2.1-rc.5's profile-boot), none of them the project's.
var projectMCPFiles = map[string]struct {
	rel    string
	format mcpFormat
}{
	"claude":    {".mcp.json", fmtClaude},
	"codex":     {".codex/config.toml", fmtCodex},
	"gemini":    {".gemini/settings.json", fmtGemini},
	"cursor":    {".cursor/mcp.json", fmtCursor},
	"opencode":  {"opencode.json", fmtOpenCode},
	"zcode":     {".zcode/config.json", fmtZCode},
	"pi":        {".pi/mcp.json", fmtPiNative},
	"qoder":     {".qoder/settings.json", fmtOmp},
	"qoder-cn":  {".qoder/settings.json", fmtOmp},
	"codebuddy": {".mcp.json", fmtClaude},
	"droid":     {".factory/mcp.json", fmtClaude},
}

// ProjectMCPFile is the file, in a project, an agent reads the project's
// MCP servers from: "" for one magpie can't give a project's servers to.
func ProjectMCPFile(agent string) string { return projectMCPFiles[agent].rel }

// ProjectNoSSE says whether an agent's project file can't take a server
// over SSE (Codex's, Pi's), whatever its user-wide one can.
func ProjectNoSSE(agent string) bool {
	f, ok := projectMCPFiles[agent]
	return ok && errors.Is((&mcpFile{Format: f.format}).supports(&Server{Transport: "sse"}), errNoSSE)
}

// mcpRel is the agent's file in this project: OpenCode's opencode.jsonc
// when that is the one the project has, and CodeBuddy's mcp.json when the
// project has that and no .mcp.json, as CodeBuddy reads only the first.
func (p *Project) mcpRel(agent string) (string, mcpFormat) {
	f, ok := projectMCPFiles[agent]
	if !ok {
		return "", 0
	}
	if f.format == fmtOpenCode && !exists(filepath.Join(p.Dir, "opencode.json")) && exists(filepath.Join(p.Dir, "opencode.jsonc")) {
		return "opencode.jsonc", f.format
	}
	if agent == "codebuddy" && !exists(filepath.Join(p.Dir, ".mcp.json")) && exists(filepath.Join(p.Dir, "mcp.json")) {
		return "mcp.json", f.format
	}
	return f.rel, f.format
}

// formatOfRel is the format of a project's file magpie wrote servers in.
func formatOfRel(rel string) (mcpFormat, bool) {
	switch rel {
	case "opencode.jsonc":
		return fmtOpenCode, true
	case "mcp.json":
		return fmtClaude, true
	}
	for _, f := range projectMCPFiles {
		if f.rel == rel {
			return f.format, true
		}
	}
	return 0, false
}

// syncProjectMCP writes the library's servers the project has into each
// agent's file there, and takes out those magpie wrote that it no longer
// has; an entry by the same name that magpie didn't write is left as it
// is, and so is everything else in the file.
func (l *Library) syncProjectMCP(p *Project, fail func(what string, err error)) {
	want := map[string][]*Server{} // the file → its servers
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for n := range p.Servers {
			if !yield(n) {
				return
			}
		}
	}) {
		s := l.server(name)
		if s == nil {
			continue
		}
		for _, id := range p.Servers[name] {
			rel, format := p.mcpRel(id)
			if rel == "" {
				continue
			}
			if err := (&mcpFile{Format: format}).supports(s); err != nil {
				if errors.Is(err, errNoSSE) {
					err = fmt.Errorf("%s can't reach a server over SSE", rel)
				}
				fail("mcp:"+name, err)
				continue
			}
			if !slices.ContainsFunc(want[rel], func(x *Server) bool { return x.Name == s.Name }) {
				want[rel] = append(want[rel], through(s, gateway.URL()))
			}
		}
	}
	files := map[string]bool{}
	for rel := range want {
		files[rel] = true
	}
	for rel := range p.Wrote {
		files[rel] = true
	}
	wrote := map[string][]string{}
	startup := map[string]map[string]float64{}
	for _, rel := range slices.Sorted(func(yield func(string) bool) {
		for r := range files {
			if !yield(r) {
				return
			}
		}
	}) {
		format, ok := formatOfRel(rel)
		if !ok {
			continue
		}
		abs := filepath.Join(p.Dir, filepath.FromSlash(rel))
		f := &mcpFile{Path: abs, Format: format, NoCmdRefs: rel == projectMCPFiles["droid"].rel}
		had := p.Wrote[rel]
		was := p.Startup[rel]
		now := map[string]float64{}
		entries, err := f.entries()
		if err != nil {
			fail("", err)
			if len(had) > 0 {
				wrote[rel] = had
				if was != nil {
					startup[rel] = was
				}
			}
			continue
		}
		var mine []string
		for _, name := range had {
			if slices.ContainsFunc(want[rel], func(s *Server) bool { return s.Name == name }) {
				continue
			}
			if entries[name] == nil {
				continue // taken out by hand: forgotten
			}
			if err := f.del(name); err != nil {
				fail("mcp:"+name, err)
				mine = append(mine, name)
				now[name] = was[name]
			}
		}
		for _, s := range want[rel] {
			s := wroteStartup(f.withCmdVars(s), was[s.Name])
			old := entries[s.Name]
			if old != nil && !slices.Contains(had, s.Name) {
				fail("mcp:"+s.Name, fmt.Errorf("%s has a server called %s already that isn't magpie's: it's left as it is", rel, s.Name))
				continue
			}
			if old != nil {
				if cur, ok := f.current(s.Name, old, s); ok && cur.same(s) && !f.behind(s, old) {
					mine = append(mine, s.Name)
					now[s.Name] = f.startupWritten(s, old)
					continue
				}
			}
			if !exists(abs) {
				if err := p.mkdirs(filepath.Dir(abs)); err != nil {
					fail("mcp:"+s.Name, err)
					continue
				}
				if !slices.Contains(p.MadeFiles, rel) {
					p.MadeFiles = append(p.MadeFiles, rel)
				}
			}
			if err := f.put(s, old); err != nil {
				fail("mcp:"+s.Name, err)
				if old != nil {
					mine = append(mine, s.Name)
					now[s.Name] = was[s.Name]
				}
				continue
			}
			mine = append(mine, s.Name)
			now[s.Name] = f.startupWritten(s, old)
		}
		sort.Strings(mine)
		maps.DeleteFunc(now, func(_ string, v float64) bool { return v == 0 })
		if len(now) > 0 {
			startup[rel] = now
		}
		if len(mine) > 0 {
			wrote[rel] = mine
			continue
		}
		// a file magpie made goes once nothing is left in it; one with
		// anything of the user's in it is the user's from then on
		if i := slices.Index(p.MadeFiles, rel); i >= 0 {
			if blankFile(abs, format) {
				if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
					fail("", err)
					continue
				}
			}
			p.MadeFiles = slices.Delete(p.MadeFiles, i, i+1)
		}
	}
	if len(wrote) == 0 {
		wrote = nil
	}
	p.Wrote = wrote
	if len(startup) == 0 {
		startup = nil
	}
	p.Startup = startup
	sort.Strings(p.MadeFiles)
}

// blankFile says whether a config file holds nothing but empty objects.
func blankFile(path string, format mcpFormat) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	var doc map[string]any
	if format == fmtCodex {
		err = toml.Unmarshal(raw, &doc)
	} else {
		err = json.Unmarshal(jsonc.ToJSON(raw), &doc)
	}
	return err == nil && blankMap(doc)
}

func blankMap(m map[string]any) bool {
	for _, v := range m {
		sub, ok := v.(map[string]any)
		if !ok || !blankMap(sub) {
			return false
		}
	}
	return true
}

// ProjectServer sets which agents get a library server in a project; none
// takes it out of the project.
func ProjectServer(dir, name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		if l.server(name) == nil {
			return fmt.Errorf("no server called %s", name)
		}
		var ids []string
		for _, id := range agents {
			if ProjectMCPFile(id) == "" {
				return fmt.Errorf("magpie knows of no file %s reads a project's MCP servers from", id)
			}
			ids = set(ids, id, true)
		}
		if len(ids) == 0 {
			delete(p.Servers, name)
			return nil
		}
		if p.Servers == nil {
			p.Servers = map[string][]string{}
		}
		p.Servers[name] = ids
		return nil
	})
}

// renameProjectServers has the projects give a server its new name.
func (l *Library) renameProjectServers(old, name string) {
	for _, p := range l.Projects {
		if a, ok := p.Servers[old]; ok {
			delete(p.Servers, old)
			p.Servers[name] = a
		}
	}
}
