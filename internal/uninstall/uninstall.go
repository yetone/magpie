// Package uninstall takes magpie off this computer: `magpie uninstall` and
// Settings › Uninstall magpie.
//
// Jevik_AI on X deleted magpie's folders by hand, and Codex's config.toml
// kept openai_base_url on 127.0.0.1:3425: every Codex turn failed with a
// 502, with nothing left that knew why. Deleting magpie's files never puts
// an agent back, so uninstalling does that first, the way each agent's
// Disconnect does (what magpie changed goes back, what the user changed
// by hand since stays theirs), then removes what magpie registered with
// the system, then its own files.
//
// What it leaves: the magpie executable itself (a running program can't
// delete itself everywhere, and the user put it there), and the Library's
// folder while it holds skills or servers the user's agents use.
package uninstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/autostart"
	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/daemon"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/pii"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/shortcut"
	"github.com/yetone/magpie/internal/update"
)

// Done is one thing uninstalling did, or couldn't.
type Done struct {
	What string `json:"what"`
	Err  string `json:"error,omitempty"`
}

// Report is what Run did.
type Report struct {
	Agents []Done `json:"agents"`
	System []Done `json:"system"`
	Data   []Done `json:"data"`
	// Kept are folders left on purpose, each with why
	Kept []Done `json:"kept,omitempty"`
	// CodexApp: a Codex app still on the config it read before, which
	// the user quits and opens
	CodexApp bool `json:"codexApp,omitempty"`
	// Exe is the magpie executable, left where it is
	Exe string `json:"exe"`
}

// Failed says whether anything couldn't be done.
func (r *Report) Failed() bool {
	for _, l := range [][]Done{r.Agents, r.System, r.Data} {
		for _, d := range l {
			if d.Err != "" {
				return true
			}
		}
	}
	return false
}

// what Run asks of the system; the tests put fakes in
var (
	agents          = agent.All
	endFailover     = agent.EndCodexFailover
	endLeftovers    = agent.TakeCodexLeftovers
	portRollouts    = agent.PortCodexRollouts
	restartCodexApp = provider.RestartCodexAppWhenIdle
	removeService   = func() error {
		err := daemon.Uninstall()
		if errors.Is(err, daemon.ErrUnsupported) {
			return nil
		}
		return err
	}
	autostartOff   = func() error { return autostart.Set(false) }
	removeShortcut = shortcut.Remove
	removeScheme   = unregisterScheme
	endPII         = pii.Remove
	forgetInstall  = update.ForgetInstallDir
	executable     = os.Executable
	wired          = (*agent.Agent).Wired
	disconnectOne  = disconnect
)

// codexAppWait is how long Run waits for a running Codex turn to end
// before it restarts the Codex app's app-server.
var codexAppWait = 30 * time.Second

// Connected are the agents magpie is in now, which Run disconnects.
func Connected() []*agent.Agent {
	var out []*agent.Agent
	for _, a := range agents() {
		if wired(a) {
			out = append(out, a)
		}
	}
	return out
}

// Agents disconnects every agent magpie is in: the first half of
// uninstalling, which the app does while it still runs.
func Agents(ctx context.Context, r *Report) {
	// failover first: it is a provider in Codex's config too
	if _, err := endFailover(); err != nil {
		r.Agents = append(r.Agents, Done{What: "Codex account failover", Err: err.Error()})
	}
	codex := false
	for _, a := range Connected() {
		err := disconnectOne(a)
		d := Done{What: a.Name}
		if err != nil {
			d.Err = err.Error()
		}
		r.Agents = append(r.Agents, d)
		codex = codex || a.ID == "codex"
	}
	// what no Disconnect could name: an agent whose config points at
	// magpie's gateway with the marks magpie kept gone (its folder deleted
	// by hand, a fresh install beside an old config) reads as not
	// connected, and Codex left on that address knocks on a port nothing
	// answers any more (#3866)
	took, err := endLeftovers()
	if took || err != nil {
		d := Done{What: "Codex's leftover settings"}
		if err != nil {
			d.Err = err.Error()
		}
		r.Agents = append(r.Agents, d)
		codex = true
	}
	// the threads Codex ran on magpie's models open without magpie
	portRollouts(10 * time.Second)
	if codex {
		// the Codex app read the old config as its app-server started
		if _, left, err := restartCodexApp(ctx, codexAppWait); left || err != nil {
			r.CodexApp = true
		}
	}
}

// disconnect is the Agents page's Disconnect, and for an app whose
// settings can't be read while it is closed, its offline disconnect.
func disconnect(a *agent.Agent) error {
	err := a.Disconnect()
	var off *agent.RuntimeUnavailableError
	if errors.As(err, &off) && off.Offline == agent.OfflineDisconnect && a.Native != nil && a.Native.ExecuteOffline != nil {
		plan, perr := a.Native.Disconnect()
		if perr != nil {
			return perr
		}
		return a.Native.ExecuteOffline(plan)
	}
	return err
}

// System removes what magpie registered with the system: its background
// service, opening at login, its Start menu shortcut and links.
func System(r *Report) {
	exe, _ := executable()
	for _, s := range []struct {
		what string
		do   func() error
	}{
		{"background service", removeService},
		{"open at login", autostartOff},
		{"Start menu shortcut", func() error { return removeShortcut(exe) }},
		{"magpie:// links", func() error { return removeScheme(exe) }},
		{"privacy model's helper", endPII},
		// Windows: the folder chosen to install in, so a download opened
		// after this asks again
		{"install folder record", forgetInstall},
	} {
		d := Done{What: s.what}
		if err := s.do(); err != nil {
			d.Err = err.Error()
		}
		r.System = append(r.System, d)
	}
}

// Folders are magpie's own folders Data removes, and the Library's folder
// when it keeps that.
func Folders() (remove []string, keep string) {
	home, _ := os.UserHomeDir()
	add := func(p string) {
		if removable(p, home) && !slices.Contains(remove, p) {
			remove = append(remove, p)
		}
	}
	cfg := appdir.Config()
	if libraryInUse() {
		keep = library.Dir()
	}
	add(cfg)
	add(appdir.Cache())
	if c, err := appdir.SystemCache(); err == nil {
		add(c)
	}
	add(appdir.WebView())
	add(claudecode.Root())
	for _, p := range webData() {
		add(p)
	}
	// a folder inside another is removed with it
	var out []string
	for _, p := range remove {
		if !slices.ContainsFunc(remove, func(q string) bool { return q != p && within(p, q) }) {
			out = append(out, p)
		}
	}
	return out, keep
}

// libraryInUse says whether the Library holds skills, servers or hooks:
// an agent given one reads it from there, or has it from there, and it
// is the user's, so it stays.
func libraryInUse() bool {
	es, err := os.ReadDir(library.Dir())
	if err != nil {
		return false
	}
	for _, e := range es {
		if e.IsDir() {
			if sub, _ := os.ReadDir(filepath.Join(library.Dir(), e.Name())); len(sub) > 0 {
				return true
			}
		}
	}
	return false
}

// Data removes magpie's own files: settings, providers, sign-ins kept by
// magpie, usage, caches. With the Library in use, everything in magpie's
// folder but it goes.
func Data(r *Report) {
	remove, keep := Folders()
	for _, p := range remove {
		d := Done{What: p}
		var err error
		if keep != "" && within(keep, p) {
			err = removeAllBut(p, keep)
		} else {
			err = os.RemoveAll(p)
		}
		if err != nil {
			d.Err = err.Error()
		}
		r.Data = append(r.Data, d)
	}
	if keep != "" {
		r.Kept = append(r.Kept, Done{What: keep, Err: "skills and servers your agents use from magpie's Library"})
	}
}

// removeAllBut removes dir but keep, which is inside it.
func removeAllBut(dir, keep string) error {
	es, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range es {
		p := filepath.Join(dir, e.Name())
		switch {
		case samePath(p, keep):
		case within(keep, p):
			errs = append(errs, removeAllBut(p, keep))
		default:
			errs = append(errs, os.RemoveAll(p))
		}
	}
	return errors.Join(errs...)
}

// Run uninstalls magpie: agents, then the system, then its files. The
// app runs Data itself once it has quit (WebView2 holds its folder open
// while it runs), so it calls Agents and System only.
func Run(ctx context.Context) *Report {
	r := &Report{}
	// the service first: one still serving would put magpie back
	System(r)
	Agents(ctx, r)
	Data(r)
	r.Exe, _ = executable()
	return r
}

// KeepData is Run without Data: every agent put back as it was and
// magpie's service, login item and shortcuts removed, with its settings,
// providers and usage kept for a magpie installed again. install.sh
// --uninstall runs it, so an agent never stays pointed at a gateway the
// script took away (whimsical_wolf_15477 on Discord: Codex after
// uninstalling).
func KeepData(ctx context.Context) *Report {
	r := &Report{}
	System(r)
	Agents(ctx, r)
	r.Exe, _ = executable()
	return r
}

// AfterExit waits for process pid to end, for up to wait, then removes
// magpie's files: what the app starts as it quits.
func AfterExit(pid int, wait time.Duration) *Report {
	until := time.Now().Add(wait)
	for update.Alive(pid) && time.Now().Before(until) {
		time.Sleep(200 * time.Millisecond)
	}
	r := &Report{}
	Data(r)
	return r
}

// startAfter starts cmd outside this process, which it outlives; the
// tests put a fake in
var startAfter = func(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	return proc.StartReaped(cmd)
}

// DataAfterExit starts `magpie uninstall --data-after <this pid>`, which
// removes magpie's files once this process, the app, has quit: the app's
// half of Run after Agents and System.
func DataAfterExit() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	return startAfter(proc.Command(exe, "uninstall", "--data-after", strconv.Itoa(os.Getpid())))
}

// removable says whether p can be one of magpie's folders: never one
// that holds more than magpie's, as the home, a root or a relative path
// would, from a variable set to one.
func removable(p, home string) bool {
	return p != "" && filepath.IsAbs(p) && filepath.Dir(p) != p && (home == "" || !within(home, p))
}

// within says whether p is dir or inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

// Says is the report as lines, for the terminal and the app's log.
func (r *Report) Says() []string {
	var out []string
	line := func(kind string, d Done) {
		if d.Err != "" {
			out = append(out, fmt.Sprintf("! %s %s: %s", kind, d.What, d.Err))
		} else {
			out = append(out, fmt.Sprintf("✓ %s %s", kind, d.What))
		}
	}
	for _, d := range r.Agents {
		line("disconnected", d)
	}
	for _, d := range r.System {
		line("removed", d)
	}
	for _, d := range r.Data {
		line("deleted", d)
	}
	for _, d := range r.Kept {
		out = append(out, "· kept "+d.What+": "+d.Err)
	}
	return out
}
