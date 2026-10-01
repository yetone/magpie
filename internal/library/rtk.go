package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
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
	// blocked says why rtk's installer mustn't be run for the agent as it
	// is on this machine, "" when it can be
	blocked func() string
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
		has: func(*agent.Agent) bool {
			return contains(filepath.Join(claudeDir(), "settings.json"), "rtk hook claude")
		},
		files: func(*agent.Agent) []string {
			d := claudeDir()
			return []string{filepath.Join(d, "settings.json"), filepath.Join(d, "CLAUDE.md"), filepath.Join(d, "RTK.md")}
		},
		remove: func(*agent.Agent) error {
			d := claudeDir()
			return errors.Join(
				dropHook(filepath.Join(d, "settings.json"), "hooks.PreToolUse", "rtk hook claude"),
				dropLines(filepath.Join(d, "CLAUDE.md"), rtkRef),
				rm(filepath.Join(d, "RTK.md")))
		},
		dir: func(*agent.Agent) string { return claudeDir() },
	},
	"codex": {
		flags:   []string{"--codex"},
		blocked: codexBlocked,
		has:     func(*agent.Agent) bool { return contains(filepath.Join(codexDir(), "hooks.json"), "rtk hook codex") },
		files: func(*agent.Agent) []string {
			d := codexDir()
			return []string{filepath.Join(d, "hooks.json"), filepath.Join(d, "AGENTS.md"), filepath.Join(d, "RTK.md")}
		},
		remove: func(*agent.Agent) error {
			d := codexDir()
			return errors.Join(
				dropHook(filepath.Join(d, "hooks.json"), "hooks.PreToolUse", "rtk hook codex"),
				dropLines(filepath.Join(d, "AGENTS.md"), rtkRef),
				rm(filepath.Join(d, "RTK.md")))
		},
		dir: func(*agent.Agent) string { return codexDir() },
	},
	"gemini": {
		// --hook-only: without it the installer writes its own GEMINI.md
		// over the user's
		flags: []string{"--gemini", "--hook-only"}, patch: true,
		has: func(*agent.Agent) bool {
			return contains(filepath.Join(home(), ".gemini", "settings.json"), "rtk-hook-gemini")
		},
		files: func(*agent.Agent) []string {
			d := filepath.Join(home(), ".gemini")
			return []string{filepath.Join(d, "settings.json"), filepath.Join(d, "hooks", "rtk-hook-gemini.sh"), filepath.Join(d, "hooks", ".rtk-hook.sha256")}
		},
		remove: func(*agent.Agent) error {
			d := filepath.Join(home(), ".gemini")
			err := errors.Join(
				dropHook(filepath.Join(d, "settings.json"), "hooks.BeforeTool", "rtk-hook-gemini"),
				rm(filepath.Join(d, "hooks", "rtk-hook-gemini.sh"), filepath.Join(d, "hooks", ".rtk-hook.sha256")))
			os.Remove(filepath.Join(d, "hooks")) // when nothing else is in it
			return err
		},
		dir: func(*agent.Agent) string { return filepath.Join(home(), ".gemini") },
	},
	"opencode": {
		flags: []string{"--opencode"}, patch: true, withClaude: true, blocked: openCodeBlocked,
		has:    func(a *agent.Agent) bool { return exists(opencodePlugin(a)) },
		files:  func(a *agent.Agent) []string { return []string{opencodePlugin(a)} },
		remove: func(a *agent.Agent) error { return rm(opencodePlugin(a)) },
		dir:    func(a *agent.Agent) string { return filepath.Dir(a.Path) },
	},
	"cursor": {
		flags: []string{"--agent", "cursor"}, patch: true, withClaude: true,
		has: func(*agent.Agent) bool {
			return contains(filepath.Join(home(), ".cursor", "hooks.json"), "rtk hook cursor")
		},
		files: func(*agent.Agent) []string { return []string{filepath.Join(home(), ".cursor", "hooks.json")} },
		remove: func(*agent.Agent) error {
			return dropHook(filepath.Join(home(), ".cursor", "hooks.json"), "hooks.preToolUse", "rtk hook cursor")
		},
		dir: func(*agent.Agent) string { return filepath.Join(home(), ".cursor") },
	},
	"copilot": {
		flags: []string{"--copilot"}, patch: true,
		has: func(*agent.Agent) bool { return exists(filepath.Join(home(), ".copilot", "hooks", "rtk-rewrite.json")) },
		files: func(*agent.Agent) []string {
			d := filepath.Join(home(), ".copilot")
			return []string{filepath.Join(d, "hooks", "rtk-rewrite.json"), filepath.Join(d, "copilot-instructions.md")}
		},
		remove: func(*agent.Agent) error {
			d := filepath.Join(home(), ".copilot")
			return errors.Join(
				rm(filepath.Join(d, "hooks", "rtk-rewrite.json")),
				dropBlock(filepath.Join(d, "copilot-instructions.md"), "<!-- rtk-instructions", "<!-- /rtk-instructions -->"))
		},
		dir: func(*agent.Agent) string { return filepath.Join(home(), ".copilot") },
	},
	"pi":  extensionSpec("pi"),
	"omp": extensionSpec("omp"),
	"hermes": {
		flags: []string{"--agent", "hermes"}, patch: true,
		has: func(*agent.Agent) bool { return exists(filepath.Join(hermesPlugin(), "plugin.yaml")) },
		files: func(*agent.Agent) []string {
			return []string{filepath.Join(home(), ".hermes", "config.yaml"), filepath.Join(hermesPlugin(), "plugin.yaml"), filepath.Join(hermesPlugin(), "__init__.py")}
		},
		remove: func(*agent.Agent) error {
			return errors.Join(os.RemoveAll(hermesPlugin()), dropHermesPlugin(filepath.Join(home(), ".hermes", "config.yaml")))
		},
		dir: func(*agent.Agent) string { return filepath.Join(home(), ".hermes") },
	},
}

func opencodePlugin(a *agent.Agent) string {
	return filepath.Join(filepath.Dir(a.Path), "plugins", "rtk.ts")
}

// openCodeBlocked: rtk's OpenCode plugin (hooks/opencode/rtk.ts, which
// rtk init --opencode writes to plugins/rtk.ts, up to rtk 0.50 and its
// develop branch) is written for OpenCode 1's plugin API — a named export
// of a function. OpenCode 2 reads the same plugins folder but takes only a
// default export of {id, setup|effect}, and turns rtk's away: "Plugin must
// export a default definition with an id and an effect or setup function"
// (rtk-ai/rtk#4311, #3898; the fix, #4187, isn't merged). So with an
// OpenCode 2 here, rtk isn't given to OpenCode: it would only add a plugin
// that fails to load.
func openCodeBlocked() string {
	v := openCodeVersion()
	if major, _, _ := strings.Cut(v, "."); major != "" && major != "0" && major != "1" {
		return fmt.Sprintf("RTK doesn't support OpenCode 2 yet: its plugin is written for OpenCode 1, and OpenCode %s refuses to load it (\"Plugin must export a default definition with an id and an effect or setup function\", github.com/rtk-ai/rtk/issues/4311)", v)
	}
	return ""
}

// codexBlocked: rtk has a hook for Codex from 0.50.0 on (rtk hook codex in
// hooks.json); before that rtk init --codex only puts @RTK.md in AGENTS.md,
// which rewrites no command, so an older rtk isn't run for Codex.
func codexBlocked() string {
	v := rtkVersion()
	if v == "" || !older(v, 0, 50) {
		return ""
	}
	return fmt.Sprintf("Codex's hook needs RTK 0.50 or newer, and this RTK is %s: older ones only add @RTK.md to AGENTS.md, which rewrites no command. Update RTK (brew upgrade rtk, or its installer again), then switch it on", v)
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
var semver = regexp.MustCompile(`(?:^|[^\w.])v?(\d+\.\d+\.\d+\S*)`)

// openCodeVersion is the version the opencode on this machine says it is,
// "" when there's none or it doesn't say; a test sets it. OpenCode 2's
// installer puts it in ~/.opencode/bin as opencode, with opencode2 beside
// it running it.
var openCodeVersion = func() string {
	name := "opencode"
	if runtime.GOOS == "windows" {
		name = "opencode.exe"
	}
	bin, err := exec.LookPath("opencode")
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

func hermesPlugin() string { return filepath.Join(home(), ".hermes", "plugins", "rtk-rewrite") }

// extensionSpec is pi's and omp's: an extension of rtk's own, and nothing else.
func extensionSpec(id string) rtkSpec {
	ext := func() string { return filepath.Join(home(), "."+id, "agent", "extensions", "rtk.ts") }
	return rtkSpec{
		flags: []string{"--agent", id}, patch: true,
		has:    func(*agent.Agent) bool { return exists(ext()) },
		files:  func(*agent.Agent) []string { return []string{ext()} },
		remove: func(*agent.Agent) error { return rm(ext()) },
		dir:    func(*agent.Agent) string { return filepath.Join(home(), "."+id, "agent") },
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
	parent := key[:strings.LastIndex(key, ".")]
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
	// Days are what it saved each day it ran commands, oldest first
	Days []RTKDay `json:"days,omitempty"`
	// Latest is rtk's latest release, "" when it isn't known (see
	// CheckLatest); Upgrade the command Upgrade RTK runs, "" when magpie
	// doesn't know how this rtk was installed
	Latest  string `json:"latest,omitempty"`
	Upgrade string `json:"upgrade,omitempty"`
	// Note is what an upgrade left to say: Homebrew's rtk behind rtk's
	// own release
	Note    string     `json:"note,omitempty"`
	Agents  []RTKAgent `json:"agents"`
	URL     string     `json:"url"`
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
func rtkPath() string {
	if p, err := exec.LookPath("rtk"); err == nil {
		return p
	}
	name, dirs := "rtk", []string{filepath.Join(home(), ".local", "bin"), filepath.Join(home(), ".cargo", "bin")}
	if runtime.GOOS == "windows" {
		name, dirs = "rtk.exe", []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links"), filepath.Join(home(), ".cargo", "bin")}
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

// rtkAgents are the agents on this machine rtk can be given to.
func rtkAgents() []*agent.Agent {
	var out []*agent.Agent
	for _, a := range agent.Detected() {
		if _, ok := rtkSpecs[a.ID]; ok {
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
	for _, a := range rtkAgents() {
		sp := rtkSpecs[a.ID]
		ra := RTKAgent{ID: a.ID, Name: a.Name, Icon: a.Icon, On: sp.has(a)}
		if sp.blocked != nil {
			ra.Blocked = sp.blocked()
		}
		v.Agents = append(v.Agents, ra)
	}
	if v.Path == "" {
		return v
	}
	if out, err := rtkRun(v.Path, "--version"); err == nil {
		v.Version = strings.TrimSpace(strings.TrimPrefix(out, "rtk"))
	}
	if c := rtkUpgrader(v.Path); c != nil {
		v.Upgrade = shown(c)
	}
	// --daily adds the days to the summary (rtk 0.28 on)
	if out, err := rtkRun(v.Path, "gain", "--daily", "--format", "json"); err == nil {
		var g struct {
			Summary struct {
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
		if json.Unmarshal([]byte(out), &g) == nil && g.Summary.Commands > 0 {
			s := g.Summary
			v.Gain = &RTKGain{Commands: s.Commands, Input: s.Input, Saved: s.Saved, Pct: s.Pct}
			for _, d := range g.Daily {
				v.Days = append(v.Days, RTKDay{Date: d.Date, Commands: d.Commands, Input: d.Input, Saved: d.Saved, Pct: d.Pct})
			}
			slices.SortFunc(v.Days, func(a, b RTKDay) int { return strings.Compare(a.Date, b.Date) })
		}
	}
	return v
}

// shown is a command as the page shows it: a script's own line.
func shown(c []string) string {
	if c[0] == "sh" {
		return c[2]
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
	for _, x := range rtkAgents() {
		if x.ID == id {
			a = x
		}
	}
	sp, ok := rtkSpecs[id]
	switch {
	case !ok:
		return nil, fmt.Errorf("rtk has no hook for %s", id)
	case a == nil:
		return nil, fmt.Errorf("%s isn't installed", id)
	case sp.has(a) == on:
		return ReadRTK(), nil
	}
	if on && sp.blocked != nil {
		if why := sp.blocked(); why != "" {
			return nil, errors.New(why)
		}
	}
	bin := rtkPath()
	if on && bin == "" {
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
	sp := rtkSpecs[a.ID]
	if err := os.MkdirAll(sp.dir(a), 0o755); err != nil {
		return err
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

// rtkInstaller is how rtk is installed here: with Homebrew when there is
// one, winget on Windows, else rtk's own script; nil when there's no way.
func rtkInstaller() []string {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("winget"); err == nil {
			return []string{"winget", "install", "--id", "rtk-ai.rtk", "--exact", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
		}
		return nil
	}
	if _, err := exec.LookPath("brew"); err == nil {
		return []string{"brew", "install", "rtk"}
	}
	if _, err := exec.LookPath("curl"); err == nil {
		return []string{"sh", "-c", "curl -fsSL " + rtkScript + " | sh"}
	}
	return nil
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
	out, err := runInstaller(c, 5*time.Minute, "HOMEBREW_NO_AUTO_UPDATE=1")
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
	if _, ok := rtkSpecs[a.ID]; !ok {
		var ids []string
		for id := range rtkSpecs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return "", fmt.Errorf("rtk has no hook for %s (it has for %s)", a.Name, strings.Join(ids, ", "))
	}
	return a.ID, nil
}
