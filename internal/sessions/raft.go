package sessions

import (
	"os"
	"path/filepath"

	"github.com/yetone/magpie/internal/appdir"
)

// Raft (the slock app, raft-computer) runs its agents each in a workspace
// of its own, <slock>/agents/<agent id>/. Those it runs on Pi keep their
// sessions there in .pi-sessions/, and those on its built-in runtime (Pi
// too, with its own agent folder) in .builtin-sessions/: Pi's SDK files,
// <time>_<session id>.jsonl, all in one folder (PiDriver's
// buildPiSessionDir, SessionManager.create). They are read as Pi's, as
// agent "raft". The handoff notes in <slock>/runtime-sessions are not
// sessions. Raft's Pi reads the user's own Pi agent folder, so one sent
// through magpie is the gateway's "pi" session of the same id too
// (GatewayKeys).

// SlockDir is Raft's folder: $SLOCK_HOME, else ~/.slock
// (%USERPROFILE%\.slock on Windows).
func SlockDir() string {
	if d := appdir.Getenv("SLOCK_HOME"); d != "" {
		if abs, err := filepath.Abs(expandHome(d)); err == nil {
			return abs
		}
		return expandHome(d)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".slock")
}

func raftFiles() []file { return raftFilesIn(SlockDir()) }

// raftFilesIn are the Pi sessions of the agents in a Raft folder.
func raftFilesIn(dir string) []file {
	if !Reads("raft") {
		return nil
	}
	var paths []string
	for _, sub := range []string{".pi-sessions", ".builtin-sessions"} {
		more, _ := filepath.Glob(filepath.Join(dir, "agents", "*", sub, "*.jsonl"))
		paths = append(paths, more...)
	}
	return piNamed("raft", paths)
}

// GatewayKeys are the "agent|id" keys a session's gateway record can
// carry: its own; for Raft's, Pi's, as Raft's Pi tells the gateway it is
// Pi; for one Claude Desktop's Code tab ran, Desktop's, as the gateway
// knows its requests by Desktop's user agent; the ids of the copies of its
// conversation (ccCopies); and its subagents' own ids (subIDs), whose calls
// it counts already. MergeExternal keeps the same keys as pairs.
//
// A ZCode request names its session by the id its database gives it with a
// prefix — sess_…, a subagent's sess_subagent_agent_… (zcodeBareID) — so the
// bare id is a key too: without it the same session was a row under its
// folder and again a gateway-only row.
func GatewayKeys(s Session) []string {
	k := []string{s.Agent + "|" + s.ID}
	if s.Agent == "raft" {
		k = append(k, "pi|"+s.ID)
	}
	if s.App != "" {
		k = append(k, s.App+"|"+s.ID)
	}
	for _, id := range s.Copies {
		k = append(k, s.Agent+"|"+id)
	}
	for _, id := range s.Subs {
		for _, rid := range requestIDs(s.Agent, id) {
			k = append(k, s.Agent+"|"+rid)
		}
	}
	for _, rid := range requestIDs(s.Agent, s.ID)[1:] {
		k = append(k, s.Agent+"|"+rid)
	}
	return k
}

// requestIDs are the ids a request through the gateway can name the
// session id by, id itself first: ZCode's database gives an id a prefix
// (sess_…, a subagent's sess_subagent_agent_…) its requests leave out
// (zcodeBareID). GatewayKeys and NativeIDs both read it, so the Sessions
// list, its spend and the management page agree on which gateway record
// is a session here (#5754).
func requestIDs(agent, id string) []string {
	if agent == "zcode" {
		if bare := zcodeBareID(id); bare != id {
			return []string{id, bare}
		}
	}
	return []string{id}
}
