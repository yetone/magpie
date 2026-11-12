package agent

import (
	"context"
	"errors"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// The Codex app on macOS keeps running, its app-server with it, when its
// window is closed, so a change to Codex's model list reaches it only once
// its app-server starts again. The Agents row's Restart does that for the
// user (the owner, 10-23): it ends the app's own app-server, and the app
// starts another and keeps its window. Quitting the app and opening it
// again, as it did before, opened a new window (yoshi037070, 10-27).

// appQuit and appOpen ask macOS to quit a bundle and to open it; vars so
// tests don't touch a real app.
var (
	appQuit = func(ctx context.Context, bundle string) error {
		out, err := proc.CommandContext(ctx, "osascript", "-e", `tell application "`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(bundle)+`" to quit`).CombinedOutput()
		return cmdErr(out, err)
	}
	appOpen = func(ctx context.Context, bundle string) error {
		out, err := proc.CommandContext(ctx, "open", bundle).CombinedOutput()
		return cmdErr(out, err)
	}
)

// appQuitWait is how long a quit app's processes have to end.
var appQuitWait = 30 * time.Second

func cmdErr(out []byte, err error) error {
	if err != nil && len(strings.TrimSpace(string(out))) > 0 {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return err
}

// restartAppServer ends the app-server the app at a bundle runs for
// itself; a var so tests don't end a real one.
var restartAppServer = provider.RestartCodexApp

// ReopenCodexApp restarts the app-server of the Codex desktop app at
// bundle; the app starts a new one and its window stays. Only an app the
// Agents row lists as left on the old list is touched, never a path a
// request names otherwise.
func ReopenCodexApp(ctx context.Context, bundle string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("only the macOS Codex app is restarted by magpie")
	}
	a, err := Find("codex")
	if err != nil {
		return err
	}
	return reopenApp(ctx, a, bundle)
}

func reopenApp(ctx context.Context, a *Agent, bundle string) error {
	if bundle == "" || !slices.ContainsFunc(a.StaleCopies(), func(c StaleCopy) bool { return c.Kind == "app" && c.Bundle == bundle }) {
		return errors.New("that Codex app has the current list already")
	}
	return restartAppServer(ctx, bundle)
}

// What the Restart on Codex's "the Codex app is still signed in as" notice
// says when it didn't go through (tNotice).
var (
	noticeSwitchAppGone    = newNotice("The {agent} app isn't running any more. It opens on the new account.")
	noticeSwitchAppNoEnd   = newNotice("magpie couldn't restart the {agent} app's app-server. Quit the app and open it again.")
	noticeSwitchAppNotBack = newNotice("The {agent} app's app-server ended, but the app didn't start another. Quit the app and open it again.")
	noticeSwitchAppUnknown = newNotice("magpie couldn't tell whether the {agent} app restarted. Quit the app and open it again.")
)

// restartSwitchedApp restarts the Codex app's app-server; a var so tests
// don't end a real one.
var restartSwitchedApp = provider.RestartSwitchedCodexApp

// RestartSwitchedCodexApp restarts the Codex desktop app's app-server
// after Codex's account was switched, so the app is on the new account
// (provider.RestartSwitchedCodexApp), and says in the reader's language
// what went wrong when it didn't.
func RestartSwitchedCodexApp(ctx context.Context) error {
	err := restartSwitchedApp(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, provider.ErrCodexAppGone):
		return errors.New(noticeSwitchAppGone.say("agent", "Codex"))
	case errors.Is(err, provider.ErrCodexAppNoEnd):
		return errors.New(noticeSwitchAppNoEnd.say("agent", "Codex"))
	case errors.Is(err, provider.ErrCodexAppNotBack):
		return errors.New(noticeSwitchAppNotBack.say("agent", "Codex"))
	}
	return errors.New(noticeSwitchAppUnknown.say("agent", "Codex"))
}

// codexWinApp is the Windows Codex app: the OpenAI.Codex Store package's
// ChatGPT.exe (26.1021 on the Windows box, under C:\Program Files\
// WindowsApps\OpenAI.Codex_…\app\), never the ChatGPT chat app's.
var codexWinApp = restartable{winExe: "ChatGPT.exe", win: func(image string) bool {
	return winBase(image, "ChatGPT.exe") && winHas(image, `\WindowsApps\OpenAI.Codex_`)
}}

// codexWinAUMID opens the Windows Codex app when its main process's
// AppUserModelID can't be read: its package family and its one app.
const codexWinAUMID = "OpenAI.Codex_2p2nqsd0c76g0!App"

// codexAppOpen is provider.CodexAppOpen: whether the Codex desktop app
// runs here, ChatGPT.app (com.openai.codex) on macOS, the Store app on
// Windows, OpenAI's package's /usr/lib/chatgpt/ChatGPT on Linux (any of
// its processes: Electron's helpers run the same executable); not known
// elsewhere.
func codexAppOpen(context.Context) (open, known bool) {
	switch {
	case restartOnLinux:
		app := desktopApps["codex"]
		return len(running("^"+regexp.QuoteMeta(app.linux+"/ChatGPT")+"( |$)")) > 0, true
	case restartOnWindows:
		_, ok := findRestartable(codexWinApp)
		return ok, true
	case restartOnMac:
		_, ok := findRestartable(restartable{mac: desktopApps["codex"]})
		return ok, true
	}
	return false, false
}

// codexAppReopen is provider.CodexAppReopen: on Windows it quits the
// Codex app and opens it again, as the Agents row's Restart does for
// Claude Desktop (restartWin), when the app started no app-server in
// place of the one magpie ended and so can't send. Its threads are kept
// by Codex; a draft not sent is lost, but nothing could be sent from it.
// macOS isn't reopened unasked: there the Agents row's Restart and the
// line magpie shows are the user's.
func codexAppReopen(ctx context.Context) error {
	if !restartOnWindows {
		return errors.New("magpie reopens only the Windows Codex app")
	}
	c, ok := findRestartable(codexWinApp)
	if !ok {
		return nil // quit meanwhile: it starts an app-server as it opens
	}
	if c.main.aumid == "" {
		c.main.aumid = codexWinAUMID
	}
	a, err := Find("codex")
	if err != nil {
		a = &Agent{ID: "codex", Name: "Codex"}
	}
	return a.restartWin(ctx, codexWinApp, c)
}

// CodexAppInstalled says whether the Codex desktop app is installed here
// and magpie can open it: ChatGPT.app or Codex.app on macOS, the Store
// app on Windows. Linux's package has no launcher magpie starts.
func CodexAppInstalled() bool {
	if !restartOnMac && !restartOnWindows {
		return false
	}
	return (&Agent{ID: "codex"}).AppVersion() != ""
}

// OpenCodexApp opens the Codex desktop app, which brings its window up
// when it is running already (Yink88564391 on X). On macOS it opens the
// bundle whose Info.plist says com.openai.codex; on Windows it starts the
// Store package's app by its AppUserModelID, as a restart does.
func OpenCodexApp(ctx context.Context) error {
	switch {
	case restartOnMac:
		app := desktopApps["codex"]
		for _, bundle := range macBundles(app) {
			if info, ok := bundleInfo(bundle); ok && info.hasID && info.id == app.id {
				return appOpen(ctx, bundle)
			}
		}
	case restartOnWindows:
		if CodexAppInstalled() {
			return winLaunch(winProc{aumid: codexWinAUMID})
		}
	}
	return errors.New("the Codex app is not installed here")
}
