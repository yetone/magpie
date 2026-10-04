package agent

// JetBrains Air, JetBrains' agentic development environment, runs its own
// agents (Claude Agent, Codex, Gemini CLI, Junie) on the accounts signed in
// under Settings | Account | AI Providers: a sign-in or an API key, with no
// base URL of its own to point at magpie (AIR-4541 asks for one). What it
// does take is any agent that speaks the Agent Client Protocol, listed in
// acp.json in its config folder — ~/Library/Application Support/JetBrains/Air
// on macOS, $XDG_CONFIG_HOME/JetBrains/Air on Linux, %APPDATA%\JetBrains\Air
// on Windows — as
//
//	{"agent_servers": {"<name in Air's agent menu>": {"command": …, "args": […], "env": {…}}}}
//
// and the models such an agent reports at session/new are its model menu
// in Air. So magpie adds an agent of its own there, "Magpie": OpenCode's ACP
// server (opencode acp), handed with OPENCODE_CONFIG a config file magpie
// keeps beside acp.json that has magpie's provider, its models, the one
// picked as the model a task starts on, and enabled_providers narrowed to
// magpie, so Air's menu lists magpie's models and routing groups and nothing
// of OpenCode's own. OpenCode merges that file over the user's own config
// and leaves the rest of it (their plugins, agents, MCP servers) as it is.
// Air's own agents, and the accounts they run on, aren't touched.
//
// Checked with OpenCode 2.0.20's opencode acp under a sandbox HOME, as Air
// starts it: session/new's model option lists magpie's models alone, the
// one set here current. Not with Air itself. One gap is OpenCode's: when a
// provider's key is in the environment it is started with (OPENAI_API_KEY,
// DEEPSEEK_API_KEY, … set, not empty), 2.0.20's ACP server lists that
// provider and none of its config's — this file's, the global one or a
// project's alike — so magpie's models don't show; an emptied key, or one
// kept in OpenCode's auth.json, doesn't do it.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// airAgent is the name magpie's agent has in acp.json, and so in Air's
// agent menu.
const airAgent = "Magpie"

// airDir is where Air keeps its settings and acp.json.
func airDir(home, cfg string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "JetBrains", "Air")
	case "windows":
		d := appdir.Getenv("APPDATA")
		if d == "" {
			d = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(d, "JetBrains", "Air")
	}
	return filepath.Join(cfg, "JetBrains", "Air")
}

// airOpenCode is the OpenCode Air is to start: the one on PATH, else the
// one OpenCode's installer puts in ~/.opencode/bin, as a full path, since
// Air, an app, may not have the shell's PATH; "" when there is none.
var airOpenCode = func(home string) string {
	if p, err := exec.LookPath("opencode"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	name := "opencode"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p := filepath.Join(home, ".opencode", "bin", name); isFile(p) {
		return p
	}
	return ""
}

func air(home, cfg string) *Agent {
	dir := airDir(home, cfg)
	path := filepath.Join(dir, "acp.json")
	// the OpenCode config magpie's agent runs on, magpie's own file
	ocPath := filepath.Join(dir, "magpie-opencode.json")
	entry := "agent_servers." + airAgent
	provider := func() any { return magpieProviderJSONFor("opencode", "air") }
	// ours: acp.json's Magpie is the agent magpie added, on its file
	ours := func() bool {
		v, _ := edit.GetJSON(path, entry+".env.OPENCODE_CONFIG")
		return v == ocPath
	}
	// model is the one a task starts on, read from magpie's own file: with
	// Magpie gone from acp.json it still reads, for Check to say so and a
	// set again to put the entry back
	model := func() string { v, _ := edit.GetJSON(ocPath, "model"); return v }
	return atomic(&Agent{
		ID: "air", Name: "JetBrains Air", Icon: "air", Aliases: []string{"jetbrains-air"},
		// no Bin: air is other tools' name too (Go's live reloader); its
		// requests are its OpenCode's, and counted as OpenCode's
		Dir: dir, Path: path,
		Notice: func() string {
			if !ours() {
				return ""
			}
			if airOpenCode(home) == "" {
				return "Air runs magpie's models through OpenCode's ACP server, and OpenCode isn't installed here: install OpenCode (npm i -g opencode-ai), then pick Magpie in a new Air task."
			}
			return "In Air, start a new task and pick Magpie in its agent menu: magpie's models and routing groups are in its model menu."
		},
		Check: func() string {
			if !usesMagpie(model()) {
				return ""
			}
			if !ours() {
				return "Air's Magpie agent (" + filepath.Base(path) + ") is gone, so Air no longer reaches magpie"
			}
			return wiringOff("Air", ocPath, func(k string) (string, bool) { return edit.GetJSON(ocPath, "provider."+magpieID+".options."+k) },
				"baseURL", gatewayV1(), "apiKey", gateway.Token)
		},
		Sync: func() error {
			return syncJSONInOrder(ocPath, "provider."+magpieID, provider)
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: model,
			Set: func(v string) error {
				if v == "" {
					if err := edit.DelJSON(path, entry); err != nil {
						return err
					}
					if err := edit.Remove(ocPath); err != nil && !os.IsNotExist(err) {
						return err
					}
					return nil
				}
				ref, ok := strings.CutPrefix(v, magpieID+"/")
				if !ok || !isMagpie(ref) {
					return fmt.Errorf("Air takes only magpie's models here (its own agents are picked in Air): %s isn't one", v)
				}
				// each key new to the file goes in at its top: last first,
				// so a new file reads $schema, model, …, provider
				if err := edit.SetJSON(ocPath,
					edit.KV{Path: "provider." + magpieID, Value: provider()},
					edit.KV{Path: "enabled_providers", Value: []string{magpieID}},
					// titles and summaries too, as the user's own small
					// model may be a provider left out here
					edit.KV{Path: "small_model", Value: v},
					edit.KV{Path: "model", Value: v},
					edit.KV{Path: "$schema", Value: "https://opencode.ai/config.json"}); err != nil {
					return err
				}
				bin := airOpenCode(home)
				if bin == "" {
					bin = "opencode"
				}
				return edit.SetJSON(path, edit.KV{Path: entry, Value: map[string]any{
					"command": bin, "args": []string{"acp"},
					"env": map[string]string{"OPENCODE_CONFIG": ocPath},
				}})
			},
			Options: func(map[string]string) []Option { return viaMagpie("air", magpieID+"/") },
		}},
	}, path, ocPath)
}
