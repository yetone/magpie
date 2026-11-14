package gateway

// A turn's end, for the notification that an agent is done (itieniu on X):
// the user sets a long task going and works in another window, and wants
// to hear when it has finished.
//
// The gateway sees only requests, not turns. A turn is the requests one
// agent's session sends from the user's prompt to its last reply: each
// reply that asks the agent to run a tool is followed by another request
// once the tool has run, and the last reply asks for none. So a session's
// turn is done when a reply that called no tool has ended, nothing of the
// session is going, and nothing more came for turnQuiet after it. A
// request magpie makes itself (System One, titles answered here), one an
// agent makes on the side (Kind: titles, compaction …) and a Claude Code
// subagent's (its x-claude-code-agent-id) aren't the turn's: a subagent's
// last reply is not its parent's.

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// TurnEnd is a turn that ran at least as long as Settings' TurnDoneAlert.
type TurnEnd struct {
	Agent   string // magpie's id for it: claude, codex, opencode …
	Session string // the agent's session id, "" when it sends none
	Folder  string // the folder it says it works in, "" when it doesn't
	Took    time.Duration
}

// TurnDone is told of each such turn; set by the app, which notifies.
// Nil (magpie serve, the CLI) has turns not followed at all.
var TurnDone func(TurnEnd)

// turnQuiet is how long after a reply that called no tool nothing more of
// its session comes before its turn is done: an agent sends its next
// request at once, the reply's own follow-ups (a title, a suggestion)
// within a second or two.
var turnQuiet = 4 * time.Second

// turnAfter is how long a turn runs before its end is said; 0 says none.
var turnAfter = func() time.Duration {
	if TurnDone == nil {
		return 0
	}
	return time.Duration(settings.Shared().TurnDoneAlert) * time.Second
}

// turnStale: a session with nothing for this long starts its next turn
// over, whatever its last request was (a reply read wrong as calling a
// tool, an agent closed mid-turn)
const turnStale = 30 * time.Minute

type turnState struct {
	start, last time.Time
	going       int
	folder      string
	timer       *time.Timer
}

// turns follows each conversation's turns. An agent that sends no session
// id has its concurrent turns told apart by the conversation a request
// belongs to (Conv, the first user turn's digest): without it every task
// an agent runs at once would share one clock, and a finished task's
// notification would read the longest task still going (#2001).
type turns struct {
	mu sync.Mutex
	m  map[string]*turnState
}

func sessionTurnKey(r *Route) string {
	session := r.Session
	if session == "" {
		session = r.Conv
	}
	return r.Agent + "\x00" + session
}

// begin is a request of a turn starting.
func (ts *turns) begin(r *Route, now time.Time) {
	dir := r.dirOf
	r.dirOf = nil // the body it holds isn't kept with the route
	if !r.turn {
		return
	}
	if turnAfter() <= 0 {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.m == nil {
		ts.m = map[string]*turnState{}
	}
	for k, st := range ts.m {
		if st.going == 0 && now.Sub(st.last) > turnStale {
			if st.timer != nil {
				st.timer.Stop()
			}
			delete(ts.m, k)
		}
	}
	k := sessionTurnKey(r)
	st := ts.m[k]
	if st == nil {
		st = &turnState{start: now, last: now}
		if dir != nil {
			st.folder = dir()
		}
		ts.m[k] = st
	}
	if st.timer != nil {
		st.timer.Stop() // the agent went on: not done yet
		st.timer = nil
	}
	st.going++
}

// end is a request of a turn ending; final when its reply went whole to
// the agent, finished, and called no tool (turnFinal).
func (ts *turns) end(r *Route, final bool, now time.Time) {
	if !r.turn {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	st := ts.m[sessionTurnKey(r)]
	if st == nil {
		return // begun while turns weren't followed
	}
	st.going = max(st.going-1, 0)
	st.last = now
	if st.going > 0 || !final {
		return
	}
	// read here, not in the timer: what is in force as the reply ends
	k, agent, session, after, done := sessionTurnKey(r), r.Agent, r.Session, turnAfter(), TurnDone
	var t *time.Timer
	t = time.AfterFunc(turnQuiet, func() {
		ts.mu.Lock()
		cur := ts.m[k]
		if cur == nil || cur.timer != t {
			ts.mu.Unlock()
			return // stopped, or another took its place
		}
		delete(ts.m, k)
		ts.mu.Unlock()
		if took := cur.last.Sub(cur.start); after > 0 && took >= after && done != nil {
			done(TurnEnd{Agent: agent, Session: session, Folder: cur.folder, Took: took})
		}
	})
	st.timer = t
}

// toolScan sees whether a reply going to the agent asks it to run a tool,
// in any of the wire formats: an Anthropic tool_use block or stop_reason,
// a Chat finish_reason of tool_calls (function_call before it), a
// Responses output item a client runs, a Gemini functionCall part. Only a
// JSON value is looked at ("type":"tool_use"), never a key, as a Chat
// delta may hold "tool_calls":[] with none; a string's escaped quotes
// (\"tool_use\") never match.
//
// It sees too whether the reply ended unfinished (#4866, emo172): an error
// event under a 200 (Anthropic's and Responses' "type":"error", Chat's and
// Gemini's "error":{…}), a Responses reply failed or incomplete, a reply
// cut at its length (max_tokens, length, MAX_TOKENS); and whether the
// agent went away before the reply's last event, Esc or a closed window.
// None of these is the task done, whatever status the request ends with.
type toolScan struct {
	tail  []byte
	saw   bool
	cut   bool            // the reply said it ended unfinished
	ended bool            // the reply's last event, or a stop reason, went by
	ctx   context.Context // the agent's request: done when it went away
}

var toolValues = [][]byte{
	[]byte(`"tool_use"`), []byte(`"tool_calls"`), []byte(`"function_call"`),
	[]byte(`"custom_tool_call"`), []byte(`"local_shell_call"`), []byte(`"shell_call"`),
	[]byte(`"apply_patch_call"`), []byte(`"computer_call"`), []byte(`"mcp_approval_request"`),
}

var geminiCall = []byte(`"functionCall"`)

// replyMark is a member of a reply's JSON: a key and how its value starts.
// The key is matched with its quotes, so a key a string holds, escaped
// (\"type\"), never matches.
type replyMark struct{ key, value []byte }

func mark(key, value string) replyMark { return replyMark{[]byte(key), []byte(value)} }

// cutMarks say a reply ended unfinished, in each wire format.
var cutMarks = []replyMark{
	mark(`"type"`, `"error"`), // Anthropic's and Responses' error event
	mark(`"type"`, `"response.failed"`), mark(`"type"`, `"response.incomplete"`), mark(`"type"`, `"response.cancelled"`),
	mark(`"error"`, `{`), // Chat's and Gemini's error, a failed response's; a success has "error":null
	mark(`"stop_reason"`, `"max_tokens"`), mark(`"stop_reason"`, `"model_context_window_exceeded"`),
	mark(`"finish_reason"`, `"length"`), mark(`"finishReason"`, `"MAX_TOKENS"`),
}

// endMarks say a reply got to its end; a cut one ends it too.
var endMarks = []replyMark{
	mark(`"type"`, `"response.completed"`), mark(`"type"`, `"message_stop"`),
	mark(`"stop_reason"`, `"`), mark(`"finish_reason"`, `"`), mark(`"finishReason"`, `"`),
}

// clientTool is a Responses item Codex runs itself that isn't one of
// toolValues: a tool_search_call it runs ("execution":"client"), which a
// search the backend ran itself doesn't say.
var clientTool = mark(`"execution"`, `"client"`)

func (s *toolScan) add(p []byte) {
	if s == nil || len(p) == 0 || s.saw && s.cut {
		return
	}
	b := append(s.tail, p...)
	s.saw = s.saw || toolIn(b) || marked(b, clientTool)
	if !s.cut {
		for _, m := range cutMarks {
			if marked(b, m) {
				s.cut, s.ended = true, true
				break
			}
		}
	}
	if !s.ended {
		for _, m := range endMarks {
			if marked(b, m) {
				s.ended = true
				break
			}
		}
		s.ended = s.ended || bodyHas(b, "data: [DONE]") || bodyHas(b, "data:[DONE]") ||
			bodyHas(b, "event: message_stop") || bodyHas(b, "event:message_stop")
	}
	keep := min(len(b), 64) // longer than any value with its colon and spaces
	s.tail = append(s.tail[:0:0], b[len(b)-keep:]...)
}

// marked reports whether b holds m's key, a colon and a value starting as
// m's does, with any spaces between.
func marked(b []byte, m replyMark) bool {
	for i := 0; ; {
		j := bodyIndexBytes(b[i:], m.key)
		if j < 0 {
			return false
		}
		i += j + len(m.key)
		rest := bytes.TrimLeft(b[i:], " \t\r\n")
		if len(rest) == 0 || rest[0] != ':' {
			continue
		}
		if bytes.HasPrefix(bytes.TrimLeft(rest[1:], " \t\r\n"), m.value) {
			return true
		}
	}
}

func toolIn(b []byte) bool {
	for _, v := range toolValues {
		for i := 0; ; {
			j := bodyIndexBytes(b[i:], v)
			if j < 0 {
				break
			}
			at := i + j
			if k := bytes.TrimRight(b[:at], " \t\r\n"); len(k) > 0 && k[len(k)-1] == ':' {
				return true
			}
			i = at + 1
		}
	}
	for i := 0; ; {
		j := bodyIndexBytes(b[i:], geminiCall)
		if j < 0 {
			return false
		}
		at := i + j + len(geminiCall)
		if k := bytes.TrimLeft(b[at:], " \t\r\n"); len(k) > 0 && k[0] == ':' {
			return true
		}
		i = at
	}
}

// called reports whether the reply called a tool; false for a nil scan.
func (s *toolScan) called() bool { return s != nil && s.saw }

// unfinished reports whether the reply ended without the task done: it said
// it failed or was cut, or the agent went away before its end; false for a
// nil scan.
func (s *toolScan) unfinished() bool {
	return s != nil && (s.cut || s.ctx != nil && s.ctx.Err() != nil && !s.ended)
}

// turnFinal says whether r's reply ends its turn: it went whole to the
// agent and called no tool. A failure, a reply cut short or failed under a
// 200, and the agent gone before the reply's end (Esc, a closed window)
// are none (#4866).
func turnFinal(r *Route) bool {
	return r.Status < 400 && r.Error == "" && !r.tools.called() && !r.tools.unfinished()
}

// TurnFolder is the last part of a folder, for a notification.
func TurnFolder(dir string) string {
	dir = strings.TrimRight(strings.ReplaceAll(dir, `\`, "/"), "/")
	if dir == "" {
		return ""
	}
	return filepath.Base(filepath.FromSlash(dir))
}

// agentTurnOf says whether a request is of its agent's turn, with the scan its
// reply goes through when it is: not one the agent makes on the side
// (kind) or a Claude Code subagent's.
func agentTurnOf(r *http.Request, kind string) (bool, *toolScan) {
	if sub, _ := subagentOf(r.Header); kind != "" || sub != "" {
		return false, nil
	}
	return true, &toolScan{ctx: r.Context()}
}

// dirOf reads the folder body says its agent works in, when asked.
func dirOf(proto provider.Protocol, body []byte) func() string {
	return func() string {
		return factsOf(proto, body).dir
	}
}
