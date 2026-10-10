package agent

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's background app-server (the daemon codex sessions attach to)
// builds its model list once, as it starts, and runs on per CODEX_HOME,
// across terminals, with no idle exit. So a change magpie makes to what
// Codex reads at start — wiring it in, taking it out, a new catalog —
// reaches no codex session started after it until the daemon restarts:
// luci (Discord, Codex 0.162) took Codex off magpie, opened codex, which
// started the daemon on Codex's own models, and after wiring Codex in
// again every new codex showed the official list. With no codex session
// attached magpie restarts it itself, right after the change and every
// so often after (a session open then is one the user closes later);
// with one attached the Agents page offers the restart.

// codexRestartAll is what is said after a change while copies of Codex
// run that magpie leaves to the user.
var codexRestartAll = newNotice("Codex builds its model list at start-up — restart the Codex app, open codex sessions and the app-server they share ({command}) to see this.").say("command", provider.CodexDaemonRestart)

// keepCodexDaemon is provider.KeepCodexDaemonCurrent; a var so tests can
// see what is asked of it without a codex.
var keepCodexDaemon = provider.KeepCodexDaemonCurrent

// codexDaemonSeen is the last check of this machine's daemon, for the
// Agents row where its processes aren't listed on each look (Windows,
// where listing them is a PowerShell run).
var codexDaemonSeen struct {
	sync.Mutex
	at    time.Time
	check provider.CodexDaemonCheck
}

// codexDaemonCheck checks the daemon of the Codex whose config is path
// against when magpie last changed what Codex reads at start, and
// restarts it when no codex session is on it.
func codexDaemonCheck(path string) provider.CodexDaemonCheck {
	a := &Agent{ID: "codex", Path: path}
	_, files, reads := a.startsWith()
	changed := a.changedAt(files, reads)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := keepCodexDaemon(ctx, filepath.Dir(path), changed)
	if c.Err != nil {
		log.Println("codex app-server:", c.Err)
	}
	codexDaemonSeen.Lock()
	codexDaemonSeen.at, codexDaemonSeen.check = time.Now(), c
	codexDaemonSeen.Unlock()
	return c
}

// codexDaemonNotice is what is said of the daemon after a change: ok is
// false when there is none behind it.
func codexDaemonNotice(path string) (string, bool) {
	c := codexDaemonCheck(path)
	switch {
	case c.Restarted:
		return noticeCodexdaemon.String(), true
	case c.Behind && c.Attached > 0:
		return noticeCodexBehind.say("command", provider.CodexDaemonRestart), true
	}
	return "", false
}

// codexDaemonEvery is how often KeepCodexDaemonCurrent looks.
const codexDaemonEvery = 15 * time.Second

// KeepCodexDaemonCurrent restarts this machine's Codex daemon whenever it
// is behind what Codex reads at start and no codex session is on it,
// looking every so often until ctx ends.
func KeepCodexDaemonCurrent(ctx context.Context) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(here(home).codexHome(), "config.toml")
	t := time.NewTicker(codexDaemonEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := os.Stat(path); err == nil {
			if c := codexDaemonCheck(path); c.Restarted {
				log.Println("codex app-server: restarted, it had the model list from before", c.Since.Format(time.RFC3339))
			}
		}
	}
}

// CodexDaemonRestarted forgets the daemon last found behind, once the user
// has restarted it.
func CodexDaemonRestarted() {
	codexDaemonSeen.Lock()
	codexDaemonSeen.check = provider.CodexDaemonCheck{}
	codexDaemonSeen.Unlock()
}

// codexDaemonBehind is the daemon as last checked, while that check is
// recent: a copy left on the old list, for StaleCopies where processes
// aren't listed on each look.
func codexDaemonBehind() []StaleCopy {
	codexDaemonSeen.Lock()
	defer codexDaemonSeen.Unlock()
	c := codexDaemonSeen.check
	if !c.Behind || c.Restarted || time.Since(codexDaemonSeen.at) > 3*codexDaemonEvery {
		return nil
	}
	return []StaleCopy{{Kind: "daemon", Since: c.Since}}
}

// what codexdaemon says after a change (notice.go)
var (
	noticeCodexBehind = newNotice("Codex builds its model list at start-up, and the app-server open codex sessions share started before this: restart it (Restart on magpie's Agents page, or {command}) and the Codex app to see this.")
	noticeCodexdaemon = newNotice("magpie restarted the app-server codex sessions share, which none was on, so new codex sessions have this; restart the Codex app, if it is open, to see it there.")
)
