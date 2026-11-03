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
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
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
	if p, err := proc.LookPath("opencode"); err == nil {
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
	return acpOpenCode(home, name, runtime.GOOS)
}

// acpOpenCode is an OpenCode a JetBrains IDE or Zed installed from the ACP
// registry, unpacked into the editor's own folder and on no PATH (#2423:
// IDEA's at ~/Library/Caches/JetBrains/IntelliJIdea2026.2/acp-agents/
// opencode/1.19.0/opencode); the newest when there are several, "" when
// none.
func acpOpenCode(home, name, goos string) string {
	var globs []string
	switch goos {
	case "darwin":
		globs = []string{
			filepath.Join(home, "Library", "Caches", "JetBrains", "*", "acp-agents", "opencode", "*", name),
			filepath.Join(home, "Library", "Application Support", "Zed", "external_agents", "registry", "opencode", "*", name),
		}
	case "windows":
		local := appdir.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		globs = []string{
			filepath.Join(local, "JetBrains", "*", "acp-agents", "opencode", "*", name),
			filepath.Join(local, "Zed", "external_agents", "registry", "opencode", "*", name),
		}
	default:
		cache := appdir.Getenv("XDG_CACHE_HOME")
		if cache == "" {
			cache = filepath.Join(home, ".cache")
		}
		data := appdir.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		globs = []string{
			filepath.Join(cache, "JetBrains", "*", "acp-agents", "opencode", "*", name),
			filepath.Join(data, "zed", "external_agents", "registry", "opencode", "*", name),
		}
	}
	best, at := "", time.Time{}
	for _, g := range globs {
		ps, _ := filepath.Glob(g)
		for _, p := range ps {
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if best == "" || fi.ModTime().After(at) {
				best, at = p, fi.ModTime()
			}
		}
	}
	return best
}

func air(home, cfg string) *Agent {
	dir := airDir(home, cfg)
	return airAt(home, &Agent{
		ID: "air", Name: "JetBrains Air", Icon: "air", Aliases: []string{"jetbrains-air"},
		// no Bin: air is other tools' name too (Go's live reloader); its
		// requests are its OpenCode's, and counted as OpenCode's
		Dir: dir,
	}, filepath.Join(dir, "acp.json"), noticeAir2)
}

// airIDE is Air in JetBrains IDEs: the Air plugin, bundled with 2026.3
// IDEs and installed from JetBrains Marketplace in 2026.2 (com.intellij.air;
// Settings › AI › Air). It isn't the standalone app's code: its
// AcpConfigurationLoader (intellij.air.backend.acp.runtime in
// air-plugin-262.8665.521) reads the custom ACP agents from
// user.home/.jetbrains/acp.json on every system, the file AI Assistant's
// "Add Custom Agent" writes too, where the app reads acp.json in its own
// config folder. An entry takes command, args, env, use_idea_mcp and
// use_custom_mcp. Its own agents (Claude Agent, Codex, Junie, …) run on
// JetBrains AI, the agent's own login, or an Anthropic, OpenAI, Gemini or
// OpenRouter key under AI › Air › Accounts, none with a base URL. On its
// own login its Codex runs this computer's codex (the terminal's PATH, else
// the @openai/codex it installs) as `codex app-server`, with the user's
// CODEX_HOME or ~/.codex and no model_provider or CODEX_HOME of its own
// (CodexAppServerProcessKt, CodexCliUtils), its model menu Codex's
// model/list and its default config/read's model; its Claude Agent runs
// claude with only Air's hooks in --settings. So both are on magpie while
// Codex and Claude Code are connected here, and the model picked on Air's
// row is the Magpie agent's alone (jumy_18634 on Discord), which the row
// says (airSaid in app.js). magpie adds the same Magpie agent as the
// app's, with its OpenCode config beside acp.json.
func airIDE(home, cfg string) *Agent {
	dir := filepath.Join(home, ".jetbrains")
	path := filepath.Join(dir, "acp.json")
	return airAt(home, &Agent{
		ID: "air-ide", Name: "Air (JetBrains IDEs)", Icon: "air",
		Aliases: []string{"air-plugin", "air-in-ides", "jetbrains-acp"},
		Dir:     dir,
		// ~/.jetbrains is there for AI Assistant's agents too: an IDE of
		// 2026.2 or later, or magpie's agent already in acp.json, says Air
		// can be
		detect: func() bool {
			if jetBrainsIDESince(home, cfg, 2026, 2) {
				return true
			}
			v, _ := edit.GetJSON(path, "agent_servers."+airAgent+".env.OPENCODE_CONFIG")
			return v == filepath.Join(dir, "magpie-opencode.json")
		},
	}, path, noticeAirIDE)
}

// jetBrainsIDESince reports whether a JetBrains IDE of year.minor or later
// has kept its settings here: a config folder named for the IDE and its
// version (IntelliJIdea2026.3, PyCharm2026.2, …) under JetBrains' folder
// (Library/Application Support on macOS, %APPDATA% on Windows,
// $XDG_CONFIG_HOME on Linux).
func jetBrainsIDESince(home, cfg string, year, minor int) bool {
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support", "JetBrains")
	case "windows":
		d := appdir.Getenv("APPDATA")
		if d == "" {
			d = filepath.Join(home, "AppData", "Roaming")
		}
		base = filepath.Join(d, "JetBrains")
	default:
		base = filepath.Join(cfg, "JetBrains")
	}
	es, _ := os.ReadDir(base)
	for _, e := range es {
		if !e.IsDir() {
			continue
		}
		m := jetBrainsVersioned.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		y, _ := strconv.Atoi(m[1])
		n, _ := strconv.Atoi(m[2])
		if y > year || y == year && n >= minor {
			return true
		}
	}
	return false
}

// jetBrainsVersioned is an IDE's config folder name: the product, then its
// year and release.
var jetBrainsVersioned = regexp.MustCompile(`^[A-Za-z]+(\d{4})\.(\d+)$`)

// airAt is an Air agent on the acp.json at path: a, with its id, name,
// icon and detection, gets the Magpie entry there and its OpenCode config
// beside it; done is the notice once it is there.
func airAt(home string, a *Agent, path string, done notice) *Agent {
	dir := filepath.Dir(path)
	// the OpenCode config magpie's agent runs on, magpie's own file
	ocPath := filepath.Join(dir, "magpie-opencode.json")
	entry := "agent_servers." + airAgent
	id := a.ID
	provider := openCodeKept(ocPath, func() any { return magpieProviderJSONFor("opencode", id) })
	// ours: acp.json's Magpie is the agent magpie added, on its file
	ours := func() bool {
		v, _ := edit.GetJSON(path, entry+".env.OPENCODE_CONFIG")
		return v == ocPath
	}
	// model is the one a task starts on, read from magpie's own file: with
	// Magpie gone from acp.json it still reads, for Check to say so and a
	// set again to put the entry back
	model := func() string { v, _ := edit.GetJSON(ocPath, "model"); return v }
	a.Path = path
	a.Notice = func() string {
		if !ours() {
			return ""
		}
		if airOpenCode(home) == "" {
			return noticeAir1.String()
		}
		return done.String()
	}
	a.Check = func() string {
		if !usesMagpie(model()) {
			return ""
		}
		if !ours() {
			return "Air's Magpie agent (" + filepath.Base(path) + ") is gone, so Air no longer reaches magpie"
		}
		return wiringOff("Air", ocPath, func(k string) (string, bool) { return edit.GetJSON(ocPath, "provider."+magpieID+".options."+k) },
			"baseURL", gatewayV1(), "apiKey", hereKey())
	}
	a.ListOnly = true
	a.Sync = func() error {
		return syncJSONInOrder(ocPath, "provider."+magpieID, provider)
	}
	a.Fields = []Field{{
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
		Options: func(map[string]string) []Option { return viaMagpie(id, magpieID+"/") },
	}}
	return atomic(a, path, ocPath)
}

// what air says after a change (notice.go)
var (
	noticeAir1   = newNotice("Air runs magpie's models through OpenCode's ACP server, and OpenCode isn't installed here: install OpenCode (npm i -g opencode-ai), then pick Magpie in a new Air task.")
	noticeAir2   = newNotice("In Air, start a new task and pick Magpie in its agent menu: magpie's models and routing groups are in its model menu.")
	noticeAirIDE = newNotice("In your JetBrains IDE (2026.3, or 2026.2 with the Air plugin), pick Magpie in Air's agent menu — restart the IDE if it isn't listed yet: magpie's models and routing groups are in its model menu.")
)
