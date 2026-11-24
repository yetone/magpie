package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// Tool is a tool a plugin gives: OpenCode's tool hook ({tool: {name:
// tool({description, args, execute})}}), which magpie offers every agent
// through its plugin tools MCP server (internal/gateway/tools_mcp.go).
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Schema is its arguments as JSON Schema, made in the host from its
	// zod shape
	Schema json.RawMessage `json:"schema,omitempty"`
}

// PluginTools are one plugin's tools.
type PluginTools struct {
	Spec  string `json:"spec"`
	Tools []Tool `json:"tools"`
}

// Tools are the tools the plugins the host loaded give, in plugins.json's
// order, starting the host if need be.
func Tools(ctx context.Context) ([]PluginTools, error) {
	loaded, err := Plugins(ctx)
	if err != nil {
		return nil, err
	}
	var out []PluginTools
	for _, l := range loaded {
		if l.Error == "" && len(l.Tools) > 0 {
			out = append(out, PluginTools{Spec: l.Spec, Tools: l.Tools})
		}
	}
	return out, nil
}

// ToolTimeout is how long a tool may run before its call is answered that
// it took too long. Agents give a tool call as little as 60 s (the MCP
// SDK's request timeout), so it ends a little before that.
var ToolTimeout = 55 * time.Second

// RunTool runs the plugin spec's tool name with args (a JSON object) in
// the host: its answer as text. agent is the agent that called it, as the
// tool's context names it.
func RunTool(ctx context.Context, spec, name string, args json.RawMessage, agent string) (string, error) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	var out struct {
		Text string `json:"text"`
	}
	err := callWithTimeout(ctx, "tool", map[string]any{
		"spec": spec, "name": name, "args": args, "agent": agent,
		"ms": ToolTimeout.Milliseconds(),
	}, &out, ToolTimeout+5*time.Second)
	if errors.Is(err, context.DeadlineExceeded) {
		return "", fmt.Errorf("%s took longer than %s", name, ToolTimeout)
	}
	return out.Text, err
}

// toolsPath is where the tools the host last loaded are noted, by plugin:
// the Plugin tools plugin's InUse reads it, which mustn't start the host.
func toolsPath() string { return filepath.Join(settings.Dir(), "plugin-tools.json") }

// ToolsPath is the note ToolsOn reads, which the gateway watches.
func ToolsPath() string { return toolsPath() }

var toolsMu sync.Mutex

// noteTools notes which plugins the host loaded give tools, written whole.
func noteTools(loaded []Loaded) {
	m := map[string][]string{}
	for _, l := range loaded {
		if l.Error != "" || len(l.Tools) == 0 {
			continue
		}
		names := make([]string, 0, len(l.Tools))
		for _, t := range l.Tools {
			names = append(names, t.Name)
		}
		m[l.Spec] = names
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	toolsMu.Lock()
	defer toolsMu.Unlock()
	old, err := steady.ReadFile(toolsPath())
	if err == nil && strings.TrimSpace(string(old)) == strings.TrimSpace(string(b)) {
		return
	}
	if len(m) == 0 && errors.Is(err, fs.ErrNotExist) {
		return
	}
	defer filememo.Forget() // the gateway follows this note (Written)
	if err := writeWhole(toolsPath(), append(b, '\n')); err != nil {
		log.Printf("plugin tools: noting them: %v", err)
		return
	}
	if f := ToolsNoted; f != nil {
		toolsNoting.Add(1)
		go func() {
			defer toolsNoting.Done()
			f()
		}()
	}
}

// toolsNoting counts the ToolsNoted calls under way: the library's syncs
// write agents' settings, so Settle waits for them before a test's folder
// goes (#7611).
var toolsNoting sync.WaitGroup

// ToolsNoted is called once the note of which plugins give tools has
// changed: the library gives a plugin that gives tools a server of its
// own then (library/plugin_tools.go), which can't be imported here.
var ToolsNoted func()

// ReadList is plugins.json, or an error when it couldn't be read or
// parsed: the plugins installed are then unknown, never none. No file is
// no plugin.
func ReadList() (List, error) {
	b, err := lastgood.Read(listPath(), lastgood.JSON)
	if errors.Is(err, fs.ErrNotExist) {
		return List{}, nil
	}
	if err != nil {
		return List{}, err
	}
	var l List
	if err := json.Unmarshal(b, &l); err != nil {
		return List{}, fmt.Errorf("%s: %w", listPath(), err)
	}
	return l, nil
}

// ToolsOn counts the plugins on in plugins.json that gave tools when the
// host last loaded them. A note that can't be read or parsed is an error:
// not known.
func ToolsOn() (int, error) {
	b, err := steady.ReadFile(toolsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var m map[string][]string
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, fmt.Errorf("%s: %w", toolsPath(), err)
	}
	n := 0
	for _, e := range Load().Plugins {
		if !e.Off && len(m[e.Spec]) > 0 {
			n++
		}
	}
	return n, nil
}

// ToolNames are the tools noted for each plugin, by spec, sorted: what the
// Plugins page shows for a plugin whose host isn't running.
func ToolNames() map[string][]string {
	b, err := steady.ReadFile(toolsPath())
	if err != nil {
		return nil
	}
	var m map[string][]string
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	for _, v := range m {
		sort.Strings(v)
	}
	return m
}
