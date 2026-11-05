package agent

import (
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// Rescan forgets what was found of the agents on this machine a while ago,
// so the next read of them looks again at once: the reader asked for it with
// the Agents page's refresh (#844), having just installed an agent, or
// removed one, or changed its settings outside magpie. What is read from an
// agent's files every time needs nothing forgotten; these are the answers
// kept for a while because asking was slow (the apps folder, wsl.exe, Alma's
// server), and where each command was found on PATH (proc.LookPath).
func Rescan() {
	proc.Forget()

	// where updates come from is asked again, too: a version published
	// after magpie started showed no update until the memo ran out (#4980)
	latests.expireAll()

	cursorLocalSeen.Lock()
	cursorLocalSeen.at = time.Time{}
	cursorLocalSeen.Unlock()

	wsl.Lock()
	wsl.at, wsl.runAt = time.Time{}, time.Time{}
	// a running distro is asked again, and one that failed is retried now
	// rather than in ten minutes
	wsl.probed, wsl.failed = map[string]bool{}, map[string]time.Time{}
	wsl.Unlock()

	almaRead.Lock()
	almaRead.at, almaRead.body = nil, nil
	almaRead.gen++
	almaRead.Unlock()
}
