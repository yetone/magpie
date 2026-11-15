package gui

import (
	"log"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/settings"
)

// Settings › General › Put agents back when quitting (tudjam17795 on X:
// after quitting, the harness it was connected to can't go on). Off by
// default, a connected agent keeps magpie as it quits. On, the app's quit
// does what each agent's Disconnect does (agent.RestoreOnQuit: their own
// files back as they were, no Quit question), and the next start connects
// them again (agent.RewireOnStart). Only the agents connected at the quit,
// and not disconnected by the user, are touched.

// restoreOnQuit says whether the quit puts the agents back: the setting,
// and a gateway this process serves (another magpie serving it goes on
// serving its agents). A settings file that can't be read leaves them.
func restoreOnQuit() bool {
	s, err := settings.LoadChecked()
	return err == nil && s.RestoreOnQuit && servedHere()
}

// hooks, vars for the tests
var (
	servedHere  = func() bool { return served.Load() != nil }
	quitDetect  = agent.Detected
	quitRestore = agent.RestoreOnQuit
	startAgents = agent.All
	startRewire = agent.RewireOnStart
)

// restoreAgentsOnQuit is OnShutdown's part of it.
func restoreAgentsOnQuit() {
	if !restoreOnQuit() {
		return
	}
	ids, err := quitRestore(quitDetect())
	if err != nil {
		log.Println("restore agents on quit:", err)
	}
	if len(ids) > 0 {
		log.Printf("quit: %d agents put back as they were", len(ids))
	}
}

// quitDisconnect is the quit question's Disconnect and quit (quitask.js):
// each connected agent put back as RestoreOnQuit puts it, so the next
// start connects it again. It disconnected each one as its row's
// Disconnect does, whose mark keeps an agent off at every start after:
// a user who chose it so their agents work while magpie is closed found
// every switch off when magpie opened again, and turned each back on by
// hand (jtbbtnt54249 on X, "退出有时候又要重新开启"). An agent that can't
// be put back is named in the error, and magpie stays open.
func quitDisconnect() error {
	ids, err := quitRestore(quitDetect())
	if len(ids) > 0 {
		log.Printf("quit: %d agents put back as they were, for the next start", len(ids))
	}
	return err
}

// rewireAgentsOnStart connects again the agents a quit put back, unless
// the gateway is paused (the pause's own resume connects its agents).
func rewireAgentsOnStart() {
	if pausedNow() {
		return
	}
	if ids, _ := startRewire(startAgents()); len(ids) > 0 {
		log.Printf("start: %d agents connected again", len(ids))
	}
}
