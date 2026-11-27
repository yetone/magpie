package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
)

// A tool call through the router can take a while, and the server behind
// it may say how far along it is (notifications/progress) or log what it
// does (notifications/message) while it runs (uratmangun2 on X, #7920).
// An agent that asked for progress on its call (_meta.progressToken), or
// set a log level for its session (logging/setLevel), and takes an event
// stream back for its POST, is told them on that call's stream, before its
// result: the library hands each one to the call's HubNote, a hubRelay.
// An agent that did neither, or takes JSON only, gets its result as before.

// HubNote takes one notification the server behind a hub call sent about
// it: notifications/progress (its progressToken the one the library gave
// the server) or notifications/message. It never blocks: it is called on
// the goroutine that reads the server's every reply.
type HubNote func(method string, params json.RawMessage)

type hubNoteKey struct{}

type hubNoted struct {
	n        HubNote
	progress bool
}

// WithHubNote is ctx carrying n, for the call ctx is of; progress is
// whether the agent asked for the call's progress, so the server is asked
// for it too.
func WithHubNote(ctx context.Context, n HubNote, progress bool) context.Context {
	return context.WithValue(ctx, hubNoteKey{}, hubNoted{n, progress})
}

// HubNoteOf is where a hub call's notifications go, and whether its
// progress is wanted; nil when the agent asked for none or can't be told
// them, and the server then isn't asked for progress.
func HubNoteOf(ctx context.Context) (HubNote, bool) {
	n, _ := ctx.Value(hubNoteKey{}).(hubNoted)
	return n.n, n.n != nil && n.progress
}

// hubLogLevels are MCP's log levels, least severe first (RFC 5424's).
var hubLogLevels = []string{"debug", "info", "notice", "warning", "error", "critical", "alert", "emergency"}

// hubLevels are the log levels clients of the router set (logging/setLevel),
// by their path's agent and session: the level is a session's, and one
// server behind the router serves every agent, so it is applied here, to
// what this client is told, not passed on to the servers.
var hubLevels = struct {
	sync.Mutex
	m map[string]int
}{m: map[string]int{}}

// hubLevelsMost is how many sessions' levels are kept; past it one is let
// go, and that session is told every level again.
const hubLevelsMost = 4096

func hubLevelKey(r *http.Request, agent string) string {
	sid := boundedSession(r.Header.Get("Mcp-Session-Id"))
	if sid == "" {
		return ""
	}
	return agent + "\x00" + sid
}

// hubSetLevel answers logging/setLevel: the level kept for the session r
// is of. A client with no session is answered too, and told every level.
func hubSetLevel(r *http.Request, agent string, params json.RawMessage) (any, map[string]any) {
	var p struct {
		Level string `json:"level"`
	}
	json.Unmarshal(params, &p)
	i := slices.Index(hubLogLevels, p.Level)
	if i < 0 {
		return nil, map[string]any{"code": -32602, "message": "invalid params: level is one of debug, info, notice, warning, error, critical, alert, emergency"}
	}
	if k := hubLevelKey(r, agent); k != "" {
		hubLevels.Lock()
		if _, had := hubLevels.m[k]; !had && len(hubLevels.m) >= hubLevelsMost {
			for old := range hubLevels.m {
				delete(hubLevels.m, old)
				break
			}
		}
		hubLevels.m[k] = i
		hubLevels.Unlock()
	}
	return map[string]any{}, nil
}

// hubLevelOf is the level the session r is of set, and whether it set one.
func hubLevelOf(r *http.Request, agent string) (int, bool) {
	k := hubLevelKey(r, agent)
	if k == "" {
		return 0, false
	}
	hubLevels.Lock()
	defer hubLevels.Unlock()
	i, ok := hubLevels.m[k]
	return i, ok
}

// hubProgressToken is the call's own progressToken, when it is one MCP
// takes (a string or an integer).
func hubProgressToken(t json.RawMessage) json.RawMessage {
	t = bytes.TrimSpace(t)
	if len(t) == 0 || string(t) == "null" {
		return nil
	}
	var s string
	var n int64
	if t[0] == '"' && json.Unmarshal(t, &s) == nil || t[0] != '"' && json.Unmarshal(t, &n) == nil {
		return t
	}
	return nil
}

// hubRelayQueue is how many notifications wait for a slow agent; past it
// the newest are dropped, never the server's reader held up.
const hubRelayQueue = 64

// hubRelay tells one agent's call the notifications of the servers it
// runs on, on the call's own event stream.
type hubRelay struct {
	x        *mcpExchange
	token    json.RawMessage // the agent's progressToken; nil: none relayed
	progress bool            // false: progress isn't relayed (run_code's several calls)
	level    int             // the least level of a log line relayed

	mu     sync.Mutex
	q      chan any
	closed bool
	done   chan struct{}
}

// newHubRelay is the relay of the tools/call r is, when its agent asked for
// progress (token) or set a log level, and takes an event stream back;
// nil when it can't be told anything. progress false relays log lines
// only: run_code runs several tools under one call, and one token's
// progress must only grow.
func newHubRelay(r *http.Request, agent string, token json.RawMessage, progress bool) *hubRelay {
	x, _ := r.Context().Value(mcpExchangeKey{}).(*mcpExchange)
	if x == nil || !x.canStream {
		return nil
	}
	token = hubProgressToken(token)
	level, set := hubLevelOf(r, agent)
	if token == nil && !set {
		return nil
	}
	if !progress {
		token = nil
	}
	h := &hubRelay{x: x, token: token, progress: progress && token != nil, level: level, q: make(chan any, hubRelayQueue), done: make(chan struct{})}
	go func() {
		defer close(h.done)
		for m := range h.q {
			h.x.send(m)
		}
	}()
	return h
}

// note is the relay's HubNote.
func (h *hubRelay) note(method string, params json.RawMessage) {
	var p map[string]json.RawMessage
	if json.Unmarshal(params, &p) != nil || p == nil {
		return
	}
	switch method {
	case "notifications/progress":
		if !h.progress {
			return
		}
		// the agent's own token, not the one the server was given
		p["progressToken"] = h.token
	case "notifications/message":
		var level string
		json.Unmarshal(p["level"], &level)
		if i := slices.Index(hubLogLevels, level); i >= 0 && i < h.level {
			return
		}
	default:
		return
	}
	m := map[string]any{"jsonrpc": "2.0", "method": method, "params": p}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	select {
	case h.q <- m:
	default:
	}
}

// end stops the relay once what it holds is sent, before the call's result
// is: nothing is written for the call after it.
func (h *hubRelay) end() {
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		close(h.q)
	}
	h.mu.Unlock()
	<-h.done
}

// withHubRelay is r carrying the relay of its call, and the end of it; r
// as it is, and an end that does nothing, when the call is told nothing.
func withHubRelay(r *http.Request, agent string, token json.RawMessage, progress bool) (*http.Request, func()) {
	h := newHubRelay(r, agent, token, progress)
	if h == nil {
		return r, func() {}
	}
	return r.WithContext(WithHubNote(r.Context(), h.note, h.progress)), h.end
}
