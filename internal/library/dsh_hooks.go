package library

// DeepSeek Harness has no hooks file of its own. Its plugin
// @deepseek-ai/dsh-hooks-claude-code runs a Claude Code hooks.json on dsh's
// interception points (session start, prompt, tool before and after, turn
// end, subagents), and a profile loads it through a row its patch list
// inserts:
//
//	- insert: # magpie
//	    - id: magpie-hooks
//	      name: "@deepseek-ai/dsh-hooks-claude-code"
//	      config:
//	        configPath: "<DSH_HOME>/magpie-hooks.json"
//
// magpie keeps its hooks in $DSH_HOME/magpie-hooks.json, in Claude Code's
// shape (hooks.go writes it as it does Claude's), and gives every profile
// that row. The plugin is a profile's own npm package, at the dsh's own
// version (its peer dependencies are pinned to it), so magpie installs it
// with `dsh plugin --profile <name> add` first and writes the row only once
// it is there: a row for a plugin that isn't installed is one dsh warns of
// and leaves out, and a deny hook would then allow what it was set to stop.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
)

const (
	dshHooksPkg      = "@deepseek-ai/dsh-hooks-claude-code"
	dshHooksProtocol = "@deepseek-ai/dsh-hook-protocol"
	dshHooksRowID    = "magpie-hooks"
)

// dshHookEvents are the events the bridge runs (CLAUDE_EVENTS in
// dsh-hooks-claude-code 0.2.2-rc.5); a hook under another Claude Code event
// is one it never runs.
var dshHookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SubagentStart", "SubagentStop"}

// dshHookSpec is how dsh keeps hooks, or why it can't. Its tools are bash,
// pwsh, read, write, edit and str_replace_editor (dsh-agent's tool names),
// and the bridge reads a matcher of names joined by | as exact names.
func dshHookSpec(t *Target) (*hookSpec, string) {
	if t.MCP == nil || filepath.Base(t.MCP.Path) != "cordis.patch.yml" {
		return nil, "DeepSeek Harness has no profile yet: start dsh once, and hooks can be given to it"
	}
	return &hookSpec{
		path: filepath.Join(t.Agent.Dir, "magpie-hooks.json"),
		events: map[string]string{"turn-end": "Stop", "after-tool": "PostToolUse", "before-tool": "PreToolUse",
			"prompt": "UserPromptSubmit", "session-start": "SessionStart"},
		native: dshHookEvents,
		tools:  map[string]string{"shell": "bash|pwsh", "edit": "write|edit|str_replace_editor"},
	}, ""
}

// dshInstalled is the version of an npm package a profile has, "" when it
// has none.
func dshInstalled(profile, pkg string) string {
	b, err := os.ReadFile(filepath.Join(profile, "node_modules", filepath.FromSlash(pkg), "package.json"))
	if err != nil {
		return ""
	}
	var v struct {
		Version string `json:"version"`
	}
	json.Unmarshal(b, &v)
	return v.Version
}

// dshTool is the version and command of the dsh a; a var for tests.
var dshTool = func(a *agent.Agent) (string, string) { return a.InstalledVersion(), a.BinPath() }

// dshPluginRun runs `dsh <args>` in cwd, a profile's folder, under
// DSH_HOME dir; a var for tests.
var dshPluginRun = func(bin, dir, cwd string, args ...string) (string, error) {
	const timeout = 3 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "DSH_HOME="+dir, "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return text, fmt.Errorf("dsh %s didn't finish in %s", strings.Join(args, " "), timeout)
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("dsh %s: %s", strings.Join(args, " "), lastLines(text, 4))
	}
	return text, nil
}

// dshInstallFailed remembers an install that failed, per profile and
// version, so a sync that runs every few seconds doesn't wait on the
// network again each time.
var (
	dshInstallMu     sync.Mutex
	dshInstallFailed = map[string]time.Time{}
)

const dshInstallRetry = 10 * time.Minute

// dshHooksPlugin brings every profile to what the hooks need: with any
// hook (want), the plugin installed at dsh's version and the row in its
// patch list; with none, the row taken out. A profile that fails is said
// in the error, the others done.
func dshHooksPlugin(t *Target, file string, want bool, keep func(path string) error) error {
	dir := t.Agent.Dir
	var ver, bin string
	if want {
		ver, bin = dshTool(t.Agent)
		if ver == "" || bin == "" {
			return errors.New("DeepSeek Harness's version or command isn't known, so its hooks plugin can't be installed")
		}
	}
	var errs []error
	for _, patch := range agent.DshPatchFiles(dir) {
		if filepath.Base(patch) != "cordis.patch.yml" {
			continue
		}
		profile := filepath.Dir(patch)
		name := filepath.Base(profile)
		if !want {
			if err := dshDelHooksRow(patch, keep); err != nil {
				errs = append(errs, fmt.Errorf("profile %s: %w", name, err))
			}
			continue
		}
		if dshInstalled(profile, dshHooksPkg) != ver || dshInstalled(profile, dshHooksProtocol) != ver {
			key := profile + "\x00" + ver
			dshInstallMu.Lock()
			at, failed := dshInstallFailed[key]
			dshInstallMu.Unlock()
			if failed && time.Since(at) < dshInstallRetry {
				errs = append(errs, fmt.Errorf("profile %s: the hooks plugin didn't install a moment ago; trying again later", name))
				// a plugin the user removed leaves no row of magpie's behind
				if err := dshDelHooksRow(patch, keep); err != nil {
					errs = append(errs, fmt.Errorf("profile %s: %w", name, err))
				}
				continue
			}
			_, err := dshPluginRun(bin, dir, profile, "plugin", "--profile", name, "add", dshHooksProtocol+"@"+ver, dshHooksPkg+"@"+ver)
			if err == nil && (dshInstalled(profile, dshHooksPkg) != ver || dshInstalled(profile, dshHooksProtocol) != ver) {
				err = fmt.Errorf("%s isn't installed after its install", dshHooksPkg)
			}
			dshInstallMu.Lock()
			if err != nil {
				dshInstallFailed[key] = time.Now()
			} else {
				delete(dshInstallFailed, key)
			}
			dshInstallMu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("profile %s: %w", name, err))
				// the plugin isn't there, so no row of magpie's points at it
				if derr := dshDelHooksRow(patch, keep); derr != nil {
					errs = append(errs, fmt.Errorf("profile %s: %w", name, derr))
				}
				continue
			}
		}
		if err := dshPutHooksRow(patch, file, keep); err != nil {
			errs = append(errs, fmt.Errorf("profile %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// dshHooksItem is the index of magpie's hooks insert in the list, -1 when
// there is none, and whether a row of the user's loads the same plugin.
func (p *dshPatches) dshHooksItem() (mine int, theirs bool) {
	mine = -1
	for i, it := range p.items {
		switch {
		case strings.TrimSpace(it[0]) == dshMark && len(it) > 1 && strings.TrimSpace(it[1]) == "- id: "+dshHooksRowID:
			mine = i
		case strings.Contains(strings.Join(it, "\n"), dshHooksPkg):
			theirs = true
		}
	}
	return mine, theirs
}

func dshPutHooksRow(patch, file string, keep func(string) error) error {
	p, err := dshRead(patch)
	if err != nil {
		return err
	}
	i, theirs := p.dshHooksItem()
	if theirs {
		return errors.New("its patch list loads the hooks plugin already, with a config of its own; magpie leaves that one")
	}
	lines := []string{
		dshMark,
		"    - id: " + dshHooksRowID,
		"      name: " + dshScalar(dshHooksPkg),
		"      config:",
		"        configPath: " + dshScalar(file),
	}
	if i >= 0 {
		if strings.Join(p.items[i], "\n") == strings.Join(lines, "\n") {
			return nil
		}
		p.items[i] = lines
	} else {
		p.items = append(p.items, lines)
	}
	if err := keep(patch); err != nil {
		return err
	}
	return p.write(patch)
}

func dshDelHooksRow(patch string, keep func(string) error) error {
	p, err := dshRead(patch)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	i, _ := p.dshHooksItem()
	if i < 0 {
		return nil
	}
	p.items = append(p.items[:i], p.items[i+1:]...)
	if err := keep(patch); err != nil {
		return err
	}
	return p.write(patch)
}
