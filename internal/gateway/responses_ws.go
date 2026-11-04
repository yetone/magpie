package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

// Responses over a WebSocket, as Codex speaks it to its own provider
// (codex-rs codex-api/src/endpoint/responses_websocket.rs and
// core/src/client.rs at openai/codex 2f1e4f9). Codex opens one socket
// for a session and sends each turn on it as a text frame, the HTTP
// body with "type":"response.create" added; the reply comes back as one
// text frame for each event the HTTP route streams. On a socket it has
// had a reply on, Codex names that reply (previous_response_id) and
// sends only what came after it, where over HTTP it sends the whole
// conversation every turn, images and all (#4058, kevinzhow and
// neighbads on X). magpie answered the upgrade 426 (a87dc1ff2), which
// has Codex use HTTP for the session.
//
// Each response.create is served as the POST it stands for: the request
// the socket was opened with is made again, with the frame as its body,
// and sent through the whole handler the upgrade came in on (socketEntry):
// the allow list, the key and its limits, the Origin and Host checks,
// routing, failover, usage and the log judge every turn as they judge an
// HTTP one, and a key turned off ends the next turn, not the socket.
//
// The conversation a turn continues is the socket's own: each reply's
// input and output, kept for the socket's life (Codex says store false on
// every request, and OpenAI's socket keeps the last response for the
// connection all the same). A turn naming one is given the conversation
// whole before it is sent, so what serves it, a provider or another
// account, sees what it would over HTTP. An id the socket doesn't hold
// is answered previous_response_not_found, on which Codex opens a new
// socket and sends the conversation whole.

// socketOriginKey holds the request a socket was opened with.
type socketOriginKey struct{}

type socketOrigin struct {
	ctx context.Context // the request's own, before any guard's
	req *http.Request   // as it arrived: URL, Host, RemoteAddr, headers
	via http.Handler    // where it arrived, which each turn goes through
}

// wantsSocket: r asks for a WebSocket.
func wantsSocket(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// socketEntry is where a request reaches the gateway (Front, Handler): a
// socket's turns are sent through it again, as they arrived.
func socketEntry(next http.Handler) http.Handler {
	var self http.Handler
	self = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantsSocket(r) && r.Context().Value(socketOriginKey{}) == nil {
			o := &socketOrigin{ctx: r.Context(), req: r.Clone(r.Context()), via: self}
			r = r.WithContext(context.WithValue(r.Context(), socketOriginKey{}, o))
		}
		next.ServeHTTP(w, r)
	})
	return self
}

const (
	// socketKeep is how many replies a socket holds: Codex names only the
	// last, and a warmup's before its first turn
	socketKeep = 2
	// socketErrMax is the most of an error reply sent on
	socketErrMax = 1 << 20
)

// clientSockets are the sockets being served, which end with the gateway.
type clientSockets struct {
	mu      sync.Mutex
	m       map[*responsesSocket]bool
	closing bool
	done    chan struct{} // closed when the last one ends while closing
}

func (cs *clientSockets) add(s *responsesSocket) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.closing {
		return false
	}
	if cs.m == nil {
		cs.m = map[*responsesSocket]bool{}
	}
	cs.m[s] = true
	return true
}

func (cs *clientSockets) ending() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.closing
}

func (cs *clientSockets) remove(s *responsesSocket) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.m, s)
	if cs.closing && len(cs.m) == 0 && cs.done != nil {
		close(cs.done)
		cs.done = nil
	}
}

// drain ends every socket once its turn in flight is answered, and waits
// for them until ctx ends, when the rest are cut. Codex opens a new one
// for its next turn, to the gateway serving the port then, and sends the
// conversation whole on it.
func (cs *clientSockets) drain(ctx context.Context) {
	cs.mu.Lock()
	cs.closing = true
	var done chan struct{}
	if len(cs.m) > 0 {
		done = make(chan struct{})
		cs.done = done
	}
	all := make([]*responsesSocket, 0, len(cs.m))
	for s := range cs.m {
		all = append(all, s)
	}
	cs.mu.Unlock()
	for _, s := range all {
		s.goingAway()
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
		for _, s := range all {
			s.c.CloseNow()
		}
	}
}

// heldTurn is one reply a socket holds: the conversation up to and
// with it.
type heldTurn struct {
	id    string
	items []json.RawMessage
}

type responsesSocket struct {
	s    *Server
	o    *socketOrigin
	path string
	c    *websocket.Conn
	ctx  context.Context
	live context.Context // the socket's, for writes (send)

	writeMu sync.Mutex
	turns   []heldTurn // the newest last

	mu      sync.Mutex
	away    bool // the gateway is ending: close after this turn
	turning bool

	// for the log
	frames, continued int
	got, sent         int64
}

// serveResponsesSocket serves a WebSocket upgrade on a Responses route,
// and false when r didn't come through socketEntry.
func (s *Server) serveResponsesSocket(w http.ResponseWriter, r *http.Request) bool {
	o, _ := r.Context().Value(socketOriginKey{}).(*socketOrigin)
	if o == nil {
		return false
	}
	// a browser's page from another site is turned away (the Origin
	// check here is the library's: Origin's host must be the Host); the
	// guards the upgrade passed judged it already
	if s.sockets.ending() {
		http.Error(w, "magpie is restarting", http.StatusServiceUnavailable)
		return true
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover})
	if err != nil {
		log.Printf("responses socket from %s: %v", r.RemoteAddr, err)
		return true
	}
	// a turn on a new socket is the conversation whole (codexBodyLimit)
	most := int64(defaultBodyLimit)
	if strings.HasPrefix(r.URL.Path, CodexPath+"/") {
		most = s.codexLimitOf()
	}
	c.SetReadLimit(most)
	sock := &responsesSocket{s: s, o: o, path: r.URL.Path, c: c, ctx: o.ctx}
	if !s.sockets.add(sock) {
		c.Close(websocket.StatusGoingAway, "magpie is restarting")
		return true
	}
	defer s.sockets.remove(sock)
	sock.serve()
	if sock.frames > 0 {
		log.Printf("responses socket from %s ended: %d turns, %d continued, %d bytes received, %d sent on as whole requests",
			r.RemoteAddr, sock.frames, sock.continued, sock.got, sock.sent)
	}
	return true
}

func (rs *responsesSocket) goingAway() {
	rs.mu.Lock()
	rs.away = true
	idle := !rs.turning
	rs.mu.Unlock()
	if idle {
		// Close waits for the client's reply: not one socket after another
		go rs.c.Close(websocket.StatusGoingAway, "magpie is restarting")
	}
}

type socketMsg struct {
	data []byte
	err  error
}

func (rs *responsesSocket) serve() {
	ctx, cancel := context.WithCancel(rs.ctx)
	defer cancel()
	defer rs.c.CloseNow()
	rs.live = ctx
	in := make(chan socketMsg)
	go func() {
		for {
			typ, data, err := rs.c.Read(ctx)
			if err == nil && typ != websocket.MessageText {
				err = errors.New("binary frame")
				rs.c.Close(websocket.StatusUnsupportedData, "text frames only")
			}
			select {
			case in <- socketMsg{data, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var queued []socketMsg
	for {
		var m socketMsg
		if len(queued) > 0 {
			m, queued = queued[0], queued[1:]
		} else {
			select {
			case m = <-in:
			case <-ctx.Done():
				return
			}
		}
		if m.err != nil {
			return
		}
		kind := gjson.GetBytes(m.data, "type").String()
		switch kind {
		case "response.create":
		case "response.interrupt":
			// nothing in flight: it ended before the ask came
			rs.write(ctx, interruptReply("response.interrupt.failed", gjson.GetBytes(m.data, "response_id").String()))
			continue
		default:
			rs.writeError(ctx, http.StatusBadRequest, "invalid_request_error", "unsupported_message",
				fmt.Sprintf("magpie serves response.create on this socket, not %q", kind), "type")
			continue
		}
		rs.mu.Lock()
		rs.turning = true
		rs.mu.Unlock()
		turnCtx, stop := context.WithCancel(ctx)
		t := &socketTurnRun{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			rs.turn(turnCtx, t, m.data)
		}()
		for waiting := true; waiting; {
			select {
			case <-done:
				waiting = false
			case n := <-in:
				if n.err != nil {
					// the client went: what is in flight ends with it
					stop()
					<-done
					return
				}
				if gjson.GetBytes(n.data, "type").String() != "response.interrupt" {
					queued = append(queued, n)
					continue
				}
				id := gjson.GetBytes(n.data, "response_id").String()
				if !t.interrupt(id) {
					rs.write(ctx, interruptReply("response.interrupt.failed", id))
					continue
				}
				stop()
				<-done
				waiting = false
				rs.write(ctx, interruptReply("response.interrupt.accepted", id))
				// the reply ends as interrupted: Codex keeps none of it
				// (discard_partial_items) and waits for its end
				end, _ := json.Marshal(map[string]any{"type": "response.incomplete", "response": map[string]any{
					"id": id, "object": "response", "status": "incomplete",
					"incomplete_details": map[string]any{"reason": "interrupted"}, "output": []any{},
				}})
				rs.write(ctx, end)
			}
		}
		stop()
		rs.mu.Lock()
		rs.turning = false
		away := rs.away
		rs.mu.Unlock()
		if t.cut {
			// the reply ended before its end: as over HTTP, Codex asks
			// again, on a new socket, with the conversation whole
			rs.c.Close(websocket.StatusInternalError, "the reply ended before response.completed")
			return
		}
		if away {
			rs.c.Close(websocket.StatusGoingAway, "magpie is restarting")
			return
		}
	}
}

func interruptReply(kind, id string) []byte {
	b, _ := json.Marshal(map[string]string{"type": kind, "response_id": id})
	return b
}

// write sends frame unless ctx, a turn's, has ended (interrupted, or the
// client gone). The write itself is under the socket's context: one whose
// context ends mid-write closes the socket, which an interrupt mustn't.
func (rs *responsesSocket) write(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rs.writeMu.Lock()
	defer rs.writeMu.Unlock()
	return rs.c.Write(rs.live, websocket.MessageText, frame)
}

// writeError is an error frame as Codex reads one: the HTTP status and
// the error object an HTTP reply would have carried.
func (rs *responsesSocket) writeError(ctx context.Context, status int, typ, code, msg, param string) error {
	e := map[string]any{"type": typ, "code": code, "message": msg}
	if param != "" {
		e["param"] = param
	}
	b, _ := json.Marshal(map[string]any{"type": "error", "status": status, "error": e})
	return rs.write(ctx, b)
}

// socketTurnRun is what a turn in flight has said so far.
type socketTurnRun struct {
	mu        sync.Mutex
	ids       []string // the response ids it gave
	cut       bool     // its reply ended before its end
	cancelled bool
}

func (t *socketTurnRun) interrupt(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, have := range t.ids {
		if have == id {
			t.cancelled = true
			return true
		}
	}
	return false
}

// held is the conversation a reply the socket holds ends.
func (rs *responsesSocket) held(id string) ([]json.RawMessage, bool) {
	for i := len(rs.turns) - 1; i >= 0; i-- {
		if rs.turns[i].id == id {
			return rs.turns[i].items, true
		}
	}
	return nil, false
}

// keep holds the reply id ends: the conversation before this turn (held
// already), the turn's own input and the reply's items. The input is
// copied out of the frame it was read from: a view kept it whole, with
// Codex's tools and instructions, on every turn a socket held, ~39 MB of
// a long session's live heap.
func (rs *responsesSocket) keep(id string, prior, fresh, reply []json.RawMessage) {
	t := heldTurn{id: id, items: make([]json.RawMessage, 0, len(prior)+len(fresh)+len(reply))}
	t.items = append(t.items, prior...)
	t.items = append(t.items, ownItems(fresh)...)
	t.items = append(t.items, reply...)
	rs.turns = append(rs.turns, t)
	if len(rs.turns) > socketKeep {
		rs.turns = append(rs.turns[:0:0], rs.turns[len(rs.turns)-socketKeep:]...)
	}
}

// ownItems are items copied into one buffer of their own.
func ownItems(items []json.RawMessage) []json.RawMessage {
	n := 0
	for _, it := range items {
		n += len(it)
	}
	buf := make([]byte, 0, n)
	out := make([]json.RawMessage, len(items))
	for i, it := range items {
		buf = append(buf, it...)
		out[i] = buf[len(buf)-len(it) : len(buf) : len(buf)]
	}
	return out
}

// turn serves one response.create.
func (rs *responsesSocket) turn(ctx context.Context, t *socketTurnRun, frame []byte) {
	rs.frames++
	rs.got += int64(len(frame))
	var q map[string]json.RawMessage
	if decodeJSONView(frame, &q) != nil {
		rs.writeError(ctx, http.StatusBadRequest, "invalid_request_error", "invalid_json", "response.create is not a JSON object", "")
		return
	}
	_, fresh := prevInput(q["input"])
	if fresh == nil {
		rs.writeError(ctx, http.StatusBadRequest, "invalid_request_error", "invalid_value", "input is neither text nor a list of items", "input")
		return
	}
	input := fresh
	var held []json.RawMessage
	if raw := q["previous_response_id"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var id string
		json.Unmarshal(raw, &id)
		prior, ok := rs.held(id)
		if !ok {
			// Codex opens a new socket and sends the conversation whole
			rs.writeError(ctx, http.StatusBadRequest, "invalid_request_error", "previous_response_not_found",
				"Previous response with id '"+id+"' not found.", "previous_response_id")
			return
		}
		held, input = prior, append(prior[:len(prior):len(prior)], fresh...)
		rs.continued++
	}
	// a warmup (generate false) asks for no reply: Codex sends it with
	// the conversation ahead of its first turn, and that turn follows it
	var generate bool
	if raw := q["generate"]; len(raw) > 0 && json.Unmarshal(raw, &generate) == nil && !generate {
		id := "resp_" + newID()
		t.mu.Lock()
		t.ids = append(t.ids, id)
		t.mu.Unlock()
		resp := map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": "in_progress", "output": []any{}}
		if m := q["model"]; len(m) > 0 {
			resp["model"] = m
		}
		b, _ := json.Marshal(map[string]any{"type": "response.created", "response": resp})
		if rs.write(ctx, b) != nil {
			return
		}
		resp["status"] = "completed"
		resp["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
		b, _ = json.Marshal(map[string]any{"type": "response.completed", "response": resp})
		if rs.write(ctx, b) == nil {
			rs.keep(id, held, fresh, nil)
		}
		return
	}
	// the body the HTTP route would have had
	delete(q, "type")
	delete(q, "previous_response_id")
	delete(q, "generate")
	body, err := turnBody(q, input)
	if err != nil {
		rs.writeError(ctx, http.StatusBadRequest, "invalid_request_error", "invalid_json", err.Error(), "")
		return
	}
	rs.sent += int64(len(body))
	if socketTurnBody != nil {
		socketTurnBody(body)
	}
	req := rs.request(ctx, body)
	out := &socketTurnWriter{rs: rs, ctx: ctx, run: t, h: http.Header{}}
	rs.o.via.ServeHTTP(out, req)
	out.finish()
	if ctx.Err() != nil {
		return
	}
	if !out.ended {
		t.cut = true
		return
	}
	if out.id != "" && out.completed {
		rs.keep(out.id, held, fresh, out.items)
	}
}

// socketTurnBody, set by the tests, is handed each body a turn is served
// with, to see that nothing keeps it once the turn is over.
var socketTurnBody func(body []byte)

// turnBody is json.Marshal(q) with input, as json.Marshal writes it, for
// q's input, written once into a slice of about its size. A turn wrote
// its conversation with json.Marshal twice, the list and then q around
// it, through encoding/json's scanner a byte at a time and a buffer that
// doubles and is copied out: about five copies of a long conversation on
// every turn, the garbage that sets the peak (marshalRaw has the rest).
func turnBody(q map[string]json.RawMessage, input []json.RawMessage) ([]byte, error) {
	q["input"] = nil
	n := 2
	keys := make([]string, 0, len(q))
	for k, raw := range q {
		n += len(k) + escapedSize(raw) + 4
		keys = append(keys, k)
	}
	for _, raw := range input {
		n += escapedSize(raw) + 1
	}
	slices.Sort(keys)
	dst := make([]byte, 0, n)
	dst = append(dst, '{')
	ok := true
	for i, k := range keys {
		if i > 0 {
			dst = append(dst, ',')
		}
		if dst, ok = appendJSONKey(dst, k, true); !ok {
			break
		}
		dst = append(dst, ':')
		if k != "input" {
			if dst, ok = appendRawJSON(dst, q[k], true); !ok {
				break
			}
			continue
		}
		if input == nil {
			dst = append(dst, "null"...)
			continue
		}
		dst = append(dst, '[')
		for j, raw := range input {
			if j > 0 {
				dst = append(dst, ',')
			}
			if dst, ok = appendRawJSON(dst, raw, true); !ok {
				break
			}
		}
		if !ok {
			break
		}
		dst = append(dst, ']')
	}
	if ok {
		return append(dst, '}'), nil
	}
	// a value encoding/json refuses: it says why
	in, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	q["input"] = in
	return json.Marshal(q)
}

// escapedSize is about what appendRawJSON writes of raw: its length, and
// the five bytes each <, > and & grows by as \u003c and the like. Code
// is full of them, and a slice that grows doubles.
func escapedSize(raw []byte) int {
	return len(raw) + 5*(bytes.Count(raw, []byte("<"))+bytes.Count(raw, []byte(">"))+bytes.Count(raw, []byte("&")))
}

// request is the POST a turn stands for: the upgrade's own request, its
// URL, Host, address and headers, with the turn as its body.
func (rs *responsesSocket) request(ctx context.Context, body []byte) *http.Request {
	o := rs.o.req
	req := o.Clone(ctx)
	req.Method = http.MethodPost
	req.Header = http.Header{}
	for k, vs := range o.Header {
		ck := http.CanonicalHeaderKey(k)
		if hopHeader(ck) || strings.HasPrefix(ck, "Sec-Websocket-") {
			continue
		}
		switch ck {
		case "Content-Type", "Content-Encoding", "Accept", "Accept-Encoding":
			continue
		case "Openai-Beta":
			// the socket's own beta (responses_websockets=…) isn't the turn's
			var keep []string
			for _, v := range vs {
				if !strings.Contains(v, "responses_websockets") {
					keep = append(keep, v)
				}
			}
			if len(keep) > 0 {
				req.Header[ck] = keep
			}
			continue
		}
		req.Header[ck] = append([]string(nil), vs...)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// what Codex says in client_metadata here and in a header over HTTP
	// (codexSocketMessage, the other way)
	if v := gjson.GetBytes(body, `client_metadata.x-codex-turn-state`).String(); v != "" {
		req.Header.Set("X-Codex-Turn-State", v)
	}
	if strings.EqualFold(gjson.GetBytes(body, `client_metadata.ws_request_header_x_openai_internal_codex_responses_lite`).String(), "true") {
		req.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	req.RequestURI = ""
	return req
}

// socketTurnWriter takes the HTTP reply a turn is given and sends it on
// as the socket's frames: each event of a stream as one, an error as an
// error frame.
type socketTurnWriter struct {
	rs  *responsesSocket
	ctx context.Context
	run *socketTurnRun
	h   http.Header

	status int
	stream bool
	pw     *io.PipeWriter
	read   chan struct{}
	buf    bytes.Buffer // a reply that isn't a stream

	// what the reply said
	id               string
	items            []json.RawMessage
	ended, completed bool
	wroteErr         bool
}

func (w *socketTurnWriter) Header() http.Header { return w.h }

func (w *socketTurnWriter) WriteHeader(code int) {
	if w.status != 0 || code < 200 {
		return
	}
	w.status = code
	ct := w.h.Get("Content-Type")
	if code/100 == 2 && strings.Contains(ct, "event-stream") {
		w.startStream()
	}
}

func (w *socketTurnWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		if w.h.Get("Content-Type") == "" && (bytes.HasPrefix(b, []byte("event:")) || bytes.HasPrefix(b, []byte("data:"))) {
			w.h.Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.stream {
		return w.pw.Write(b)
	}
	if w.buf.Len() < socketErrMax || w.status/100 == 2 {
		w.buf.Write(b)
	}
	return len(b), nil
}

func (w *socketTurnWriter) Flush() {}

func (w *socketTurnWriter) startStream() {
	w.stream = true
	for _, f := range w.headerFrames() {
		w.send(f)
	}
	pr, pw := io.Pipe()
	w.pw, w.read = pw, make(chan struct{})
	go func() {
		defer close(w.read)
		err := readSSE(pr, func(_, data string) error {
			data = strings.TrimSpace(data)
			if data == "" || data == "[DONE]" {
				return nil
			}
			return w.event([]byte(data))
		})
		pr.CloseWithError(err)
		if err == nil {
			io.Copy(io.Discard, pr)
		}
	}()
}

// headerFrames say what the HTTP route says in the reply's headers, as
// Codex reads it on a socket: the turn state and the model in
// response.metadata, the model list's tag in codex.response.metadata,
// and the account's windows in codex.rate_limits.
func (w *socketTurnWriter) headerFrames() [][]byte {
	var out [][]byte
	meta := map[string]string{}
	for _, k := range []string{"x-codex-turn-state", "openai-model", "x-reasoning-included"} {
		if v := w.h.Get(k); v != "" {
			meta[k] = v
		}
	}
	if len(meta) > 0 {
		b, _ := json.Marshal(map[string]any{"type": "response.metadata", "headers": meta})
		out = append(out, b)
	}
	if v := w.h.Get("X-Models-Etag"); v != "" {
		b, _ := json.Marshal(map[string]any{"type": "codex.response.metadata", "headers": map[string]string{"x-models-etag": v}})
		out = append(out, b)
	}
	if f := codexLimitsFrame(w.h); f != nil {
		out = append(out, f)
	}
	return out
}

// codexLimitsFrame is the codex.rate_limits event the x-codex-* headers
// of a reply say (codexLimitHeaders, the other way), nil for none.
func codexLimitsFrame(h http.Header) []byte {
	windows := map[string]any{}
	for _, which := range []string{"primary", "secondary"} {
		used := h.Get("x-codex-" + which + "-used-percent")
		p, err := strconv.ParseFloat(used, 64)
		if err != nil {
			continue
		}
		w := map[string]any{"used_percent": p}
		if v, err := strconv.ParseInt(h.Get("x-codex-"+which+"-window-minutes"), 10, 64); err == nil {
			w["window_minutes"] = v
		}
		if v, err := strconv.ParseInt(h.Get("x-codex-"+which+"-reset-at"), 10, 64); err == nil {
			w["reset_at"] = v
		}
		windows[which] = w
	}
	if len(windows) == 0 {
		return nil
	}
	ev := map[string]any{"type": "codex.rate_limits", "rate_limits": windows}
	if p := h.Get("x-codex-plan-type"); p != "" {
		ev["plan_type"] = p
	}
	has, unl := h.Get("x-codex-credits-has-credits"), h.Get("x-codex-credits-unlimited")
	if has != "" && unl != "" {
		cr := map[string]any{"has_credits": strings.EqualFold(has, "true"), "unlimited": strings.EqualFold(unl, "true")}
		if b := h.Get("x-codex-credits-balance"); b != "" {
			cr["balance"] = b
		}
		ev["credits"] = cr
	}
	b, _ := json.Marshal(ev)
	return b
}

// event is one event of the reply's stream.
func (w *socketTurnWriter) event(data []byte) error {
	var e struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
		Item     json.RawMessage `json:"item"`
	}
	if json.Unmarshal(data, &e) != nil {
		return nil // not JSON: Codex would skip it too
	}
	switch e.Type {
	case "response.created", "response.in_progress":
		w.note(gjson.GetBytes(e.Response, "id").String())
	case "response.output_item.done":
		// in the order they came, as Codex adds them to its side
		if len(e.Item) > 0 {
			w.items = append(w.items, append(json.RawMessage(nil), e.Item...))
		}
	case "response.completed":
		w.note(gjson.GetBytes(e.Response, "id").String())
		w.ended, w.completed = true, true
	case "response.incomplete", "response.failed", "response.cancelled", "error":
		w.ended = true
	case "codex.response.metadata":
		// the model list's tag as magpie gives it (modelsEtag), which the
		// reply's header has
		if v := w.h.Get("X-Models-Etag"); v != "" && gjson.GetBytes(data, "headers.x-models-etag").Exists() {
			if b, ok := withModelsEtag(data, v); ok {
				data = b
			}
		}
	}
	return w.send(data)
}

func (w *socketTurnWriter) note(id string) {
	if id == "" {
		return
	}
	w.id = id
	w.run.mu.Lock()
	for _, have := range w.run.ids {
		if have == id {
			w.run.mu.Unlock()
			return
		}
	}
	w.run.ids = append(w.run.ids, id)
	w.run.mu.Unlock()
}

func (w *socketTurnWriter) send(frame []byte) error { return w.rs.write(w.ctx, frame) }

// finish is the end of the handler's reply.
func (w *socketTurnWriter) finish() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.stream {
		w.pw.Close()
		<-w.read
		return
	}
	if w.ctx.Err() != nil {
		return
	}
	if w.status/100 != 2 {
		w.sendHTTPError()
		return
	}
	// a whole reply: what its stream would have said
	body := bytes.TrimSpace(w.buf.Bytes())
	r := gjson.ParseBytes(body)
	if r.Get("object").String() != "response" || r.Get("id").String() == "" {
		w.status = http.StatusBadGateway
		w.buf.Reset()
		w.buf.WriteString(`{"error":{"type":"server_error","message":"the reply was neither a stream nor a response"}}`)
		w.sendHTTPError()
		return
	}
	for _, f := range w.headerFrames() {
		w.send(f)
	}
	w.event([]byte(`{"type":"response.created","response":` + string(body) + `}`))
	for i, it := range r.Get("output").Array() {
		ev, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": i, "item": json.RawMessage(it.Raw)})
		w.event(ev)
	}
	kind := "response.completed"
	switch r.Get("status").String() {
	case "incomplete":
		kind = "response.incomplete"
	case "failed":
		kind = "response.failed"
	}
	w.event([]byte(`{"type":"` + kind + `","response":` + string(body) + `}`))
}

// sendHTTPError is an error reply as Codex reads it on a socket: the
// status, the error object and the headers it acts on (Retry-After, the
// account's x-codex-* windows).
func (w *socketTurnWriter) sendHTTPError() {
	w.ended = true
	body := bytes.TrimSpace(w.buf.Bytes())
	var e json.RawMessage
	if v := gjson.GetBytes(body, "error"); v.IsObject() {
		e = json.RawMessage(v.Raw)
	} else {
		msg := string(body)
		if !utf8.Valid(body) || msg == "" {
			msg = http.StatusText(w.status)
		}
		e, _ = json.Marshal(map[string]string{"type": "server_error", "message": msg})
	}
	headers := map[string]string{}
	for k := range w.h {
		lk := strings.ToLower(k)
		if lk == "retry-after" || lk == "retry-after-ms" || strings.HasPrefix(lk, "x-codex-") ||
			strings.HasPrefix(lk, "x-ratelimit-") || lk == "x-request-id" || lk == "x-error-json" {
			headers[lk] = w.h.Get(k)
		}
	}
	frame := map[string]any{"type": "error", "status": w.status, "error": e}
	if len(headers) > 0 {
		frame["headers"] = headers
	}
	b, _ := json.Marshal(frame)
	w.send(b)
}

// withModelsEtag is a codex.response.metadata event with its
// x-models-etag set to v, every other member as it was.
func withModelsEtag(data []byte, v string) ([]byte, bool) {
	var top map[string]json.RawMessage
	var h map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil || json.Unmarshal(top["headers"], &h) != nil {
		return nil, false
	}
	h["x-models-etag"], _ = json.Marshal(v)
	top["headers"], _ = json.Marshal(h)
	b, err := json.Marshal(top)
	return b, err == nil
}
