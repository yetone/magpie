package sessions

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

// An agent that was uninstalled leaves its sessions behind: OpenCode's are
// rows in a database magpie only reads (_SekiBetu_ on X: after removing
// OpenCode, its sessions stayed listed with a Resume and no Delete). Two
// things follow from the command being gone. Resume is no longer offered:
// the line would fail. And the list may be cleared: magpie can't delete
// rows of a database the agent keeps, but it can stop listing them, which
// is what Forget does. The agent's own files are never touched, and Restore
// in the trash lists the session again.

// cliOf is the command each agent's sessions are resumed with, for the
// agents magpie can't move to the trash (Deletable) as well.
var cliOf = map[string]string{
	"claude": "claude", "codex": "codex", "opencode": "opencode", "pi": "pi",
	"cline": "cline", "qoder": "qodercli", "qoder-cn": "qoderclicn",
	"grok": "grok", "codebuddy": "codebuddy", "omp": "omp", "droid": "droid",
	"kimi": "kimi", "agy": "agy", "cursor": "cursor-agent", "hermes": "hermes",
}

// cliGone says whether an agent's command is not installed on this
// computer. An agent with no command of its own (an app) is never gone.
// A var so tests can say; a test run answers false.
var cliGone = func(agent string) bool {
	bin := cliOf[agent]
	return bin != "" && !testing.Testing() && !lookOK(bin)
}

// Forgettable says whether magpie can stop listing an agent's sessions: its
// sessions can't be moved to the trash, and its command is gone.
func Forgettable(agent string) bool { return !Deletable(agent) && cliGone(agent) }

// Removable says whether Delete is on an agent's rows: they go to the
// trash, or, with the agent uninstalled, off the list (Forget).
func Removable(agent string) bool { return Deletable(agent) || Forgettable(agent) }

// Forget takes a listed session of an agent that is gone off the list, as
// DeleteGateway does for one seen only through the gateway: its note is in
// the trash, and Restore lists it again, as does a use of it after.
func Forget(agent, id string) (Trashed, error) {
	if !Forgettable(agent) {
		return Trashed{}, errors.New("magpie can't delete this agent's sessions")
	}
	for _, m := range ListAgent(agent) {
		if m.ID == id {
			return forgetSession(agent, id, m.Title, m.Last)
		}
	}
	return Trashed{}, errors.New("no such session")
}

func forgetSession(agent, id, title string, last time.Time) (Trashed, error) {
	t, err := DeleteGateway(agent, id, title, last)
	if err != nil {
		return t, err
	}
	// DeleteGateway wrote the note without it: say it is no gateway text
	t.Forgot = true
	dir := trashFolder(t.Key)
	if dir == "" {
		return t, errors.New("no such deleted session")
	}
	return t, writeManifest(dir, t)
}

// ForgottenOut leaves out of a listing the rows of an agent that is gone
// (Forgettable) that Forget took off the list, up to their last request.
// The list is returned as it is when nothing is left out.
func ForgottenOut(list []Session) []Session {
	gone := map[string]bool{}
	out := func(hidden func(agent, id string, last time.Time) bool, s Session) bool {
		g, ok := gone[s.Agent]
		if !ok {
			g = Forgettable(s.Agent)
			gone[s.Agent] = g
		}
		return g && hidden(s.Agent, s.ID, s.Last)
	}
	hidden := GatewayHidden()
	if !slices.ContainsFunc(list, func(s Session) bool { return out(hidden, s) }) {
		return list
	}
	return slices.DeleteFunc(slices.Clone(list), func(s Session) bool { return out(hidden, s) })
}

// HiddenStamp changes whenever the hidden list is written, for a poll's tag.
func HiddenStamp() string {
	fi, err := os.Stat(gatewayHiddenPath())
	if err != nil {
		return ""
	}
	return fmt.Sprint(fi.ModTime().UnixNano(), "/", fi.Size())
}

// ForceCLIGone says, for a test in another package, which agents' commands
// are gone; the returned func puts it back.
func ForceCLIGone(agents ...string) (restore func()) {
	old := cliGone
	cliGone = func(a string) bool { return slices.Contains(agents, a) }
	return func() { cliGone = old }
}
