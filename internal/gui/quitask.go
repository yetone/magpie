package gui

import (
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/settings"
)

// Quitting magpie ends the gateway, and every agent connected to it fails
// until magpie is opened again, with nothing to say why: Codex retried
// "unexpected status 502 Bad Gateway: Unknown error" five times and gave
// up, and its user found magpie's line in ~/.codex/config.toml only after
// a long search (Aisiverto on X). So a Quit the user chooses (the tray's
// menu, the panel's Quit, Command-Q on the Mac) while agents are connected
// opens the window on a question first, naming them (quitask.js): Quit,
// Disconnect and quit (each one's own settings put back, as its row's
// Disconnect does), or Cancel, with "Don't ask again" (zhujiu on Discord),
// which Settings › General › Ask before quitting turns back on
// (settings.QuitNoAsk). A quit magpie makes itself (a restart to
// update, Uninstall) and the system's own (logging out, shutting down,
// the Dock's Quit) don't ask.

// quitAgentJSON is an agent a quit would cut off, for the question.
type quitAgentJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// quitAgents lists the agents connected to the gateway this process
// serves: none when another magpie serves it, which goes on serving them.
// A var so tests can say.
var quitAgents = func() []quitAgentJSON {
	if served.Load() == nil {
		return nil
	}
	return wiredAgents(agent.Detected())
}

// wiredAgents is the agents of as with magpie in their config.
func wiredAgents(as []*agent.Agent) []quitAgentJSON {
	out := []quitAgentJSON{}
	for _, a := range as {
		if a.Wired() {
			out = append(out, quitAgentJSON{ID: a.ID, Name: a.Name, Icon: a.Icon})
		}
	}
	return out
}

// quitAsker is a host that asks before a quit the user chose (the app's);
// a browser's page (magpie web) quits at once.
type quitAsker interface {
	// AskQuit quits, or first asks in the window when agents are connected
	AskQuit()
}

// quitAgain is how long after the question is put a second Quit quits
// without it.
const quitAgain = time.Minute

// quitNow says whether a Quit chosen at now quits without asking: with no
// agent connected (wired), or within quitAgain of the question put at
// asked (unix nanoseconds; 0 when it never was).
func quitNow(asked int64, now time.Time, wired int) bool {
	if wired == 0 {
		return true
	}
	return asked != 0 && now.Sub(time.Unix(0, asked)) < quitAgain
}

// quitAtOnce says whether a Quit chosen at now quits without asking: the
// user said not to ask (settings.QuitNoAsk, the question's "Don't ask
// again"), or quitNow. A settings file that can't be read asks.
func quitAtOnce(asked int64, now time.Time) bool {
	if s, err := settings.LoadChecked(); err == nil && s.QuitNoAsk {
		return true
	}
	return quitNow(asked, now, len(quitAgents()))
}
