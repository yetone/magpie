package gateway

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// Codex's side calls ask for one of Codex's own models by its bare name,
// whatever the conversation is on: its memories are written on
// gpt-6-luna and gpt-6.1-terra (gpt-5.6-luna and gpt-5.6-terra before
// Codex 0.167.0), its auto-review runs on
// codex-auto-review. With the conversation on one of magpie's models
// beside Codex's ChatGPT sign-in, each of those went to that ChatGPT
// account rather than where the user had routed Codex (cybercaius on
// Discord). With nothing set for them, the memories follow the session:
// they go on the model the conversation's own turns go on, when that is
// one of magpie's (servedSession; started with a turn, after that turn:
// codexMemorySession), as the app's titles,
// descriptions, summaries and composer predictions do (codexSideTo). The auto-review follows it when set to
// (settings.CodexAutoReviewSession, which codexcat writes into each of
// magpie's entries); left to Codex it stays on codex-auto-review, which a
// ChatGPT sign-in runs free (#1593).

// CodexSideSet says whether Codex's own config names the model for a side
// call ("memories": [memories] extract_model or consolidation_model), true when the config can't be
// read; nil leaves every such call as Codex sent it (agent.CodexSideSet).
var CodexSideSet func(which string) bool

// codexLeadsCap bounds how many conversations codexLeads keeps.
const codexLeadsCap = 512

// codexLeads is the model each Codex conversation's own turns last went
// on, by its thread id, and the one any of them last went on, by the
// gateway key it came with.
type codexLeads struct {
	mu     sync.Mutex
	thread map[string]codexLead
	last   map[string]codexLead
	// next is closed, and replaced, when any conversation's own turn
	// comes (codexSideSession waits on it)
	next chan struct{}
}

type codexLead struct {
	model string
	at    time.Time
}

// saw notes a turn of a Codex conversation's own (no call kind) on model.
func (l *codexLeads) saw(r *http.Request, kind, model string) {
	if kind != "" || model == "" {
		return
	}
	key := access.Caller(r.Context()).KeyID
	thread := sessionOf(r.Header)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.thread == nil {
		l.thread, l.last = map[string]codexLead{}, map[string]codexLead{}
	}
	l.last[key] = codexLead{model: model, at: time.Now()}
	if l.next != nil {
		close(l.next)
		l.next = nil
	}
	if thread == "" {
		return
	}
	if _, ok := l.thread[thread]; !ok && len(l.thread) >= codexLeadsCap {
		oldest, at := "", time.Time{}
		for id, v := range l.thread {
			if oldest == "" || v.at.Before(at) {
				oldest, at = id, v.at
			}
		}
		delete(l.thread, oldest)
	}
	l.thread[thread] = codexLead{model: model, at: time.Now()}
}

// lookup is the model the conversation a side call was made for is on:
// its parent thread's (a review's conversation), the thread it was forked
// from (a composer prediction), else its own thread's, else the one this
// key's Codex last had a turn on; with whether the call's conversation was
// one magpie has seen a turn of, and when this key's last turn came when
// it wasn't.
func (l *codexLeads) lookup(r *http.Request, body []byte) (model string, tied bool, at time.Time) {
	meta := requestSessionMetadata(r.Header, body)
	parent := meta.Parent
	if parent == "" {
		parent = strings.TrimSpace(r.Header.Get("x-codex-parent-thread-id"))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, id := range []string{parent, meta.Forked, sessionOf(r.Header)} {
		if v, ok := l.thread[id]; ok && id != "" {
			return v.model, true, v.at
		}
	}
	v := l.last[access.Caller(r.Context()).KeyID]
	return v.model, false, v.at
}

// codexSideFresh is how recent the key's last turn must be for a side
// call tied to no conversation magpie has seen to take its model at once;
// codexSideWait bounds how long such a call waits for the turn it was
// started beside.
var (
	codexSideFresh = 3 * time.Second
	codexSideWait  = 5 * time.Second
)

// sideOf is of for a side call started beside a turn: the Codex app's
// title, description, summary or prediction, and Codex's memories. Codex
// writes a new conversation's title on a temporary thread of its own, with
// no link to the conversation, started as the user's first message is
// sent, so it often reaches magpie before that conversation's first turn.
// Read then, the key's last turn is the previous conversation's, on the
// model the user switched away from (cybercaius on Discord: the first
// title after a switch came from the old model). When this key's last
// turn is older than codexSideFresh, such a call waits, up to
// codexSideWait or until it is cancelled, for the next turn of any
// conversation, and takes that one's model.
//
// beside is a call Codex starts with every turn of its conversation, tied
// to it: its memories, which Codex extracts and consolidates as each turn
// with the user's input starts (codex-rs app-server turn_processor,
// start_memories_startup_task), under that conversation's thread. Racing
// the turn, the conversation's last model is the one before a switch, so
// it waits too. A call tied to a conversation that isn't (a composer
// prediction, a known thread's title) takes its last model at once.
func (l *codexLeads) sideOf(r *http.Request, body []byte, beside bool) string {
	model, tied, at := l.lookup(r, body)
	if tied && !beside || model == "" || time.Since(at) < codexSideFresh {
		return model
	}
	l.mu.Lock()
	if l.next == nil {
		l.next = make(chan struct{})
	}
	next := l.next
	l.mu.Unlock()
	t := time.NewTimer(codexSideWait)
	defer t.Stop()
	select {
	case <-next:
	case <-t.C:
	case <-r.Context().Done():
	}
	model, _, _ = l.lookup(r, body)
	return model
}

// codexFollowTo is the model a Codex side call that asked for one of
// Codex's own models goes on instead: the session's, when it is one of
// magpie's and nothing is set for that call. "" leaves the call as it
// came: another call, a model of magpie's asked for, a model set for it, a
// conversation on one of Codex's own models, or a session model magpie
// can't serve it on now.
func (s *Server) codexFollowTo(r *http.Request, agent, kind, asked string, body []byte) string {
	if agent != "codex" && !fromCodex(r.Header) || asked == "" || strings.Contains(asked, "/") {
		return ""
	}
	// A subagent isn't one of them (mengxiao on Discord): Codex starts a
	// spawn that names no model on its lead's (codex-rs core/src/agent/
	// child_config.rs, build_agent_spawn_config), so one of Codex's bare
	// models in a subagent's request under a lead on magpie's was named,
	// in spawn_agent's model, in [agents] default_subagent_model or in a
	// role's config. Moving it onto the lead's model overrode the user's
	// own pick ("use GPT-6.1 Terra for the subagent"); only the model set
	// for Codex's subagents in magpie moves one (codexSubagentPick).
	if !isMemoryKind(kind) {
		return ""
	}
	// the user's own pick in Codex's config is kept; read before the
	// session, which a memory call may wait on
	if CodexSideSet == nil || CodexSideSet("memories") {
		return ""
	}
	// started with a turn, maybe the first after a switch
	to := s.codexMemorySession(r, body)
	if to == "" || to == asked {
		return ""
	}
	return to
}

// codexSideSession is the model of magpie's the Codex app's own side
// turns follow, read after the turn they were started beside
// (codexLeads.sideOf, servedSession).
func (s *Server) codexSideSession(r *http.Request, body []byte) string {
	return s.servedSession(r, s.codexLeads.sideOf(r, body, false))
}

// codexMemorySession is codexSideSession for Codex's memory calls, read
// after the turn they were started with (codexLeads.sideOf, beside).
func (s *Server) codexMemorySession(r *http.Request, body []byte) string {
	return s.servedSession(r, s.codexLeads.sideOf(r, body, true))
}

// servedSession is to, the model a Codex side call's conversation's own
// turns go on (codexLeads), else, before any turn of Codex's has come
// through magpie (Codex consolidates its memories as it starts), the model
// Codex's config is set to (CodexSideModel), when magpie can serve it to
// this caller now. cybercaius on Discord: with the model picked in the
// Codex app, not its config, the memories went to ChatGPT, and so did the
// titles, descriptions and composer predictions, which followed the config
// alone. "" when the conversation is on one of Codex's own models, or
// magpie can't serve its model now (turned off, gone, or one the caller's
// key is held from).
func (s *Server) servedSession(r *http.Request, to string) string {
	if to == "" && CodexSideModel != nil {
		to = CodexSideModel()
	}
	if !strings.Contains(to, "/") {
		return ""
	}
	if _, off := idOff(to); off {
		return ""
	}
	p, model, ok := provider.Resolve(to)
	if !ok {
		return ""
	}
	if who, held := keyHolds(r); held && !modelAllowed(who, p, model) {
		return ""
	}
	return to
}
