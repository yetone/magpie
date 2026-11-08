package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Desktop, WorkBuddy and OpenMausBot read what magpie writes for
// them as they start (微雨南山 and stones on Discord, 10-24: a provider
// added showed up there only after a restart; maxleebeta, 11-08: a model
// picked for OpenMausBot wasn't in its search). Claude Desktop asks the
// gateway for its models once, at start. WorkBuddy 5.6's app.asar watches models.json only
// when the file was there at start, and the watch is on the file itself,
// so a rewrite by rename leaves it watching the old one. While such an app
// runs on what it read before magpie's last change, its Agents row offers
// a Restart. magpie never restarts one unasked: the button asks first, in
// magpie's own dialog, and only then quits the app as its own Quit does
// and opens it again. The Codex app on macOS has its own Restart on its
// stale-copy line (codex_app.go), behind the same question.

// restartable is how one such app is found running: its macOS bundle (an
// id and the names it is installed under), and on Windows its executable's
// name with a test of a process's image path that says it is the app's.
type restartable struct {
	mac    desktopApp
	winExe string
	win    func(image string) bool
}

// claudeDesktopApp is Claude Desktop's bundle (Claude 2.7032's Info.plist:
// com.anthropic.claudefordesktop, executable Claude).
var claudeDesktopApp = desktopApp{id: "com.anthropic.claudefordesktop", names: []string{"Claude.app"}}

// restartApps are the agents whose desktop app the Agents row restarts.
// macOS: Claude.app, WorkBuddy.app and WorkBuddy AI.app (both run an
// executable named Electron) and OpenMausBot.app. Windows: Claude
// Desktop is claude.exe, from Squirrel's %LOCALAPPDATA%\AnthropicClaude
// (a claude.exe stub beside app-<version>\claude.exe) or its MSIX
// (desktopdir's Claude_… and AnthropicPBC.Claude_…), never Claude
// Code's claude.exe elsewhere;
// WorkBuddy is C:\Program Files\WorkBuddy\WorkBuddy.exe (5.6.2, seen on
// the Windows box). WorkBuddy AI's Windows build wasn't to hand, so it has
// no Restart there. Only OpenMausBot ships a Linux app; magpie doesn't
// restart it there.
var restartApps = map[string]restartable{
	"claude-desktop": {mac: claudeDesktopApp, winExe: "claude.exe", win: func(image string) bool {
		return winBase(image, "claude.exe") && (winHas(image, `\AnthropicClaude\`) || winHas(image, `\WindowsApps\Claude_`) || winHas(image, `\WindowsApps\AnthropicPBC.Claude_`))
	}},
	"workbuddy": {mac: workbuddyApp, winExe: "WorkBuddy.exe", win: func(image string) bool {
		return winBase(image, "WorkBuddy.exe") && !winHas(image, `WorkBuddy AI`) && !winHas(image, `WorkBuddy-AI`)
	}},
	"workbuddy-ai": {mac: workbuddyAIApp},
	// OpenMausBot reads its engines, magpie's models among them, once at
	// start (openmausbot.go); its Windows build is electron-builder's
	// per-user NSIS, OpenMausBot.exe under Programs\openmausbot
	"openmausbot": {mac: ombApp, winExe: "OpenMausBot.exe", win: func(image string) bool {
		return winBase(image, "OpenMausBot.exe")
	}},
}

func winBase(image, name string) bool {
	i := strings.LastIndexAny(image, `\/`)
	return strings.EqualFold(image[i+1:], name)
}

func winHas(image, part string) bool {
	return strings.Contains(strings.ToLower(image), strings.ToLower(part))
}

// restartOnWindows, restartOnMac and restartOnLinux say which system this
// is; vars so a test runs any.
var (
	restartOnWindows = runtime.GOOS == "windows"
	restartOnMac     = runtime.GOOS == "darwin"
	restartOnLinux   = runtime.GOOS == "linux"
)

// winProc is a process running on Windows: its id, its parent's, its
// image's full path, its command line ("" when it can't be read), when it
// started, and its AppUserModelID when it is a packaged (Store or MSIX)
// app's.
type winProc struct {
	pid, parent int
	image, cmd  string
	start       time.Time
	aumid       string
}

// runsScript says whether a process of an app's own executable exe runs a
// script on it as Node (ELECTRON_RUN_AS_NODE), which is no copy of the app
// itself: its command line names a .js, .cjs or .mjs file, or its first
// argument that is no flag is a file's absolute path. WorkBuddy 5.5 and
// 5.6 run their daemon so ("WorkBuddy.exe"
// --disable-warning=ExperimentalWarning "…\app.asar\main\
// daemon-app-server-entry.js" --stdio, seen on the Windows box), their
// sidecar (…/main/sidecar-entry.js --token …) and the CLI workers and
// prewarmed CLIs it keeps (…/cli/bin/codebuddy --prewarm …, a script with
// no extension). The sidecar is spawned detached, outlives the app's quit
// and is taken up again by the next start (ensureSidecar: "reconnected to
// existing sidecar"). Counted as the app, it kept the app's start at its
// own, so the row said to restart WorkBuddy after every real restart, and
// a Restart waited for it to end and never opened WorkBuddy again (Northes
// on Discord, 11-04). Electron's own processes name no script: the main
// one runs bare, or with flags, or a link it was opened with
// (workbuddy://…); its helpers run with flags (--type=renderer …) and, on
// Windows, /prefetch:N. exe may be "" when it isn't known; the command
// line's first word is taken for it then. A command line that can't be
// read ("") is the app's.
func runsScript(cmd, exe string) bool {
	cmd = strings.TrimSpace(cmd)
	for _, f := range strings.Fields(cmd) {
		f = strings.ToLower(strings.Trim(f, `"'`))
		if strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".cjs") || strings.HasSuffix(f, ".mjs") {
			return true
		}
	}
	// the arguments: the command line past the executable, which a Mac's
	// may show with spaces in its path, and Windows' in quotes
	rest := ""
	switch {
	case exe != "" && len(cmd) >= len(exe)+2 && strings.EqualFold(cmd[:len(exe)+2], `"`+exe+`"`):
		rest = cmd[len(exe)+2:]
	case exe != "" && len(cmd) >= len(exe) && strings.EqualFold(cmd[:len(exe)], exe):
		rest = cmd[len(exe):]
	case strings.HasPrefix(cmd, `"`):
		if i := strings.Index(cmd[1:], `"`); i >= 0 {
			rest = cmd[i+2:]
		}
	default:
		_, rest, _ = strings.Cut(cmd, " ")
	}
	for _, f := range strings.Fields(rest) {
		f = strings.Trim(f, `"'`)
		if strings.HasPrefix(f, "-") {
			continue
		}
		return absArg(f)
	}
	return false
}

// absArg says whether an argument is an absolute file path, on either
// system: /…, C:\… or C:/…, \\server\…; never Windows' /prefetch:N.
func absArg(f string) bool {
	switch {
	case strings.HasPrefix(f, "/prefetch:"):
		return false
	case strings.HasPrefix(f, "/"), strings.HasPrefix(f, `\\`):
		return true
	}
	return len(f) >= 3 && f[1] == ':' && (f[2] == '\\' || f[2] == '/') &&
		('a' <= f[0]|0x20 && f[0]|0x20 <= 'z')
}

// appProcs are those of ps, processes of the executable exe, that are the
// app's own, not a script run on it (runsScript).
func appProcs(ps []runningProc, exe string) []runningProc {
	var out []runningProc
	for _, p := range ps {
		if !runsScript(p.cmd, exe) {
			out = append(out, p)
		}
	}
	return out
}

// The Windows part of a restart, vars so tests stand in for each:
// winProcs lists the running processes named exe, winClose asks pids to close as
// their windows' close does, winShown says whether any of pids still shows
// a window (true when it can't tell), winEnd ends pid with its tree, and
// winLaunch opens what a restart opens again.
var (
	winProcs  = listWinProcs
	winClose  = closeWinProcs
	winShown  = winWindowsShown
	winEnd    = endWinTree
	winLaunch = launchWin
	// appCloseWait is how long a Windows app that still shows a window
	// has to close by itself before it is ended, and appBackWait how long
	// it has to come back. appTrayGrace is how long one whose windows have
	// all closed has to end by itself before it counts as staying in the
	// tray.
	appCloseWait = 10 * time.Second
	appBackWait  = 20 * time.Second
	appTrayGrace = time.Second
	appPoll      = 200 * time.Millisecond
)

// appRunning is r's app as it runs here: when it started, and on macOS its
// bundle and the bundle's main executable, on Windows its processes and
// the one the rest hang off.
type appRunning struct {
	since  time.Time
	bundle string
	exe    string
	procs  []winProc
	main   winProc
}

// findRestartable is r's app running here, if it is.
func findRestartable(r restartable) (appRunning, bool) {
	if restartOnWindows {
		if r.win == nil {
			return appRunning{}, false
		}
		var ps []winProc
		for _, p := range winProcs(r.winExe) {
			if r.win(p.image) && !runsScript(p.cmd, p.image) {
				ps = append(ps, p)
			}
		}
		if len(ps) == 0 {
			return appRunning{}, false
		}
		mine := map[int]bool{}
		for _, p := range ps {
			mine[p.pid] = true
		}
		// the main process is the one whose parent isn't the app's:
		// Electron's helpers hang off it
		c := appRunning{procs: ps, main: ps[0], since: ps[0].start}
		top := false
		for _, p := range ps {
			if p.start.Before(c.since) {
				c.since = p.start
			}
			if !mine[p.parent] && (!top || p.start.Before(c.main.start)) {
				c.main, top = p, true
			}
		}
		return c, true
	}
	if !restartOnMac || r.mac.id == "" {
		return appRunning{}, false
	}
	for _, b := range macBundles(r.mac) {
		exe := bundleExecutable(b, r.mac.id)
		if exe == "" {
			continue
		}
		main := filepath.Join(b, "Contents", "MacOS", exe)
		ps := appProcs(running("^"+regexp.QuoteMeta(main)+"( |$)"), main)
		if len(ps) == 0 {
			continue
		}
		up := ps[0].up
		for _, p := range ps[1:] {
			up = max(up, p.up)
		}
		return appRunning{since: time.Now().Add(-up), bundle: b, exe: main}, true
	}
	return appRunning{}, false
}

// bundleExecutable is the CFBundleExecutable of the bundle at b when its
// Info.plist says it is id's, "" otherwise.
func bundleExecutable(b, id string) string {
	info, err := os.ReadFile(filepath.Join(b, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	got := plistIDRe.FindSubmatch(info)
	exe := plistExeRe.FindSubmatch(info)
	if got == nil || exe == nil || strings.TrimSpace(string(got[1])) != id {
		return ""
	}
	return strings.TrimSpace(string(exe[1]))
}

// AppRun is a desktop app running on what it read before magpie's last
// change to it: when it started, for the row to offer a Restart.
type AppRun struct {
	Since time.Time `json:"since"`
}

// AppBehind is the agent's desktop app when it runs here and started
// before magpie last changed what it reads at start; nil when it isn't
// running, has started since, or magpie doesn't restart it here. Only for
// an agent connected to magpie, or one disconnected from it in magpie:
// those are the changes magpie made.
func (a *Agent) AppBehind() *AppRun {
	r, ok := restartApps[a.ID]
	if !ok || a.WSL != "" {
		return nil
	}
	if !a.Wired() && !a.Disconnected() {
		return nil
	}
	files, reads := a.appReads()
	if len(files) == 0 {
		return nil
	}
	c, ok := findRestartable(r)
	if !ok {
		return nil
	}
	changed := a.changedAt(files, reads)
	if changed.IsZero() || !c.since.Before(changed.Add(-time.Second)) {
		return nil
	}
	return &AppRun{Since: c.since}
}

// appReads are the files the agent's desktop app reads magpie's part from
// at start, and that part. Claude Desktop: deploymentMode, the profile
// applied and magpie's profile (or the user's for magpie's gateway), with the models the gateway lists it.
// WorkBuddy: magpie's models in its models.json (workbuddyPart).
// OpenMausBot: magpie's engine in its config.json (ombPart).
func (a *Agent) appReads() (files []string, reads func() string) {
	switch a.ID {
	case "claude-desktop":
		ps := desktopSetsHere()
		for _, p := range ps {
			files = append(files, p.config, p.config3p, p.meta, p.prof)
			if f := desktopAppliedGateway(p); f != "" {
				files = append(files, f)
			}
		}
		return files, func() string {
			var b strings.Builder
			for _, p := range ps {
				id, _ := edit.GetJSON(p.meta, "appliedId")
				applied := p.prof
				if f := desktopAppliedGateway(p); f != "" {
					applied = f
				}
				// the mode Desktop starts in, not the files' words: it
				// drops a stored deploymentMode itself (desktop3p)
				mode := "1p"
				if desktop3p(p, applied) {
					mode = "3p"
				}
				prof, _ := edit.Read(p.prof)
				b.WriteString(mode + "\n" + id + "\n")
				b.Write(prof)
				b.WriteString("\n")
				// a profile of the user's for magpie's gateway, applied
				// instead of magpie's (desktopKeyApplied)
				if f := desktopAppliedGateway(p); f != "" {
					theirs, _ := edit.Read(f)
					b.Write(theirs)
					b.WriteString("\n")
				}
			}
			shown, _ := provider.CatalogFor(a.ListsFor())
			for _, e := range shown {
				b.WriteString(e.ID + "\n")
			}
			return b.String()
		}
	case "workbuddy", "workbuddy-ai":
		return []string{a.Path}, func() string { return workbuddyPart(a.Path) }
	case "openmausbot":
		return []string{a.Path}, func() string { return ombPart(a.Path) }
	}
	return nil, nil
}

// desktopSetsHere are Claude Desktop's folders on this computer.
var desktopSetsHere = func() []desktopPaths {
	home, _ := os.UserHomeDir()
	return desktopSets(desktopdir.OS, home, os.Getenv)
}

// restarting holds one restart per agent at a time, so a second click
// waits for the first instead of quitting the app it just opened.
var restarting sync.Map

// What a Restart that didn't go through says, in the reader's language
// (tNotice): an app not on the old settings any more was quit or
// restarted meanwhile.
var (
	noticeRestartNotHere    = newNotice("magpie doesn't restart {agent} on this computer.")
	noticeRestartCurrent    = newNotice("{agent} isn't running on the old settings any more.")
	noticeRestartAsk        = newNotice("magpie couldn't ask {agent} to quit. Quit it yourself, then open it again.")
	noticeRestartStillOpen  = newNotice("{agent} is still open; it may be asking something in its window. Answer it there, or quit it, then open it again.")
	noticeRestartStillThere = newNotice("{agent} is still running. Quit it, then open it again.")
	noticeRestartNoOpen     = newNotice("{agent} quit, but didn't open again. Open it yourself.")
)

func restartErr(n notice, a *Agent) error { return errors.New(n.say("agent", a.Name)) }

// RestartApp quits the agent's desktop app and opens it again, so it reads
// what magpie wrote. Only the Agents row's Restart calls it, after the
// user confirmed, and only an app AppBehind lists is quit. macOS: asked to
// quit as ⌘Q does, every process of its bundle waited for (but a script
// run on it, runsScript); one still open
// then (it may be asking something) is left, never forced, and not opened
// a second time. Windows: its windows are asked to close; an app still
// running then (Electron apps stay in the tray) is ended with its tree.
func (a *Agent) RestartApp(ctx context.Context) error {
	r, ok := restartApps[a.ID]
	if !ok || a.WSL != "" {
		return restartErr(noticeRestartNotHere, a)
	}
	mu, _ := restarting.LoadOrStore(a.ID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if a.AppBehind() == nil {
		return restartErr(noticeRestartCurrent, a)
	}
	c, ok := findRestartable(r)
	if !ok {
		return restartErr(noticeRestartCurrent, a)
	}
	if c.bundle != "" {
		return reopenBundle(ctx, a, c.bundle, c.exe)
	}
	return a.restartWin(ctx, r, c)
}

// restartWin asks c's windows to close, ends it when it stays (in
// the tray, or past appCloseWait), opens it again and waits for it.
func (a *Agent) restartWin(ctx context.Context, r restartable, c appRunning) error {
	pids := make([]int, len(c.procs))
	for i, p := range c.procs {
		pids[i] = p.pid
	}
	_ = winClose(pids)
	if !waitClosed(ctx, r, appCloseWait) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := winEnd(c.main.pid); err != nil || !waitGone(ctx, r, appCloseWait) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return restartErr(noticeRestartStillThere, a)
		}
	}
	if err := winLaunch(c.main); err != nil {
		return restartErr(noticeRestartNoOpen, a)
	}
	end := time.Now().Add(appBackWait)
	for {
		if _, ok := findRestartable(r); ok {
			return nil
		}
		if !time.Now().Before(end) {
			return restartErr(noticeRestartNoOpen, a)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(appPoll):
		}
	}
}

// waitClosed waits, after its windows were asked to close, until r's app
// no longer runs: true when it ended by itself. An app that closed every
// window and still runs after appTrayGrace has gone to the tray, which no
// waiting ends (Claude Desktop 2.9939 on Windows hides its window in ~0.6s
// and keeps all nine processes), so it is false then, not after limit;
// limit is for an app still showing a window, which may be asking
// something.
func waitClosed(ctx context.Context, r restartable, limit time.Duration) bool {
	start := time.Now()
	end := start.Add(limit)
	for {
		c, ok := findRestartable(r)
		if !ok {
			return true
		}
		now := time.Now()
		if !now.Before(end) {
			return false
		}
		if now.Sub(start) >= appTrayGrace {
			pids := make([]int, len(c.procs))
			for i, p := range c.procs {
				pids[i] = p.pid
			}
			if !winShown(pids) {
				return false
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(appPoll):
		}
	}
}

// waitGone waits until r's app no longer runs, for at most limit.
func waitGone(ctx context.Context, r restartable, limit time.Duration) bool {
	end := time.Now().Add(limit)
	for {
		if _, ok := findRestartable(r); !ok {
			return true
		}
		if !time.Now().Before(end) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(appPoll):
		}
	}
}

// reopenBundle quits the app at bundle, as ⌘Q does, waits for every
// process under it to end, but for a script run on its executable exe,
// which may outlive it by design (runsScript), and opens it again
// (codex_app.go's way).
func reopenBundle(ctx context.Context, a *Agent, bundle, exe string) error {
	if err := appQuit(ctx, bundle); err != nil {
		return restartErr(noticeRestartAsk, a)
	}
	pat := regexp.QuoteMeta(bundle + "/Contents/")
	end := time.Now().Add(appQuitWait)
	for len(appProcs(running(pat), exe)) > 0 {
		if time.Now().After(end) {
			return restartErr(noticeRestartStillOpen, a)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(appPoll):
		}
	}
	if err := appOpen(ctx, bundle); err != nil {
		return restartErr(noticeRestartNoOpen, a)
	}
	return nil
}
