package claudetui

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The transcript has a message only once it ends, so on its own the
// helper tells a reply a whole message at a time. Claude Code is a Bun
// program, and Bun loads a script before it that BUN_OPTIONS names
// (--preload): tap.js copies the main turn's replies, as Anthropic streams
// them, to a file of the helper's, and the helper tells them on as -p's
// stream_event lines. The requests, and what Claude Code reads and
// writes, are its own: the tap reads a copy of each reply's body. A Claude
// Code that isn't Bun, or doesn't load it, writes nothing there, and its
// replies are told whole as before.
//
//go:embed tap.js
var tapJS []byte

// tapPrefix names the folders a tap is written to, the helper's pid after
// it; tap.js knows itself by it, to take itself out of what Claude Code
// hands on.
const tapPrefix = "magpie-claude-tap-"

// tapEnv names the file tap.js writes to.
const tapEnv = "MAGPIE_CLAUDE_TAP"

// GuardEnv names, to the helper, a file of the bridge's that holds the
// extra body (CLAUDE_CODE_EXTRA_BODY) of the client's latest turn: tap.js
// puts its safeguards in each request Claude Code makes, which reads that
// variable only from an environment nothing outside it can change. The
// bridge rewrites the file as a turn brings new ones, and tap.js writes
// <file>.live once it has seen a request it puts them in.
const GuardEnv = "MAGPIE_CLAUDE_GUARD"

// how long a message of the transcript waits for its stream, and a turn's
// end for its streams to end; and how often the tap is read while one
// goes on
var (
	tapWait = time.Second
	tapPoll = 10 * time.Millisecond
)

// tap reads what tap.js writes.
type tap struct {
	dir string
	f   *os.File
	rd  *bufio.Reader
	// part is a line not yet ended
	part []byte
	// on: tap.js was loaded and taps Claude Code's requests
	on bool
	// open are the streams going, by tap.js's number: the id of the
	// message each is, once its message_start comes
	open    map[int]*tapStream
	ended   map[int]bool
	started map[string]bool
	// dropped are the messages told whole, as their stream came late:
	// their stream says nothing
	dropped map[string]bool
	out     emitter
	sid     string
	// limits is the allowance a reply told whose message was told whole
	// or never began, of stream limitsOf: told before the turn's end
	limits   json.RawMessage
	limitsOf int
	// toldOf is the latest stream whose allowance was told with it: an
	// older one's is never told after it
	toldOf int
}

type tapStream struct {
	id      string
	stopped bool
	// limits is the rate_limit_info its reply's headers tell, until told
	limits json.RawMessage
}

// newTap writes tap.js and the file it writes to in a folder of the
// helper's, and returns the environment that has Claude Code load it:
// nil, with no tap, where that can't be done. path maps a path of the
// helper's to Claude Code's (shim.path).
func newTap(path func(string) string) (*tap, []string) {
	dir, err := os.MkdirTemp("", tapPrefix+strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		return nil, nil
	}
	t := &tap{dir: dir, open: map[int]*tapStream{}, ended: map[int]bool{}, started: map[string]bool{}, dropped: map[string]bool{}}
	js, out := filepath.Join(dir, "tap.js"), filepath.Join(dir, "tap.jsonl")
	if os.WriteFile(js, tapJS, 0o600) != nil || os.WriteFile(out, nil, 0o600) != nil {
		t.drop()
		return nil, nil
	}
	if t.f, err = os.Open(out); err != nil {
		t.drop()
		return nil, nil
	}
	t.rd = bufio.NewReaderSize(t.f, 1<<20)
	// BUN_OPTIONS is split at whitespace, with no quoting: a path with a
	// space in it takes its short name, or there is no tap
	preload := path(shortPath(js))
	if runtime.GOOS == "windows" && !strings.HasPrefix(preload, "/") {
		preload = filepath.ToSlash(preload)
	}
	if strings.ContainsAny(preload, " \t\r\n") {
		t.drop()
		return nil, nil
	}
	opts := "--preload " + preload
	// the user's own, but not a Windows one's for a Claude Code in WSL
	if v := strings.TrimSpace(os.Getenv("BUN_OPTIONS")); v != "" && os.Getenv(WSLEnv) == "" {
		opts = v + " " + opts
	}
	env := []string{"BUN_OPTIONS=" + opts, tapEnv + "=" + path(out)}
	if g := os.Getenv(GuardEnv); g != "" {
		env = append(env, GuardEnv+"="+path(g))
	}
	return t, env
}

// drop removes the tap's folder.
func (t *tap) drop() {
	if t == nil {
		return
	}
	if t.f != nil {
		_ = t.f.Close()
	}
	_ = os.RemoveAll(t.dir)
}

// read tells what tap.js wrote since it was last read.
func (t *tap) read() {
	if t == nil {
		return
	}
	for {
		line, err := t.rd.ReadBytes('\n')
		t.part = append(t.part, line...)
		if err != nil {
			return
		}
		t.line(t.part)
		t.part = nil
	}
}

func (t *tap) line(b []byte) {
	var l struct {
		Tap   string          `json:"tap"`
		S     int             `json:"s"`
		Open  bool            `json:"open"`
		End   string          `json:"end"`
		Event json.RawMessage `json:"e"`
		// the reply's anthropic-ratelimit-unified-* headers
		Headers map[string]string `json:"h"`
	}
	if json.Unmarshal(b, &l) != nil || t.ended[l.S] {
		return
	}
	switch {
	case l.Tap != "":
		t.on = l.Tap == "on"
	case l.Open:
		t.open[l.S] = &tapStream{limits: rateLimitInfo(l.Headers)}
	case len(l.Event) > 0:
		var e struct {
			Type    string `json:"type"`
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		}
		if json.Unmarshal(l.Event, &e) != nil {
			return
		}
		st := t.open[l.S]
		if e.Type == "message_start" {
			var limits json.RawMessage
			if st != nil {
				limits = st.limits
			}
			st = &tapStream{id: e.Message.ID, limits: limits}
			t.open[l.S] = st
			if t.dropped[st.id] {
				t.keepLimits(l.S, st)
				return
			}
			t.started[st.id] = true
			// as -p tells it, before the reply: the bridge says it on
			// the reply's headers (limitHeaders)
			if st.limits != nil && l.S > t.toldOf {
				t.out.emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": st.limits})
				t.toldOf = l.S
				if t.limitsOf < l.S {
					t.limits, t.limitsOf = nil, 0
				}
			}
			st.limits = nil
		}
		if st == nil || st.id == "" || t.dropped[st.id] {
			return
		}
		if e.Type == "message_stop" {
			st.stopped = true
		}
		t.event(l.Event)
	case l.End != "":
		t.end(l.S)
	}
}

// end ends stream n: one cut short ends as Claude Code ends such a
// stream in -p, with a message_stop and no message_delta, which the
// bridge takes as an attempt it asks again after.
func (t *tap) end(n int) {
	st := t.open[n]
	delete(t.open, n)
	t.ended[n] = true
	t.keepLimits(n, st)
	if st != nil && st.id != "" && !st.stopped && !t.dropped[st.id] {
		t.event(json.RawMessage(`{"type":"message_stop"}`))
	}
}

func (t *tap) event(e json.RawMessage) {
	t.out.emit(map[string]any{"type": "stream_event", "event": e, "session_id": t.sid, "parent_tool_use_id": nil})
}

// going says whether a stream is open, one with no message yet if bare.
func (t *tap) going(bare bool) bool {
	for _, st := range t.open {
		if !bare || st.id == "" {
			return true
		}
	}
	return false
}

// before is called before the transcript's message id is told: a message
// streamed is told after its stream has begun, for the bridge to leave
// its whole rows out; one whose stream hasn't come within tapWait is told
// whole, its stream left out. A message no stream can be (no reply is
// being read: Claude Code asked again without a stream) waits for none.
func (t *tap) before(id string) {
	if t == nil {
		return
	}
	t.read()
	if !t.on || t.started[id] || t.dropped[id] {
		return
	}
	for until := time.Now().Add(tapWait); t.going(true) && time.Now().Before(until); {
		time.Sleep(tapPoll)
		t.read()
		if t.started[id] {
			return
		}
	}
	t.dropped[id] = true
}

// settle is called before a turn's end is told: its streams end first,
// within tapWait, and one still going then ends there.
func (t *tap) settle() {
	if t == nil {
		return
	}
	t.read()
	for until := time.Now().Add(tapWait); t.going(false) && time.Now().Before(until); {
		time.Sleep(tapPoll)
		t.read()
	}
	for n := range t.open {
		t.end(n)
	}
	if t.limits != nil {
		t.out.emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": t.limits})
		t.limits, t.limitsOf = nil, 0
	}
}

// keepLimits keeps what stream n's reply told of the allowance, not
// told with its stream, for the turn's end, as a stream told mid-message
// would end it in the bridge: the latest reply's, and only when no later
// reply's was told already.
func (t *tap) keepLimits(n int, st *tapStream) {
	if st == nil || st.limits == nil {
		return
	}
	if n > t.toldOf && n >= t.limitsOf {
		t.limits, t.limitsOf = st.limits, n
	}
	st.limits = nil
}

// limitWindows are the windows Anthropic tells on a reply, as
// anthropic-ratelimit-unified-<window>-*, by the kind -p names them in
// its rate_limit_event (Claude Code 2.1.291).
var limitWindows = []struct{ kind, window string }{
	{"five_hour", "5h"},
	{"seven_day", "7d"},
	{"seven_day_overage_included", "7d_oi"},
}

// rateLimitInfo is a reply's allowance, from its headers, as -p tells it
// in a rate_limit_event's rate_limit_info: each window that says both its
// share used and when it renews. Nil when none does.
func rateLimitInfo(h map[string]string) json.RawMessage {
	num := func(name string) (float64, bool) {
		v := strings.TrimSpace(h["anthropic-ratelimit-unified-"+name])
		f, err := strconv.ParseFloat(v, 64)
		return f, v != "" && err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	}
	ws := map[string]any{}
	for _, w := range limitWindows {
		used, ok := num(w.window + "-utilization")
		reset, ok2 := num(w.window + "-reset")
		if ok && ok2 {
			ws[w.kind] = map[string]any{"utilization": used, "resetsAt": int64(math.Round(reset))}
		}
	}
	if len(ws) == 0 {
		return nil
	}
	info := map[string]any{"unifiedWindows": ws}
	if s := h["anthropic-ratelimit-unified-status"]; s != "" {
		info["status"] = s
	}
	b, _ := json.Marshal(info)
	return b
}
