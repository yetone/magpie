package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
	"gopkg.in/yaml.v3"
)

// RTK (rtk-ai.app) is a CLI that the shell commands an agent runs go
// through — git status, cargo test, ls — cut down to what the model needs,
// so their output costs fewer tokens. Each agent gets it through a hook its
// own installer writes (rtk init -g …): magpie runs that installer for the
// agents switched on, takes out what it wrote for those switched off, and
// reads each one's files to say which have it, since rtk init --show knows
// only of Claude Code, OpenCode and Cursor.

// RTKURL is where rtk is installed from.
const RTKURL = "https://www.rtk-ai.app"

// rtkSpec is how rtk's installer names one agent, and what it writes there.
type rtkSpec struct {
	flags []string // after rtk init -g
	// patch: the installer takes --auto-patch (Codex's refuses it)
	patch bool
	// has says whether the agent has rtk's hook, by its files
	has func(a *agent.Agent) bool
	// files are those the installer writes and remove takes back, kept
	// aside by magpie first
	files func(a *agent.Agent) []string
	// remove takes out what the installer put in, and only that. It is
	// magpie's own, not rtk init --uninstall: it works with rtk gone (its
	// hooks left behind then fail every command), and rtk's uninstaller
	// deletes Gemini's GEMINI.md whole and takes Claude Code, OpenCode and
	// Cursor away together.
	remove func(a *agent.Agent) error
	// dir is the agent's folder, made first: rtk writes nothing for an
	// agent whose folder isn't there yet
	dir func(a *agent.Agent) string
	// withClaude: the installer gives it to Claude Code too, which is
	// pointed at a folder thrown away after
	withClaude bool
	// env is what the installer is run with besides magpie's own
	// environment, so it writes where the agent reads
	env func(a *agent.Agent) []string
	// blocked says why rtk's installer mustn't be run for the agent as it
	// is where it lives (this machine, or its WSL distro), "" when it can be
	blocked func(a *agent.Agent) string
}

func contains(path, s string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Contains(b, []byte(s))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// rtkRef is the line rtk adds to an instructions file to pull its own in:
// @RTK.md, or @ and RTK.md's whole path.
func rtkRef(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "@") && (t == "@RTK.md" || strings.HasSuffix(t, "/RTK.md") || strings.HasSuffix(t, `\RTK.md`))
}

var rtkSpecs = map[string]rtkSpec{
	"claude": {
		patch: true,
		has: func(a *agent.Agent) bool {
			return contains(filepath.Join(rtkClaudeDir(a), "settings.json"), "rtk hook claude")
		},
		files: func(a *agent.Agent) []string {
			d := rtkClaudeDir(a)
			return []string{filepath.Join(d, "settings.json"), filepath.Join(d, "CLAUDE.md"), filepath.Join(d, "RTK.md")}
		},
		remove: func(a *agent.Agent) error {
			d := rtkClaudeDir(a)
			// rtk writes its hook under PostToolUse too when its pipeline
			// has after_tool rules (rtk 1.0.1, src/hooks/init/claude.rs),
			// and its own uninstaller takes PreToolUse, PostToolUse and
			// SessionStart out alike; has reads the file whole, so an entry
			// left under another event kept the row on (#2879)
			return errors.Join(
				dropHook(filepath.Join(d, "settings.json"), "hooks.PreToolUse", "rtk hook claude"),
				dropHook(filepath.Join(d, "settings.json"), "hooks.PostToolUse", "rtk hook claude"),
				dropHook(filepath.Join(d, "settings.json"), "hooks.SessionStart", "rtk hook claude"),
				dropLines(filepath.Join(d, "CLAUDE.md"), rtkRef),
				rm(filepath.Join(d, "RTK.md")))
		},
		dir: func(a *agent.Agent) string { return rtkClaudeDir(a) },
	},
	"codex": {
		flags:   []string{"--codex"},
		blocked: codexBlocked,
		has: func(a *agent.Agent) bool {
			return contains(filepath.Join(rtkCodexDir(a), "hooks.json"), "rtk hook codex")
		},
		files: func(a *agent.Agent) []string {
			d := rtkCodexDir(a)
			return []string{filepath.Join(d, "hooks.json"), filepath.Join(d, "AGENTS.md"), filepath.Join(d, "RTK.md")}
		},
		remove: func(a *agent.Agent) error {
			d := rtkCodexDir(a)
			return errors.Join(
				dropHook(filepath.Join(d, "hooks.json"), "hooks.PreToolUse", "rtk hook codex"),
				dropLines(filepath.Join(d, "AGENTS.md"), rtkRef),
				rm(filepath.Join(d, "RTK.md")))
		},
		dir: func(a *agent.Agent) string { return rtkCodexDir(a) },
	},
	"gemini": {
		// --hook-only: without it the installer writes its own GEMINI.md
		// over the user's
		flags: []string{"--gemini", "--hook-only"}, patch: true,
		has: func(a *agent.Agent) bool {
			return contains(rtkAt(a, ".gemini", "settings.json"), "rtk-hook-gemini")
		},
		files: func(a *agent.Agent) []string {
			d := rtkAt(a, ".gemini")
			return []string{filepath.Join(d, "settings.json"), filepath.Join(d, "hooks", "rtk-hook-gemini.sh"), filepath.Join(d, "hooks", ".rtk-hook.sha256")}
		},
		remove: func(a *agent.Agent) error {
			d := rtkAt(a, ".gemini")
			err := errors.Join(
				dropHook(filepath.Join(d, "settings.json"), "hooks.BeforeTool", "rtk-hook-gemini"),
				rm(filepath.Join(d, "hooks", "rtk-hook-gemini.sh"), filepath.Join(d, "hooks", ".rtk-hook.sha256")))
			os.Remove(filepath.Join(d, "hooks")) // when nothing else is in it
			return err
		},
		dir: func(a *agent.Agent) string { return rtkAt(a, ".gemini") },
	},
	"opencode": {
		flags: []string{"--opencode"}, patch: true, withClaude: true, blocked: openCodeBlocked,
		has:    openCodeHas,
		files:  func(a *agent.Agent) []string { return []string{opencodePlugin(a)} },
		remove: func(a *agent.Agent) error { return rm(opencodePlugin(a)) },
		dir:    func(a *agent.Agent) string { return filepath.Dir(a.Path) },
	},
	"cursor": {
		flags: []string{"--agent", "cursor"}, patch: true, withClaude: true,
		has: func(a *agent.Agent) bool {
			return contains(rtkAt(a, ".cursor", "hooks.json"), "rtk hook cursor")
		},
		files: func(a *agent.Agent) []string { return []string{rtkAt(a, ".cursor", "hooks.json")} },
		remove: func(a *agent.Agent) error {
			return dropHook(rtkAt(a, ".cursor", "hooks.json"), "hooks.preToolUse", "rtk hook cursor")
		},
		dir: func(a *agent.Agent) string { return rtkAt(a, ".cursor") },
	},
	"copilot": {
		flags: []string{"--copilot"}, patch: true,
		has: func(a *agent.Agent) bool {
			return exists(rtkAt(a, ".copilot", "hooks", "rtk-rewrite.json"))
		},
		files: func(a *agent.Agent) []string {
			d := rtkAt(a, ".copilot")
			return []string{filepath.Join(d, "hooks", "rtk-rewrite.json"), filepath.Join(d, "copilot-instructions.md")}
		},
		remove: func(a *agent.Agent) error {
			d := rtkAt(a, ".copilot")
			return errors.Join(
				rm(filepath.Join(d, "hooks", "rtk-rewrite.json")),
				dropBlock(filepath.Join(d, "copilot-instructions.md"), "<!-- rtk-instructions", "<!-- /rtk-instructions -->"))
		},
		dir: func(a *agent.Agent) string { return rtkAt(a, ".copilot") },
	},
	// Antigravity CLI (agy): rtk 0.51 on writes a plugin of its own into
	// Antigravity's global customization folder, ~/.gemini/config, shared
	// by the CLI and the IDE (src/hooks/init/antigravity.rs): plugin.json,
	// hooks.json (PreToolUse → rtk hook antigravity) and rules/AGENTS.md,
	// all in plugins/rtk. agy discovers a plugin there by its plugin.json.
	// The Antigravity branch reads no patch mode, so --auto-patch isn't
	// given; rtk's own uninstaller for it ran Claude Code's
	// (rtk-ai/rtk#3404), so the folder is taken out by magpie.
	"agy": {
		flags: []string{"--agent", "antigravity"}, blocked: agyBlocked,
		has: func(a *agent.Agent) bool {
			return contains(filepath.Join(agyPlugin(a), "hooks.json"), "rtk hook antigravity")
		},
		files: func(a *agent.Agent) []string {
			d := agyPlugin(a)
			return []string{filepath.Join(d, "plugin.json"), filepath.Join(d, "hooks.json"), filepath.Join(d, "rules", "AGENTS.md")}
		},
		remove: func(a *agent.Agent) error { return os.RemoveAll(agyPlugin(a)) },
		dir:    func(a *agent.Agent) string { return filepath.Dir(filepath.Dir(agyPlugin(a))) },
	},
	"pi":  extensionSpec("pi"),
	"omp": extensionSpec("omp"),
	// Factory's Droid: rtk 0.44 on (src/hooks/init/droid.rs in 1.1.0)
	// adds a PreToolUse group matching Execute, running rtk hook droid, to
	// the one file of Droid's whose PreToolUse hooks Droid runs: the root
	// hooks.json (or the legacy hooks/hooks.json when there's none), else
	// settings.json's hooks, and takes a stale one out of the others. It
	// finds Droid's folder as Droid does, $FACTORY_HOME_OVERRIDE/.factory
	// or ~/.factory, and is told the one magpie found Droid in. It reads
	// no patch mode, so --auto-patch isn't given.
	"droid": {
		flags: []string{"--agent", "droid"}, blocked: droidBlocked,
		has: func(a *agent.Agent) bool {
			for _, f := range droidHookFiles(a) {
				if contains(f.path, droidHook) {
					return true
				}
			}
			return false
		},
		files: func(a *agent.Agent) []string {
			var out []string
			for _, f := range droidHookFiles(a) {
				out = append(out, f.path)
			}
			return out
		},
		remove: func(a *agent.Agent) error {
			var errs []error
			for _, f := range droidHookFiles(a) {
				errs = append(errs, dropHook(f.path, f.key, droidHook))
			}
			return errors.Join(errs...)
		},
		dir: func(a *agent.Agent) string { return a.Dir },
		env: func(a *agent.Agent) []string { return []string{"FACTORY_HOME_OVERRIDE=" + filepath.Dir(a.Dir)} },
	},
	// Hermes's folder is the agent's (appdir.HermesHome): $HERMES_HOME,
	// else %LOCALAPPDATA%\hermes on Windows and ~/.hermes elsewhere. rtk
	// (0.51, src/hooks/init/hermes.rs) knows only $HERMES_HOME and
	// ~/.hermes, so it is told the folder: on Windows it otherwise writes
	// its plugin where Hermes doesn't look (新v学员 on Discord).
	"hermes": {
		flags: []string{"--agent", "hermes"}, patch: true,
		has: func(a *agent.Agent) bool { return exists(filepath.Join(hermesPlugin(a), "plugin.yaml")) },
		files: func(a *agent.Agent) []string {
			return []string{filepath.Join(a.Dir, "config.yaml"), filepath.Join(hermesPlugin(a), "plugin.yaml"), filepath.Join(hermesPlugin(a), "__init__.py")}
		},
		remove: func(a *agent.Agent) error {
			return errors.Join(os.RemoveAll(hermesPlugin(a)), dropHermesPlugin(filepath.Join(a.Dir, "config.yaml")))
		},
		dir: func(a *agent.Agent) string { return a.Dir },
		env: func(a *agent.Agent) []string { return []string{"HERMES_HOME=" + a.Dir} },
	},
}

// rtkNoHook are agents RTK has no hook for, and why: the Library lists
// them all the same, so one isn't simply missing as if magpie hadn't seen
// it (Discord, lc: rtk 没有识别 deepseek harness).
//
// DeepSeek Harness: RTK works by rewriting the shell command an agent is
// about to run (git status → rtk git status), and dsh has no way for a hook
// to do that — its pre-tool hooks only allow, deny or ask, and its Claude
// Code hook bridge drops updatedInput (deepseek-harness's
// pre-tool-input-rewrite note is only proposed). rtk init has no
// --agent dsh either (rtk 0.51; rtk-ai/rtk#3847, and #3934 only adds
// instructions to AGENTS.md, which rewrite nothing).
var rtkNoHook = map[string]string{
	"dsh": "RTK has no hook for DeepSeek Harness yet: RTK works by rewriting the shell command an agent is about to run, and dsh's hooks can only allow or deny a command, not change it; rtk init has no --agent dsh either (github.com/rtk-ai/rtk/issues/3847)",
	// rtk init's agents are claude, cursor, codex, copilot, gemini,
	// opencode and the rest of rtkSpecs (src/hooks/init in rtk 1.1.0).
	// There is no grok or grok-app, so a hook written here would have
	// nowhere to go, for Grok Build no more than for the App that runs it.
	// rtk-ai/rtk#2136 asks for Grok Build, and Grok App runs Grok Build,
	// so the two share it on purpose (sharedRTKIssues in the test).
	"grok":     "RTK has no hook for Grok Build yet: rtk init has no Grok agent, so it cannot rewrite the shell commands Grok Build runs (github.com/rtk-ai/rtk/issues/2136)",
	"grok-app": "RTK has no hook for Grok App yet: rtk init has no Grok or Grok App agent, so it cannot rewrite the shell commands Grok App runs (github.com/rtk-ai/rtk/issues/2136)",
	// rtk 1.1.0 has no Qoder agent: rtk-ai/rtk#1327 asks for one, and its
	// PR (#1721) is still open. Qoder CN is Qoder's China build, so the two
	// share it on purpose (sharedRTKIssues in the test).
	"qoder":    "RTK has no hook for Qoder yet: rtk init has no Qoder agent (RTK 1.1.0), so it cannot rewrite the shell commands Qoder CLI runs (github.com/rtk-ai/rtk/issues/1327)",
	"qoder-cn": "RTK has no hook for Qoder CN yet: rtk init has no Qoder agent (RTK 1.1.0), so it cannot rewrite the shell commands Qoder CN's CLI runs (github.com/rtk-ai/rtk/issues/1327)",
	// rtk init --agent cline writes .clinerules into the folder it is run
	// in, asking the model to type rtk itself, and has no hook; it is
	// project-only (instructions_agents.rs in 1.1.0), so there is nothing
	// to give every project. rtk-ai/rtk#3841 asks for a hook through Cline
	// CLI's own hooks.
	"cline": "RTK has no hook for Cline yet: rtk init --agent cline only writes a .clinerules into one project, asking the model to type rtk itself, and nothing for every project (github.com/rtk-ai/rtk/issues/3841)",
	// rtk init --agent kimi adds instructions to one project's AGENTS.md and
	// refuses -g: Kimi Code's PreToolUse hooks can allow or deny a command
	// but not rewrite it (instructions_agents.rs in 1.1.0).
	"kimi": "RTK has no hook for Kimi Code: Kimi Code's hooks can only allow or deny a command, not rewrite it, so rtk init --agent kimi only adds instructions to one project's AGENTS.md and refuses to install for every project",
}

// droidHook is the command rtk's Droid hook runs.
const droidHook = "rtk hook droid"

// droidHookFile is one of Droid's files with PreToolUse hooks, and where
// in it they are.
type droidHookFile struct{ path, key string }

// droidHookFiles are every file Droid reads PreToolUse hooks from, as rtk
// 1.1.0's droid_hook_file_candidates: hooks.json and the legacy
// hooks/hooks.json hold them at the top, settings.json under hooks.
func droidHookFiles(a *agent.Agent) []droidHookFile {
	return []droidHookFile{
		{filepath.Join(a.Dir, "hooks.json"), "PreToolUse"},
		{filepath.Join(a.Dir, "hooks", "hooks.json"), "PreToolUse"},
		{filepath.Join(a.Dir, "settings.json"), "hooks.PreToolUse"},
	}
}

// droidBlocked: rtk has a Droid agent from 0.44.0 on; an older one refuses
// --agent droid.
func droidBlocked(a *agent.Agent) string {
	v := rtkVersionOf(a)
	if v == "" || !older(v, 0, 44) {
		return ""
	}
	return fmt.Sprintf("Droid's hook needs RTK 0.44 or newer, and this RTK is %s, which has no Droid hook. Update RTK (brew upgrade rtk, or its installer again), then switch it on", v)
}

func opencodePlugin(a *agent.Agent) string {
	return filepath.Join(filepath.Dir(a.Path), "plugins", "rtk.ts")
}

// openCodeBlocked: rtk's OpenCode plugin (hooks/opencode/rtk.ts, which
// rtk init --opencode writes to plugins/rtk.ts) was written for OpenCode
// 1's plugin API up to rtk 0.51 — a named export of a function. OpenCode 2
// reads the same plugins folder but takes only a default export of
// {id, setup|effect}, and turns that one away: "Plugin must export a
// default definition with an id and an effect or setup function"
// (rtk-ai/rtk#4311, #3898). rtk 0.52 on (rtk-ai/rtk#4187, in 0.52.0,
// 1.0.0 and 1.0.1) writes one default object with OpenCode 2's id and
// setup() beside OpenCode 1's server(). So with an OpenCode 2 here, rtk is
// given to OpenCode only by an rtk of 0.52 or newer; an older one, or one
// that doesn't say its version, would only add a plugin that fails to load.
func openCodeBlocked(a *agent.Agent) string {
	v := openCodeVersionOf(a)
	if !openCode2(v) {
		return ""
	}
	r := rtkVersionOf(a)
	if r != "" && !older(r, 0, 52) {
		return ""
	}
	if r == "" {
		return fmt.Sprintf("OpenCode %s needs RTK 0.52 or newer, whose plugin OpenCode 2 loads; older ones write a plugin for OpenCode 1, which OpenCode 2 refuses to load (github.com/rtk-ai/rtk/issues/4311). Install or update RTK, then switch it on", v)
	}
	return fmt.Sprintf("OpenCode %s needs RTK 0.52 or newer, and this RTK is %s, whose plugin is written for OpenCode 1: OpenCode 2 refuses to load it (github.com/rtk-ai/rtk/issues/4311). Update RTK (brew upgrade rtk, or its installer again), then switch it on", v, r)
}

// openCode2 says whether OpenCode version v is 2 or later.
func openCode2(v string) bool {
	major, _, _ := strings.Cut(v, ".")
	return major != "" && major != "0" && major != "1"
}

// openCodeHas says whether OpenCode has rtk's plugin. Under OpenCode 2 a
// plugin an rtk before 0.52 wrote (no setup(), OpenCode 1's API only) is
// refused at load. With an rtk here that writes the new one, that old
// plugin doesn't count: the row is off, and switching it on runs rtk's
// installer, which writes the new one over it. With an older rtk it still
// counts, so the row can be switched off to take it out.
func openCodeHas(a *agent.Agent) bool {
	p := opencodePlugin(a)
	if !exists(p) {
		return false
	}
	if contains(p, "setup(") || !openCode2(openCodeVersionOf(a)) {
		return true
	}
	return openCodeBlocked(a) != ""
}

// codexBlocked: rtk has a hook for Codex from 0.50.0 on (rtk hook codex in
// hooks.json); before that rtk init --codex only puts @RTK.md in AGENTS.md,
// which rewrites no command, so an older rtk isn't run for Codex.
func codexBlocked(a *agent.Agent) string {
	v := rtkVersionOf(a)
	if v == "" || !older(v, 0, 50) {
		return ""
	}
	return fmt.Sprintf("Codex's hook needs RTK 0.50 or newer, and this RTK is %s: older ones only add @RTK.md to AGENTS.md, which rewrites no command. Update RTK (brew upgrade rtk, or its installer again), then switch it on", v)
}

// agyPlugin is the folder rtk's installer writes its Antigravity plugin to.
func agyPlugin(a *agent.Agent) string {
	return rtkAt(a, ".gemini", "config", "plugins", "rtk")
}

// agyBlocked: rtk installs for Antigravity globally from 0.51.0 on; 0.50
// knows it only per project and fails rtk init -g --agent antigravity
// ("Antigravity is project-scoped"), so an older rtk isn't run for it.
func agyBlocked(a *agent.Agent) string {
	v := rtkVersionOf(a)
	if v == "" || !older(v, 0, 51) {
		return ""
	}
	return fmt.Sprintf("Antigravity CLI's hook needs RTK 0.51 or newer, and this RTK is %s, which installs it only per project, not for every project. Update RTK (brew upgrade rtk, or its installer again), then switch it on", v)
}

// older says whether version v is before major.minor.
func older(v string, major, minor int) bool {
	var a, b int
	if n, _ := fmt.Sscanf(v, "%d.%d", &a, &b); n < 2 {
		return false
	}
	return a < major || a == major && b < minor
}

// rtkVersion is the version the rtk here says it is, "" when there's none
// or it doesn't say; a test sets it.
var rtkVersion = func() string {
	bin := rtkPath()
	if bin == "" {
		return ""
	}
	out, err := rtkRun(bin, "--version")
	if err != nil {
		return ""
	}
	if m := semver.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// semver finds the version in what opencode --version prints: OpenCode 1
// prints "1.18.32", OpenCode 2 "opencode v2.0.18".
var semver = lazyre.New(`(?:^|[^\w.])v?(\d+\.\d+\.\d+\S*)`)

// openCodeVersion is the version the opencode on this machine says it is,
// "" when there's none or it doesn't say; a test sets it. OpenCode 2's
// installer puts it in ~/.opencode/bin as opencode, with opencode2 beside
// it running it.
var openCodeVersion = func() string {
	name := "opencode"
	if runtime.GOOS == "windows" {
		name = "opencode.exe"
	}
	bin, err := proc.Look("opencode")
	if err != nil {
		bin = filepath.Join(home(), ".opencode", "bin", name)
		if !exists(bin) {
			return ""
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := proc.ProbeContext(ctx, bin, "--version")
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if m := semver.FindStringSubmatch(string(out)); m != nil {
		return m[1]
	}
	return ""
}

func hermesPlugin(a *agent.Agent) string { return filepath.Join(a.Dir, "plugins", "rtk-rewrite") }

// extensionSpec is pi's and omp's: an extension of rtk's own, and nothing else.
func extensionSpec(id string) rtkSpec {
	ext := func(a *agent.Agent) string { return rtkAt(a, "."+id, "agent", "extensions", "rtk.ts") }
	return rtkSpec{
		flags: []string{"--agent", id}, patch: true,
		has:    func(a *agent.Agent) bool { return exists(ext(a)) },
		files:  func(a *agent.Agent) []string { return []string{ext(a)} },
		remove: func(a *agent.Agent) error { return rm(ext(a)) },
		dir:    func(a *agent.Agent) string { return rtkAt(a, "."+id, "agent") },
	}
}

func rm(paths ...string) error {
	var errs []error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dropHook takes the hooks whose command has mark out of the array at key:
// Claude Code's, Codex's and Gemini's are groups, each with its own hooks,
// a group left with none going too; Cursor's are the hooks themselves. An
// array left empty goes, and hooks with it when that leaves it empty.
func dropHook(path, key, mark string) error {
	raw, ok := edit.GetJSON(path, key)
	if !ok {
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal([]byte(raw), &list) != nil {
		return fmt.Errorf("%s: %s isn't a list", path, key)
	}
	var keep []json.RawMessage
	changed := false
	for _, e := range list {
		var h struct {
			Command string            `json:"command"`
			Hooks   []json.RawMessage `json:"hooks"`
		}
		json.Unmarshal(e, &h)
		if strings.Contains(h.Command, mark) {
			changed = true
			continue
		}
		if h.Hooks != nil && bytes.Contains(e, []byte(mark)) {
			var inner []json.RawMessage
			for _, x := range h.Hooks {
				var c struct {
					Command string `json:"command"`
				}
				json.Unmarshal(x, &c)
				if !strings.Contains(c.Command, mark) {
					inner = append(inner, x)
				}
			}
			if len(inner) < len(h.Hooks) {
				changed = true
				if len(inner) == 0 {
					continue
				}
				var g map[string]json.RawMessage
				json.Unmarshal(e, &g)
				g["hooks"], _ = json.Marshal(inner)
				e, _ = json.Marshal(g)
			}
		}
		keep = append(keep, e)
	}
	if !changed {
		return nil
	}
	if len(keep) > 0 {
		return edit.SetJSON(path, edit.KV{Path: key, Value: keep})
	}
	if err := edit.DelJSON(path, key); err != nil {
		return err
	}
	i := strings.LastIndex(key, ".")
	if i < 0 {
		// the array was at the top (Droid's hooks.json): the file stays
		return nil
	}
	parent := key[:i]
	if v, ok := edit.GetJSON(path, parent); ok && strings.TrimSpace(v) == "{}" {
		return edit.DelJSON(path, parent)
	}
	return nil
}

// dropLines takes out the lines match says are rtk's, with the blank line
// rtk put before one; a file left with nothing in it goes.
func dropLines(path string, match func(string) bool) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ls := strings.Split(string(b), "\n")
	var out []string
	for i, l := range ls {
		if !match(l) {
			out = append(out, l)
			continue
		}
		next := ""
		if i+1 < len(ls) {
			next = ls[i+1]
		}
		if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" && strings.TrimSpace(next) == "" {
			out = out[:n-1]
		}
	}
	if len(out) == len(ls) {
		return nil
	}
	return writeOrRemove(path, strings.Join(out, "\n"))
}

// dropBlock takes out the lines from the one with open to the one with
// close, as dropLines does.
func dropBlock(path, open, close string) error {
	in := false
	return dropLines(path, func(l string) bool {
		switch {
		case strings.Contains(l, open):
			in = true
		case in && strings.Contains(l, close):
			in = false
			return true
		}
		return in
	})
}

func writeOrRemove(path, s string) error {
	if strings.TrimSpace(s) == "" {
		return edit.Remove(path)
	}
	return edit.WriteAtomic(path, []byte(s))
}

// dropHermesPlugin takes rtk-rewrite out of Hermes' plugins.enabled, and
// the keys rtk added for it when that leaves them empty.
func dropHermesPlugin(path string) error {
	if err := dropLines(path, func(l string) bool { return strings.TrimSpace(l) == "- rtk-rewrite" }); err != nil {
		return err
	}
	var c struct {
		Plugins map[string]any `yaml:"plugins"`
	}
	b, err := os.ReadFile(path)
	if err != nil || yaml.Unmarshal(b, &c) != nil || c.Plugins == nil {
		return nil
	}
	if l, ok := c.Plugins["enabled"]; ok && (l == nil || fmt.Sprint(l) == "[]") {
		if err := edit.DelYAML(path, "plugins.enabled"); err != nil {
			return err
		}
		delete(c.Plugins, "enabled")
	}
	if len(c.Plugins) == 0 {
		return edit.DelYAML(path, "plugins")
	}
	return nil
}

// RTKAgent is one agent rtk can be given to, and whether it has it.
type RTKAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
	On   bool   `json:"on"`
	// Blocked says why rtk can't be switched on for it here (OpenCode 2);
	// one that has it can still be switched off
	Blocked string `json:"blocked,omitempty"`
	// NoHook: rtk has no hook for it at all (rtkNoHook), and Blocked says why
	NoHook bool `json:"noHook,omitempty"`
	// WSL is the distro an agent in WSL lives in: its hook runs the rtk
	// of that distro (RTKView.WSL), not this machine's
	WSL string `json:"wsl,omitempty"`
	// Asleep: its distro isn't running, so its files aren't read (that
	// would start it) and On isn't known
	Asleep bool `json:"asleep,omitempty"`
}

// RTKGain is what rtk says it saved, over every command it has recorded.
type RTKGain struct {
	Commands int     `json:"commands"`
	Input    int64   `json:"input"`
	Saved    int64   `json:"saved"`
	Pct      float64 `json:"pct"`
}

// RTKDay is what rtk saved on one day it ran commands.
type RTKDay struct {
	Date     string  `json:"date"` // 2006-01-02, local
	Commands int     `json:"commands"`
	Input    int64   `json:"input"`
	Saved    int64   `json:"saved"`
	Pct      float64 `json:"pct"`
}

// RTKView is the RTK part of the Library page.
type RTKView struct {
	Path    string   `json:"path,omitempty"` // "" when rtk isn't installed
	Version string   `json:"version,omitempty"`
	Gain    *RTKGain `json:"gain,omitempty"`
	// GainErr is why what rtk saved isn't known: rtk gain failed or
	// printed nothing readable
	GainErr string `json:"gainErr,omitempty"`
	// CodexSandbox is the Windows sandbox Codex runs its commands in,
	// when it has rtk: what rtk saves there isn't counted in Gain
	CodexSandbox string `json:"codexSandbox,omitempty"`
	// Days are what it saved each day it ran commands, oldest first
	Days []RTKDay `json:"days,omitempty"`
	// Latest is rtk's latest release, "" when it isn't known (see
	// CheckLatest); Upgrade the command Upgrade RTK runs, "" when magpie
	// doesn't know how this rtk was installed
	Latest  string `json:"latest,omitempty"`
	Upgrade string `json:"upgrade,omitempty"`
	// NoUpgrade is why there is no Upgrade when magpie knows: "cargo", an
	// rtk cargo installed with no cargo to be found to upgrade it, which
	// the page says with Manual, the command to run in a terminal once
	// there is (新v学员: its Upgrade button only did nothing)
	NoUpgrade string `json:"noUpgrade,omitempty"`
	Manual    string `json:"manual,omitempty"`
	// Admin is the folder rtk is in when only an administrator can change
	// it (winget's for every user, under Program Files): Upgrade asks
	// Windows for one first
	Admin string `json:"admin,omitempty"`
	// Waiting is the package manager this rtk was installed with (winget,
	// Homebrew) when Latest is out but it has nothing newer than this rtk
	// yet, WaitingHas what it has: an upgrade through it would change
	// nothing, so the page offers none (#1025)
	Waiting    string `json:"waiting,omitempty"`
	WaitingHas string `json:"waitingHas,omitempty"`
	// Note is what an upgrade left to say: Homebrew's rtk behind rtk's
	// own release
	Note string `json:"note,omitempty"`
	// OffPath: rtk isn't on the PATH an agent started now gets, so its
	// hooks, which run rtk by name, do nothing (Pi's says "rtk binary not
	// found in PATH"). PathDir is where Put RTK on PATH makes it found — a
	// link to it made in that folder (PathLink), or (Windows) that folder,
	// rtk's own, added to the user's PATH; "" when magpie has nowhere
	PathDir  string     `json:"pathDir,omitempty"`
	PathLink bool       `json:"pathLink,omitempty"`
	OffPath  bool       `json:"offPath,omitempty"`
	Agents   []RTKAgent `json:"agents"`
	// WSL is the rtk of each WSL distro an agent listed lives in
	WSL []RTKDistro `json:"wsl,omitempty"`
	URL string      `json:"url"`
	// Install is the command Install RTK runs, shown before it is clicked
	Install string `json:"install,omitempty"`
	// Restart are the agents a change reached, to be restarted to see it
	Restart []string `json:"restart,omitempty"`
	Backup  string   `json:"backup,omitempty"`
}

var rtkMu sync.Mutex

// rtkPath is rtk's, "" when it isn't installed; one installed since magpie
// started may not be on its PATH yet, so where the installers put it is
// looked in too (Homebrew's are on it from the start: proc.UserPath).
// winget's is in its package folder, linked from WinGet\Links only when
// winget could make the link. Found there, it may not be on the PATH the
// agents get: see rtkReached.
func rtkPath() string {
	if p, err := proc.Look("rtk"); err == nil {
		return p
	}
	name, dirs := "rtk", append([]string{filepath.Join(home(), ".local", "bin")}, cargoBins()...)
	if runtime.GOOS == "windows" {
		winget := filepath.Join(appdir.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet")
		name, dirs = "rtk.exe", append([]string{filepath.Join(winget, "Links")}, cargoBins()...)
		if appdir.Getenv("LOCALAPPDATA") != "" {
			pkgs, _ := filepath.Glob(filepath.Join(winget, "Packages", "rtk-ai.rtk_*"))
			dirs = append(dirs, pkgs...)
		}
	}
	for _, d := range dirs {
		if p := filepath.Join(d, name); exists(p) {
			return p
		}
	}
	return ""
}

func rtkRun(bin string, args ...string) (string, error) { return rtkRunEnv(bin, nil, args...) }

func rtkRunEnv(bin string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, args...)
	// rtk asks before a change it isn't told to make: nothing answers it
	cmd.Stdin = nil
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("rtk %s: %s", strings.Join(args, " "), lastLines(text, 4))
	}
	return text, nil
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " ")
}

// detected is the agents on this machine, as agent.Detected says; a var for
// tests. Detecting them probes a file or a command for every agent magpie
// knows of, which is a few hundred milliseconds, so it is asked once per read
// and not once per pass over the list (see ReadRTK).
var detected = agent.Detected

// rtkAgents are the agents on this machine rtk can be given to. They are as
// detected says, so a caller with a list already in hand passes it rather
// than have them detected again.
func rtkAgents(agents []*agent.Agent) []*agent.Agent {
	if agents == nil {
		agents = detected()
	}
	var out []*agent.Agent
	for _, a := range agents {
		sp, ok := rtkSpecs[rtkKind(a.ID)]
		// an uninstall that left the folder behind (#2254) isn't given rtk,
		// as the library gives it nothing (Targets); one with rtk's hook
		// still shows, so the hook can be taken out
		if ok && (!a.CLIMissing() || sp.has(a)) {
			out = append(out, a)
		}
	}
	return out
}

// ReadRTK says whether rtk is installed, what it saved, and which agents
// have it.
func ReadRTK() *RTKView {
	v := &RTKView{Agents: []RTKAgent{}, URL: RTKURL, Path: rtkPath()}
	if v.Path == "" {
		if c := rtkInstaller(); c != nil {
			v.Install = shown(c)
		}
	}
	// the agents are detected once and the two passes below share them
	agents := detected()
	for _, a := range rtkAgents(agents) {
		sp := rtkSpecs[rtkKind(a.ID)]
		ra := RTKAgent{ID: a.ID, Name: a.Name, Icon: a.Icon, WSL: a.WSL}
		// a stopped distro's files aren't opened: that starts it (#3896)
		if a.WSL != "" && !rtkWSLLive(a) {
			ra.Asleep = true
			v.Agents = append(v.Agents, ra)
			continue
		}
		ra.On = sp.has(a)
		if sp.blocked != nil {
			ra.Blocked = sp.blocked(a)
		}
		v.Agents = append(v.Agents, ra)
	}
	for _, a := range agents {
		if why, ok := rtkNoHook[rtkKind(a.ID)]; ok && !a.CLIMissing() {
			v.Agents = append(v.Agents, RTKAgent{ID: a.ID, Name: a.Name, Icon: a.Icon, Blocked: why, NoHook: true, WSL: a.WSL})
		}
	}
	v.WSL = rtkDistros(v.Agents)
	if v.Path == "" {
		return v
	}
	if !rtkReached() {
		v.OffPath = true
		v.PathDir, v.PathLink = rtkPathDir(v.Path)
	}
	if out, err := rtkRun(v.Path, "--version"); err == nil {
		v.Version = strings.TrimSpace(strings.TrimPrefix(out, "rtk"))
	}
	if c := upgraderOf(v.Path); c != nil {
		v.Upgrade, v.Admin = shown(c), rtkAdminFolder(c, v.Path)
	} else {
		if v.NoUpgrade = rtkNoUpgrade(v.Path); v.NoUpgrade == "cargo" {
			v.Manual = rtkCargoCmd
		}
	}
	v.Gain, v.Days, v.GainErr = rtkGain(v.Path)
	if runtime.GOOS == "windows" {
		for _, a := range v.Agents {
			if a.ID == "codex" && a.On {
				v.CodexSandbox = codexSandbox()
			}
		}
	}
	return v
}

// rtkGain is what rtk says it saved (rtk gain, from its history database:
// every command run through rtk on this machine by this user, from any
// agent or terminal, since its history began), and the days it saved it on,
// oldest first; nil and why when rtk didn't say, which the page shows rather
// than "nothing saved yet" (#741).
//
// Only its stdout is JSON: what rtk writes to stderr — a warning about a
// hook, a console code page, a corrupted table — went into the same buffer
// and made all of it unreadable, so it is read apart, and a line printed
// around the JSON object is passed over too.
func rtkGain(bin string) (*RTKGain, []RTKDay, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// --daily adds the days to the summary (rtk 0.28 on)
	cmd := proc.CommandContext(ctx, bin, "gain", "--daily", "--format", "json")
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		said := installerSaid(stderr.String()+"\n"+stdout.String(), 3)
		if said == "" {
			said = err.Error()
		}
		return nil, nil, "rtk gain: " + said
	}
	return parseGain(stdout.String())
}

func parseGain(out string) (*RTKGain, []RTKDay, string) {
	var g struct {
		Summary *struct {
			Commands int     `json:"total_commands"`
			Input    int64   `json:"total_input"`
			Saved    int64   `json:"total_saved"`
			Pct      float64 `json:"avg_savings_pct"`
		} `json:"summary"`
		Daily []struct {
			Date     string  `json:"date"`
			Commands int     `json:"commands"`
			Input    int64   `json:"input_tokens"`
			Saved    int64   `json:"saved_tokens"`
			Pct      float64 `json:"savings_pct"`
		} `json:"daily"`
	}
	i, j := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if i < 0 || j < i || json.Unmarshal([]byte(out[i:j+1]), &g) != nil || g.Summary == nil {
		return nil, nil, strings.TrimSuffix("rtk gain printed no summary magpie can read: "+installerSaid(out, 2), ": ")
	}
	s := g.Summary
	if s.Commands == 0 {
		return nil, nil, ""
	}
	var days []RTKDay
	for _, d := range g.Daily {
		days = append(days, RTKDay{Date: d.Date, Commands: d.Commands, Input: d.Input, Saved: d.Saved, Pct: d.Pct})
	}
	slices.SortFunc(days, func(a, b RTKDay) int { return strings.Compare(a.Date, b.Date) })
	return &RTKGain{Commands: s.Commands, Input: s.Input, Saved: s.Saved, Pct: s.Pct}, days, ""
}

// codexSandbox is the Windows sandbox Codex runs its commands in, from its
// config.toml ([windows] sandbox, or the features it replaced): "elevated"
// runs them as Codex's own sandbox account (CodexSandboxOffline/Online,
// started with that account's profile), so the rtk its hook puts in front
// of them keeps its history in that account's AppData, not the user's;
// "unelevated" runs them with a token that can't write the user's. Either
// way rtk gain here doesn't count them. "" when Codex doesn't sandbox them.
// It is parsed again only when config.toml changes: one with a table for
// each of thousands of projects is ~0.2s of CPU to parse (#1665).
func codexSandbox() string {
	s, _ := filememo.Read("rtk codex sandbox", filepath.Join(codexDir(), "config.toml"), func(raw []byte) (string, error) {
		return codexSandboxIn(raw), nil
	})
	return s
}

// codexSandboxIn is codexSandbox of config.toml's bytes.
func codexSandboxIn(raw []byte) string {
	var c struct {
		SandboxMode string `toml:"sandbox_mode"`
		Windows     struct {
			Sandbox string `toml:"sandbox"`
		} `toml:"windows"`
		Features map[string]any `toml:"features"`
	}
	if toml.Unmarshal(edit.TOMLWithout(raw, "projects"), &c) != nil || c.SandboxMode == "danger-full-access" {
		return ""
	}
	switch {
	case c.Windows.Sandbox != "":
		if c.Windows.Sandbox == "elevated" || c.Windows.Sandbox == "unelevated" {
			return c.Windows.Sandbox
		}
	case c.Features["elevated_windows_sandbox"] == true:
		return "elevated"
	case c.Features["experimental_windows_sandbox"] == true, c.Features["enable_experimental_windows_sandbox"] == true:
		return "unelevated"
	}
	return ""
}

// shown is a command as the page shows it: a script's own line.
func shown(c []string) string {
	if c[0] == "sh" {
		return c[2]
	}
	if c[0] == rtkWindowsRelease {
		// magpie's own action, not a command line: name what it does
		// rather than the action and the path the user's rtk is at (#2006)
		return "rtk's own Windows release"
	}
	return strings.Join(c, " ")
}

// SetRTK gives rtk to an agent with rtk's own installer, or takes it away
// by taking out what that put in — which needs no rtk, so an agent left
// with the hook of an rtk since removed can be put right.
func SetRTK(id string, on bool) (*RTKView, error) {
	rtkMu.Lock()
	defer rtkMu.Unlock()
	var a *agent.Agent
	agents := detected()
	for _, x := range rtkAgents(agents) {
		if x.ID == id {
			a = x
		}
	}
	kind := rtkKind(id)
	sp, ok := rtkSpecs[kind]
	switch {
	case !ok && rtkNoHook[kind] != "":
		return nil, errors.New(rtkNoHook[kind])
	case !ok:
		return nil, fmt.Errorf("rtk has no hook for %s", id)
	case a == nil:
		return nil, fmt.Errorf("%s isn't installed", id)
	case a.WSL != "" && !rtkWSLLive(a):
		// nothing in it is read or run: that would start it (#3896)
		return nil, fmt.Errorf("WSL %s isn't running, and magpie doesn't start it to change RTK for %s: start the distro (open its terminal), then switch it again", a.WSL, a.Name)
	case sp.has(a) == on:
		return ReadRTK(), nil
	}
	if on && sp.blocked != nil {
		if why := sp.blocked(a); why != "" {
			return nil, errors.New(why)
		}
	}
	bin := ""
	if a.WSL != "" {
		// the hook runs in the distro, so it is the distro's rtk that is
		// run for it: a Windows rtk.exe is no path there
		if on {
			d := rtkDistroOf(a.WSL, true)
			if bin = d.Path; bin == "" {
				return nil, fmt.Errorf("RTK isn't installed in WSL %s — install it there first (magpie library rtk install %s)", a.WSL, a.WSL)
			}
		}
	} else if bin = rtkPath(); on && bin == "" {
		return nil, fmt.Errorf("rtk isn't installed — install it first (%s)", RTKURL)
	}
	b := newBackups()
	for _, p := range sp.files(a) {
		if err := b.keep(a.ID, p); err != nil {
			return nil, err
		}
	}
	if on {
		if err := rtkInstall(bin, a); err != nil {
			return nil, err
		}
	} else {
		if err := sp.remove(a); err != nil {
			return nil, fmt.Errorf("taking rtk out of %s: %w", a.Name, err)
		}
		if sp.has(a) {
			return nil, fmt.Errorf("rtk's hook is still in %s", a.Name)
		}
	}
	if b.dir != "" {
		pruneBackups()
	}
	v := ReadRTK()
	v.Restart, v.Backup = []string{a.ID}, b.dir
	return v, nil
}

func rtkInstall(bin string, a *agent.Agent) error {
	sp := rtkSpecs[rtkKind(a.ID)]
	// a folder in a WSL distro that is a link it couldn't resolve isn't
	// written through (#4502)
	if err := a.Unreached(); err != nil {
		return err
	}
	if _, err := agent.WSLOpen(a, sp.dir(a)); err != nil {
		return err
	}
	if err := os.MkdirAll(sp.dir(a), 0o755); err != nil {
		return err
	}
	if a.WSL != "" {
		return rtkInstallWSL(bin, a, sp)
	}
	args := append([]string{"init", "-g"}, sp.flags...)
	if sp.patch {
		args = append(args, "--auto-patch")
	}
	var env []string
	if sp.withClaude {
		tmp, err := os.MkdirTemp("", "magpie-rtk-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		env = []string{"CLAUDE_CONFIG_DIR=" + tmp}
	}
	if sp.env != nil {
		env = append(env, sp.env(a)...)
	}
	out, err := rtkRunEnv(bin, env, args...)
	if err != nil {
		return err
	}
	if !sp.has(a) {
		return fmt.Errorf("rtk's installer didn't add its hook to %s: %s", a.Name, lastLines(out, 3))
	}
	return nil
}

// rtkScript is rtk's own installer, which puts it in ~/.local/bin.
const rtkScript = "https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh"

// rtkInstaller is how rtk is installed here: with Homebrew when it has
// rtk's bottle for this computer, winget on Windows, else rtk's own
// script; nil when there's no way.
func rtkInstaller() []string {
	if runtime.GOOS == "windows" {
		if _, err := proc.Look("winget"); err == nil {
			return []string{"winget", "install", "--id", "rtk-ai.rtk", "--exact", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
		}
		return nil
	}
	if brew, err := proc.Look("brew"); err == nil && brewBottled(brew) {
		return []string{"brew", "install", "rtk"}
	}
	if _, err := proc.Look("curl"); err == nil {
		return []string{"sh", "-c", "curl -fsSL " + rtkScript + " | sh"}
	}
	return nil
}

// brewBottled says whether Homebrew installs rtk on this computer from its
// bottle, a download of seconds. Without one, `brew install rtk` builds rtk
// from source, and first the Rust, LLVM and Python it builds with: hours,
// while Install RTK waits five minutes, so it never finished and was ended
// each time it was tried again (Scott__D on X). Homebrew bottles rtk for
// Apple silicon on the newest macOS versions only: not for an Intel Mac,
// nor for macOS 14 and older (brew there would install 19 formulae, rust
// and llvm among them, from source). brew info says which: "rtk: stable
// 1.1.0 (bottled)" only when brew would pour the bottle here. A brew that
// doesn't say, or doesn't answer, is not used: rtk's own script, which
// downloads rtk's release for this system, is.
//
// ReadRTK asks it on every read while rtk isn't installed, so the answer is
// kept for brewBottledFor, per brew.
var brewBottled = func(brew string) bool {
	bottled.Lock()
	defer bottled.Unlock()
	if bottled.brew == brew && time.Since(bottled.at) < brewBottledFor {
		return bottled.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), brewInfoWait)
	defer cancel()
	cmd := proc.CommandContext(ctx, brew, "info", "rtk")
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1")
	out, err := cmd.Output()
	ok := err == nil && brewSaysBottled(string(out))
	bottled.brew, bottled.ok, bottled.at = brew, ok, time.Now()
	return ok
}

var bottled struct {
	sync.Mutex
	brew string
	ok   bool
	at   time.Time
}

const brewBottledFor = time.Hour

// brewInfoWait is how long brew info is given: it may first download
// Homebrew's formula list when its copy is old.
var brewInfoWait = 30 * time.Second

// brewSaysBottled reads brew info rtk's first line, "==> rtk: stable 1.1.0
// (bottled), HEAD".
func brewSaysBottled(out string) bool {
	for _, l := range strings.Split(ansiSeq.ReplaceAllString(out, ""), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "==> rtk: ") {
			return strings.Contains(l, "(bottled)")
		}
	}
	return false
}

// InstallRTK installs rtk, when the user asks for it.
func InstallRTK() (*RTKView, error) {
	rtkMu.Lock()
	defer rtkMu.Unlock()
	if rtkPath() != "" {
		return ReadRTK(), nil
	}
	c := rtkInstaller()
	if c == nil {
		return nil, fmt.Errorf("no way to install rtk here — get it from %s", RTKURL)
	}
	out, err := runRTKInstaller(c, 5*time.Minute, "", "HOMEBREW_NO_AUTO_UPDATE=1")
	if err != nil {
		return nil, err
	}
	v := ReadRTK()
	if v.Path == "" {
		return nil, fmt.Errorf("%s ran, but rtk still isn't found: %s", strings.Join(c, " "), lastLines(string(out), 3))
	}
	return v, nil
}

// RTKTakes is the id of the agent q names when rtk can be given to it.
func RTKTakes(q string) (string, error) {
	a, err := agent.Find(q)
	if err != nil {
		return "", err
	}
	if why := rtkNoHook[rtkKind(a.ID)]; why != "" {
		return "", errors.New(why)
	}
	if _, ok := rtkSpecs[rtkKind(a.ID)]; !ok {
		var ids []string
		for id := range rtkSpecs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return "", fmt.Errorf("rtk has no hook for %s (it has for %s)", a.Name, strings.Join(ids, ", "))
	}
	return a.ID, nil
}
