package fasttools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PluginName is the plugin's folder under Antigravity's plugins.
const PluginName = "fast-tools"

// rulesVersion is the version of what Write writes: a plugin written by an
// older magpie is written again (Keep). A var for the tests.
var rulesVersion = 1

// Dir is the plugin's folder: Antigravity's global customization folder,
// ~/.gemini/config, shared by its CLI and IDE, finds a plugin in
// plugins/<name> by its plugin.json.
func Dir(home string) string {
	return filepath.Join(home, ".gemini", "config", "plugins", PluginName)
}

// markFile, in the plugin's folder, is how magpie knows the plugin is its
// own: a folder of this name without it, someone's own plugin, isn't
// touched. It isn't in plugin.json, where Antigravity's strict check
// refuses a field it doesn't know.
const markFile = ".magpie.json"

type mark struct {
	FastTools int `json:"fastTools"`
}

// written is the rulesVersion of the plugin magpie wrote there; 0 for none,
// or a mark that can't be read.
func written(home string) int {
	b, err := os.ReadFile(filepath.Join(Dir(home), markFile))
	if err != nil {
		return 0
	}
	var m mark
	if json.Unmarshal(b, &m) != nil || m.FastTools < 0 {
		return 0
	}
	return m.FastTools
}

func hasManifest(home string) bool {
	_, err := os.Lstat(filepath.Join(Dir(home), "plugin.json"))
	return !errors.Is(err, fs.ErrNotExist)
}

// On says the plugin magpie writes is there.
func On(home string) bool { return written(home) > 0 && hasManifest(home) }

// ErrNotOurs: a plugin of this name that magpie didn't write is there.
var ErrNotOurs = errors.New("a plugin named " + PluginName + " that magpie didn't write is there already")

// Write writes the plugin, its MCP server and its SessionStart hook
// starting exe, magpie's own program.
func Write(home, exe string) error {
	if exe == "" {
		return errors.New("no magpie program to start")
	}
	// a plugin.json without magpie's mark is someone's own
	if hasManifest(home) && written(home) == 0 {
		return ErrNotOurs
	}
	dir := Dir(home)
	tools := map[string]any{}
	for _, t := range toolNames {
		// eager: the tools are in every turn's list, not looked up first
		tools[t] = map[string]any{"eager": true}
	}
	// the mark first and plugin.json last: a folder left half-written is
	// still known as magpie's, and isn't a plugin until it is whole
	files := []struct {
		name string
		v    any
	}{
		{markFile, mark{FastTools: rulesVersion}},
		{"mcp_config.json", map[string]any{"mcpServers": map[string]any{
			ServerName: map[string]any{"command": exe, "args": []string{"fast-tools", "mcp"}, "tools": tools},
		}}},
		{"hooks.json", map[string]any{PluginName: map[string]any{
			"SessionStart": []any{map[string]any{"type": "command", "command": hookCommand(exe), "timeout": 5}},
		}}},
		{"rules/AGENTS.md", rules},
		{"assets/logo.svg", logo},
		{"plugin.json", map[string]any{
			"name":        PluginName,
			"displayName": "Fast tools",
			"version":     fmt.Sprintf("1.0.%d", rulesVersion),
			"description": "Batched search, reads, edits and checks, and a workspace snapshot at the start of a conversation.",
			"logo":        "assets/logo.svg",
		}},
	}
	for _, f := range files {
		text, ok := f.v.(string)
		if !ok {
			b, err := json.MarshalIndent(f.v, "", "  ")
			if err != nil {
				return err
			}
			text = string(b) + "\n"
		}
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(f.name)), text, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// logo is the plugin's icon in Antigravity: a bolt, grey to sit in light
// and dark.
const logo = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><path d="M9.25 2.25 4 9h3.75l-1 4.75L12 7H8.25z" fill="none" stroke="#888" stroke-width="1.3" stroke-linejoin="round"/></svg>
`

// hookCommand is the hook's command line: exe quoted for the shell
// Antigravity runs a hook in, sh -c on unix and cmd /c on Windows.
func hookCommand(exe string) string { return hookCommandOn(exe, runtime.GOOS) }

func hookCommandOn(exe, goos string) string {
	if goos == "windows" {
		// cmd takes a "…" path as one word; a Windows path has no "
		return `"` + exe + `" fast-tools session-start`
	}
	if strings.ContainsAny(exe, " \t'\"\\$`!&;|<>()*?[]#~{}") {
		exe = "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
	}
	return exe + " fast-tools session-start"
}

// Remove takes the plugin out, if it is magpie's.
func Remove(home string) error {
	if _, err := os.Stat(Dir(home)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if written(home) == 0 {
		return ErrNotOurs
	}
	return os.RemoveAll(Dir(home))
}

// Keep writes the plugin again when it is on but starts a magpie that is
// no longer there (magpie was moved, or reinstalled at another path), or
// was written by an older magpie. One that starts another magpie still
// there is left as it is: two magpies (a build of one's own beside the
// app) would otherwise each take it back on every start. It never turns
// the plugin on.
func Keep(home, exe string) error {
	v := written(home)
	if v == 0 || exe == "" {
		return nil
	}
	if v >= rulesVersion && hasManifest(home) && startsThere(home) {
		return nil
	}
	return Write(home, exe)
}

// startsThere says the program the plugin's MCP server starts is there.
func startsThere(home string) bool {
	b, err := os.ReadFile(filepath.Join(Dir(home), "mcp_config.json"))
	if err != nil {
		return false
	}
	var c struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &c) != nil {
		return false
	}
	cmd := c.MCPServers[ServerName].Command
	if cmd == "" {
		return false
	}
	fi, err := os.Stat(cmd)
	return err == nil && fi.Mode().IsRegular()
}

// rules are the plugin's rules: when to reach for which tool. They go to
// the model with every turn, so they are short and say nothing but how to
// work.
const rules = `# Batched tools

The fast MCP server's tools each do in one call what otherwise takes many.
Use them when the work splits into independent parts; pass cwd, the
workspace's absolute path, every time.

- Several searches (text, regex, file globs, read-only git): one
  batch_search with all of them, not one search a turn.
- Several files, or parts of them: one read_slices. A long file comes as an
  outline first; then ask for the line ranges needed.
- A change across files: one apply_edits with every edit. Nothing is written
  unless every edit applies; give verify (a build or test command) to check
  the result in the same call.
- Independent builds, tests and linters: one run_checks; they run side by
  side and only failures show their output.

Keep using the built-in tools for one quick search, read or edit, and for
anything these don't do.
`
