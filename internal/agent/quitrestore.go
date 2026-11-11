package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// Restoring on quit (tudjam17795 on X: after magpie quits, the harness
// it was connected to can't go on). Settings › General › Put agents back
// when quitting is off by default: a connected agent keeps magpie as it
// quits, which is what the Quit question names. With it on, RestoreOnQuit
// does what each agent's Disconnect does, the files put back as they were
// before magpie, and RewireOnStart connects them again at the next start.
//
// Only the agents magpie had connected are touched, and only those are
// connected again: one the user disconnected stays so (its disconnectedKey
// mark), and one the user disconnected, or connected, while magpie was
// closed (the CLI) is left as they set it. Unlike a Disconnect, a quit
// leaves no mark, which would stop the start from connecting it again.

// quitRestoredKey is the stash's record of the agents a quit put back, a
// JSON list of ids.
const quitRestoredKey = "quit.restored"

// RestoreOnQuit disconnects each of as that has magpie in its config and
// that the user hasn't disconnected, and records which for RewireOnStart.
// An agent that can't be disconnected is left as it is and named in the
// error; the others go on.
func RestoreOnQuit(as []*Agent) (restored []string, err error) {
	var errs []string
	for _, a := range as {
		if a.Disconnected() || !a.Wired() {
			continue
		}
		if e := a.Disconnect(); e != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", a.Name, e))
			continue
		}
		a.forgetDisconnected()
		restored = append(restored, a.ID)
		b, _ := json.Marshal(restored)
		stash(map[string]string{quitRestoredKey: string(b)})
	}
	if len(errs) > 0 {
		err = fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return restored, err
}

// RewireOnStart connects again the agents RestoreOnQuit put back, among
// all (a list of All()). One connected again by hand meanwhile, or
// disconnected by the user since, is left as it is. The record is taken
// out first: an agent that can't be connected is said, not tried at every
// start.
func RewireOnStart(all []*Agent) (rewired []string, err error) {
	var ids []string
	json.Unmarshal([]byte(unstash(quitRestoredKey)), &ids)
	var errs []string
	for _, id := range ids {
		var a *Agent
		for _, c := range all {
			if c.ID == id {
				a = c
				break
			}
		}
		if a == nil || a.Wired() || a.Disconnected() {
			continue
		}
		if _, e := a.ConnectHow(); e != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", a.Name, e))
			continue
		}
		rewired = append(rewired, id)
	}
	if len(errs) > 0 {
		err = fmt.Errorf("%s", strings.Join(errs, "; "))
		log.Println("rewire after quit:", err)
	}
	return rewired, err
}

// forgetDisconnected takes out the mark Disconnect left.
func (a *Agent) forgetDisconnected() { forget(a.ID + disconnectedKey) }
