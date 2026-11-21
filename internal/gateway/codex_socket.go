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
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A streamed turn to the ChatGPT backend (CodexBase/responses) goes over
// the backend's WebSocket, as Codex's own client sends it, and comes back
// to magpie's caller as the server-sent events the HTTP route answers with.
// On some networks a streamed HTTP reply from chatgpt.com is cut at about
// 120 s, before response.completed, while the same turn over the WebSocket
// runs to its end (brickhao on Discord: 120.7 s through magpie, 121.5 s
// over HTTP without it, both cut; 124.5 s and 131.4 s over the WebSocket,
// both whole). Codex itself still speaks HTTP to magpie, over loopback:
// it is the leg from magpie to the vendor that was cut.
//
// What goes on the socket is what Codex 0.16x sends there (codex-rs
// ResponseCreateWsRequest, build_websocket_headers): the handshake carries
// the turn's headers with OpenAI-Beta: responses_websockets=2026-02-06 and
// neither x-codex-turn-state nor the responses-lite header, which go in
// the message's client_metadata instead; the message is the HTTP body with
// "type":"response.create" ahead of it. Every event comes back a frame;
// codex.rate_limits and codex.response.metadata, which the HTTP route says
// in headers, are told in headers too (x-codex-*, x-codex-turn-state).
//
// Anything the socket can't do falls back to the HTTP request as it was:
// a refused handshake (and that host is spoken HTTP for codexSocketShun),
// a connection limit, a body that isn't a streamed turn, and a socket
// that ends, or won't take the message, before any of the reply was
// handed on. That last one is Lamrfu's (Discord): chatgpt.com closed the
// socket 1009 (message too big) on a long conversation's turn, and the
// turn was a 502. Codex's own client sends a turn on a kept socket as a
// delta after previous_response_id, so its messages stay small; magpie
// does too (codex_socket_keep.go). A message as big as one refused 1009
// goes straight over HTTP to that host for codexSocketShun (codexTooBig).

// codexSocketOff turns the socket off: the gateway's tests' fakes of the
// ChatGPT backend speak HTTP, so TestMain sets it, and codex_socket_test
// clears it.
var codexSocketOff atomic.Bool

// codexSocketBeta is the OpenAI-Beta Codex's WebSocket handshake carries.
const codexSocketBeta = "responses_websockets=2026-02-06"

// codexSocketShun is how long a host that refused the handshake is
// spoken HTTP before it is tried again.
var codexSocketShun = 10 * time.Minute

// codexRelayShun is how long a relay (provider.SocketRelay) that refused
// the socket is spoken HTTP: most relays never take it (tried 11-21:
// DeepSeek 405, OpenRouter, AiHubMix and OpenCode Go 404), and asking
// every ten minutes cost each a refused handshake ahead of a turn. A
// passing fault (429, 5xx) is still codexSocketShun's.
var codexRelayShun = 6 * time.Hour

// codexDeltaLapse is how long a relay that answered a delta not as one
// (codexDeltaIgnored) is sent each turn whole, on its socket still.
var codexDeltaLapse = 24 * time.Hour

// codexSocketDial bounds the handshake, as Codex's own
// websocket_connect_timeout does.
var codexSocketDial = 30 * time.Second

// codexSocketPrelude is how many frames ahead of the reply proper are read
// for headers before the reply is handed on.
const codexSocketPrelude = 8

var (
	codexShunMu sync.Mutex
	codexShun   = map[string]time.Time{}
	// codexTooBig is the smallest message a host closed 1009 on, and
	// until when messages that big go to it over HTTP; under codexShunMu.
	codexTooBig = map[string]tooBig{}
	// codexNoDelta is until when a relay's turns go whole
	// (codexDeltaIgnored); under codexShunMu.
	codexNoDelta = map[string]time.Time{}
)

// codexDeltaIgnored notes that host answered a turn sent after
// previous_response_id without saying it followed that response, as a
// relay that drops the field would; its turns go whole for
// codexDeltaLapse.
func codexDeltaIgnored(host string) {
	codexShunMu.Lock()
	_, was := codexNoDelta[host]
	codexNoDelta[host] = time.Now().Add(codexDeltaLapse)
	codexShunMu.Unlock()
	if !was {
		log.Printf("codex: %s answered a turn after previous_response_id without naming it; its turns go whole for %s", host, codexDeltaLapse)
	}
}

func codexDeltaLapsed(host string) bool {
	codexShunMu.Lock()
	defer codexShunMu.Unlock()
	until, ok := codexNoDelta[host]
	if ok && time.Now().After(until) {
		delete(codexNoDelta, host)
		return false
	}
	return ok
}

type tooBig struct {
	size  int
	until time.Time
}

// codexMessageTooBig says whether a message of size bytes is as big as
// one host refused, and so goes over HTTP.
func codexMessageTooBig(host string, size int) bool {
	codexShunMu.Lock()
	defer codexShunMu.Unlock()
	tb, ok := codexTooBig[host]
	if ok && time.Now().After(tb.until) {
		delete(codexTooBig, host)
		return false
	}
	return ok && size >= tb.size
}

func refusedTooBig(host string, size int) {
	codexShunMu.Lock()
	tb, was := codexTooBig[host]
	if !was || size < tb.size || time.Now().After(tb.until) {
		tb.size = size
	}
	tb.until = time.Now().Add(codexSocketShun)
	codexTooBig[host] = tb
	codexShunMu.Unlock()
	if !was {
		log.Printf("codex: %s closed the WebSocket on a %d-byte turn as too big; turns that big go over HTTP for %s", host, size, codexSocketShun)
	}
}

func codexShunned(host string) bool {
	codexShunMu.Lock()
	defer codexShunMu.Unlock()
	until, ok := codexShun[host]
	if ok && time.Now().After(until) {
		delete(codexShun, host)
		return false
	}
	return ok
}

func shunCodexSocket(host string, why string, d time.Duration) {
	codexShunMu.Lock()
	until, was := codexShun[host]
	if t := time.Now().Add(d); !was || t.After(until) {
		codexShun[host] = t
	}
	codexShunMu.Unlock()
	if !was {
		log.Printf("codex: %s refused the WebSocket (%s); its turns go over HTTP for %s", host, why, d)
	}
}

// codexSocket sends a streamed turn to the ChatGPT backend over its
// WebSocket, and one to a Responses provider whose Socket is set over
// the same WebSocket at its Responses URL (#5781); every other request
// goes through next.
type codexSocket struct{ next http.RoundTripper }

func (t codexSocket) RoundTrip(req *http.Request) (*http.Response, error) {
	if codexSocketOff.Load() || req.Method != http.MethodPost || req.GetBody == nil {
		return t.next.RoundTrip(req)
	}
	turn, shun := provider.SocketTurn(req)
	if !turn || (shun && codexShunned(req.URL.Host)) {
		return t.next.RoundTrip(req)
	}
	res, err := t.overSocket(req, shun, shun && provider.SocketRelay(req))
	if res != nil && res.StatusCode == http.StatusOK && res.Header.Get("Content-Type") == "" &&
		!provider.CodexTurnURL(req.URL) {
		// what a Responses provider's stream is said to be over HTTP;
		// chatgpt.com's turns go on as they always have
		res.Header.Set("Content-Type", "text/event-stream")
	}
	return res, err
}

// overSocket is RoundTrip for a turn that may go over the socket; shun
// says a refused handshake leaves the host to HTTP for a while, and relay
// that it is a relay's (provider.SocketRelay).
func (t codexSocket) overSocket(req *http.Request, shun, relay bool) (*http.Response, error) {
	overHTTP := false
	toSocket(req.Context())
	defer func() { sentOnSocket(req.Context(), !overHTTP) }()
	viaHTTP := func() (*http.Response, error) {
		if !overHTTP {
			toHTTP(req.Context())
		}
		overHTTP = true
		return t.next.RoundTrip(req)
	}
	body, ok := codexSocketMessage(req)
	copilot := provider.CopilotSocketTurn(req)
	if !ok || (copilot && !provider.CopilotSocketModel(gjson.GetBytes(body, "model").String())) {
		return viaHTTP()
	}
	create := codexCreate
	if copilot {
		initiator := "agent"
		if strings.EqualFold(req.Header.Get("X-Initiator"), "user") {
			initiator = "user"
		}
		create = func(body []byte) []byte { return copilotCreate(body, initiator) }
	}
	msg := create(body)
	var shape *codexTurnShape
	key := codexKeepKey(req, body)
	if key == "" && copilot {
		// Copilot is sent no conversation's id: the conversation is how
		// it opens, as a delta goes only after the turn it carries on
		key = codexKeepKeyOf(req, conversationID(req.Header, body), "")
	}
	if key != "" {
		if s, ok := codexShape(body); ok {
			shape = s
		} else {
			key = ""
		}
	}
	if k := codexKeepTake(key); k != nil {
		send, after := msg, ""
		if relay && codexDeltaLapsed(req.URL.Host) {
			// it answered a delta not as one lately: whole on its socket
		} else if d, ok := k.delta(body, shape); ok {
			send = create(d)
			if relay {
				after = k.replyID
			}
		}
		if codexMessageTooBig(req.URL.Host, len(send)) {
			k.close()
		} else {
			res, err := t.turn(req, k, send, shape, true, after)
			if !errors.Is(err, errCodexSocketAgain) {
				return res, err
			}
		}
	}
	if codexMessageTooBig(req.URL.Host, len(msg)) {
		return viaHTTP()
	}
	res, err := t.socket(req, msg, key, shape, shun, relay, copilot)
	if errors.Is(err, errCodexSocketRelayError) {
		// a relay's socket answered a new turn with an error: HTTP
		// answers it, and a relay whose HTTP takes the turn it refused
		// is one whose socket isn't to be asked
		res, err := viaHTTP()
		if err == nil && res.StatusCode < 400 {
			shunCodexSocket(req.URL.Host, "an error frame its HTTP route answered", codexRelayShun)
		}
		return res, err
	}
	if errors.Is(err, errCodexSocketFallBack) {
		return viaHTTP()
	}
	return res, err
}

var (
	// errCodexSocketFallBack says the turn goes over HTTP after all.
	errCodexSocketFallBack = errors.New("codex: over HTTP")
	// errCodexSocketAgain says a kept socket failed before the reply was
	// handed on, and the turn goes whole on a new one.
	errCodexSocketAgain = errors.New("codex: on a new socket")
	// errCodexSocketRelayError says a relay's new socket answered with
	// an error frame ahead of the reply: the turn goes over HTTP.
	errCodexSocketRelayError = errors.New("codex: a relay's error over HTTP")
)

// codexSocketMessage is req's turn as the body of the socket's
// response.create message, and false for a body that isn't a streamed
// turn.
func codexSocketMessage(req *http.Request) ([]byte, bool) {
	rc, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	plain := rc
	if enc := req.Header.Get("Content-Encoding"); enc != "" {
		if !strings.EqualFold(enc, "zstd") {
			return nil, false
		}
		d := &http.Request{Header: http.Header{"Content-Encoding": {enc}}, Body: rc}
		if plain, err = decodedBody(d); err != nil {
			return nil, false
		}
		defer plain.Close()
	}
	body, err := io.ReadAll(io.LimitReader(plain, defaultBodyLimit+1))
	if err != nil || len(body) > defaultBodyLimit {
		return nil, false
	}
	body = bytes.TrimSpace(body)
	if len(body) < 2 || body[0] != '{' || !gjson.ValidBytes(body) {
		return nil, false
	}
	if !gjson.GetBytes(body, "stream").Bool() || gjson.GetBytes(body, "type").Exists() {
		return nil, false
	}
	// what Codex says in a header over HTTP and in client_metadata here
	meta := map[string]string{}
	if v := req.Header.Get("X-Codex-Turn-State"); v != "" {
		meta["x-codex-turn-state"] = v
	}
	if strings.EqualFold(req.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite"), "true") {
		meta["ws_request_header_x_openai_internal_codex_responses_lite"] = "true"
	}
	if len(meta) > 0 {
		return withClientMetadata(body, meta)
	}
	return body, true
}

// codexCreate is body, a JSON object, as a response.create message.
func codexCreate(body []byte) []byte {
	rest := bytes.TrimSpace(body[1:])
	out := make([]byte, 0, len(body)+32)
	out = append(out, `{"type":"response.create"`...)
	if len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	return append(out, rest...)
}

// copilotCreate is body, a JSON object, as Copilot's response.create
// message: without stream, and with whether the user or the agent asked
// the turn (initiator, said in X-Initiator over HTTP), as Copilot Chat
// sends it (chatWebSocketManager.ts sendRequest).
func copilotCreate(body []byte, initiator string) []byte {
	who, _ := json.Marshal(initiator)
	out := make([]byte, 0, len(body)+48)
	out = append(out, `{"type":"response.create","initiator":`...)
	out = append(out, who...)
	gjson.ParseBytes(body).ForEach(func(k, v gjson.Result) bool {
		switch k.String() {
		case "stream", "type", "initiator":
			return true
		}
		out = append(out, ',')
		out = append(out, k.Raw...)
		out = append(out, ':')
		out = append(out, v.Raw...)
		return true
	})
	return append(out, '}')
}

// withoutClientMetadata is body with key gone from its client_metadata,
// every other byte as it was, and body itself when it has no such key.
func withoutClientMetadata(body []byte, key string) []byte {
	cm := gjson.GetBytes(body, "client_metadata")
	if !cm.IsObject() || cm.Index <= 0 || !cm.Get(gjson.Escape(key)).Exists() {
		return body
	}
	have := map[string]any{}
	if json.Unmarshal([]byte(cm.Raw), &have) != nil {
		return body
	}
	delete(have, key)
	raw, err := json.Marshal(have)
	if err != nil {
		return body
	}
	out := make([]byte, 0, len(body))
	out = append(out, body[:cm.Index]...)
	out = append(out, raw...)
	return append(out, body[cm.Index+len(cm.Raw):]...)
}

// withClientMetadata is body with meta's keys set in its client_metadata,
// every other byte as it was.
func withClientMetadata(body []byte, meta map[string]string) ([]byte, bool) {
	cm := gjson.GetBytes(body, "client_metadata")
	have := map[string]any{}
	if cm.Exists() && cm.Type != gjson.Null {
		if !cm.IsObject() || json.Unmarshal([]byte(cm.Raw), &have) != nil {
			return nil, false
		}
	}
	for k, v := range meta {
		if _, ok := have[k]; !ok {
			have[k] = v
		}
	}
	raw, err := json.Marshal(have)
	if err != nil {
		return nil, false
	}
	if cm.Exists() && cm.Index > 0 {
		out := make([]byte, 0, len(body)+len(raw))
		out = append(out, body[:cm.Index]...)
		out = append(out, raw...)
		return append(out, body[cm.Index+len(cm.Raw):]...), true
	}
	end := bytes.LastIndexByte(body, '}')
	out := make([]byte, 0, len(body)+len(raw)+20)
	out = append(out, bytes.TrimRight(body[:end], " \t\r\n")...)
	if out[len(out)-1] != '{' {
		out = append(out, ',')
	}
	out = append(out, `"client_metadata":`...)
	out = append(out, raw...)
	return append(out, '}'), true
}

// codexSocketHeaders are req's headers as the handshake carries them.
// Copilot's carries no OpenAI-Beta, as Copilot Chat's
// (createResponsesWebSocket in @vscode/copilot-api), and no X-Initiator,
// which each turn's message says.
func codexSocketHeaders(req *http.Request, copilot bool) http.Header {
	h := http.Header{}
	for k, vs := range req.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Type", "Content-Length", "Content-Encoding", "Accept", "Accept-Encoding",
			"X-Idempotency-Key", "Idempotency-Key", "X-Codex-Turn-State",
			"X-Openai-Internal-Codex-Responses-Lite", "Host", "Openai-Beta":
			continue
		}
		if hopHeader(k) || strings.HasPrefix(http.CanonicalHeaderKey(k), "Sec-Websocket-") {
			continue
		}
		if copilot && http.CanonicalHeaderKey(k) == "X-Initiator" {
			continue
		}
		h[k] = vs
	}
	if !copilot {
		h.Set("OpenAI-Beta", codexSocketBeta)
	}
	return h
}

// socket opens a socket for the turn and sends msg on it; one that
// carries on with key's conversation is kept for its next turn. A host
// that refuses the handshake is left to HTTP for a while when shun says
// so (a provider's Socket "on" never is).
func (t codexSocket) socket(req *http.Request, msg []byte, key string, shape *codexTurnShape, shun, relay, copilot bool) (*http.Response, error) {
	// the socket may outlive the request, kept for the next turn: only
	// the handshake ends with it
	ctx, cancel := context.WithCancel(context.WithoutCancel(req.Context()))
	stop := context.AfterFunc(req.Context(), cancel)
	timer := time.AfterFunc(codexSocketDial, cancel)
	u := *req.URL
	c, hs, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient:      &http.Client{Transport: t.next, CheckRedirect: provider.KeepsCredentials},
		HTTPHeader:      codexSocketHeaders(req, copilot),
		CompressionMode: websocket.CompressionNoContextTakeover,
	})
	stopped := timer.Stop()
	going := stop()
	if err != nil {
		cancel()
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		switch {
		case !shun:
			// tried again next turn
		case hs == nil && relay:
			// a relay that never answers the handshake costs a turn
			// codexSocketDial each time it is asked
			shunCodexSocket(u.Host, err.Error(), codexRelayShun)
		case hs == nil:
			shunCodexSocket(u.Host, err.Error(), codexSocketShun)
		case relay && (hs.StatusCode == http.StatusTooManyRequests || hs.StatusCode >= 500):
			shunCodexSocket(u.Host, hs.Status, codexSocketShun)
		case relay:
			// a 401 too: a relay that answers HTTP with the same key
			// may answer the socket's path with anything
			shunCodexSocket(u.Host, hs.Status, codexRelayShun)
		case hs.StatusCode == http.StatusUnauthorized || hs.StatusCode == http.StatusTooManyRequests ||
			hs.StatusCode >= 500:
			// the account's or a passing fault: HTTP says it as it is
		default:
			shunCodexSocket(u.Host, hs.Status, codexSocketShun)
		}
		return nil, errCodexSocketFallBack
	}
	if !stopped || !going {
		c.CloseNow()
		cancel()
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, errCodexSocketFallBack
	}
	c.SetReadLimit(-1)
	header := http.Header{}
	for k, vs := range hs.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Upgrade", "Content-Length", "Content-Type":
			continue
		}
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "Sec-Websocket-") {
			continue
		}
		header[k] = vs
	}
	k := &codexKept{c: c, cancel: cancel, key: key, host: u.Host, header: header, opened: time.Now(), copilot: copilot, relay: relay}
	return t.turn(req, k, msg, shape, false, "")
}

// turn sends msg on k and reads the frames ahead of the reply proper. On
// a kept socket (reused), whatever fails before the reply is handed on
// is errCodexSocketAgain. after is the response a relay's delta was sent
// after: its reply is handed on only once response.created names it
// (provider.SocketRelay); one that doesn't is sent again whole.
func (t codexSocket) turn(req *http.Request, k *codexKept, msg []byte, shape *codexTurnShape, reused bool, after string) (*http.Response, error) {
	ended := func(err error) error {
		if req.Context().Err() != nil {
			return req.Context().Err()
		}
		err = codexSocketEnded(k.host, len(msg), err)
		if reused {
			return errCodexSocketAgain
		}
		return err
	}
	trace := socketTrace(req, reused)
	if err := k.c.Write(req.Context(), websocket.MessageText, msg); err != nil {
		k.close()
		return nil, ended(err)
	}
	wroteSocket(req.Context(), len(msg))
	if trace != nil && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{})
	}
	header := k.header.Clone()
	body := &codexSocketBody{c: k.c, ctx: req.Context(), k: k, shape: shape}
	for i := range codexSocketPrelude {
		frame, err := body.frame()
		if i == 0 && err == nil && trace != nil && trace.GotFirstResponseByte != nil {
			trace.GotFirstResponseByte()
		}
		if err != nil {
			body.Close()
			// nothing of the reply has been handed on yet
			return nil, ended(err)
		}
		if frame == nil { // dropped
			continue
		}
		kind := gjson.GetBytes(frame, "type").String()
		switch kind {
		case "codex.rate_limits":
			codexLimitHeaders(header, frame)
		case "codex.response.metadata":
			gjson.GetBytes(frame, "headers").ForEach(func(k, v gjson.Result) bool {
				if header.Get(k.String()) == "" {
					header.Set(k.String(), v.String())
				}
				return true
			})
		case "error":
			if reused {
				// previous_response_not_found, the connection limit, or
				// any other fault: a new socket answers for itself
				body.Close()
				return nil, errCodexSocketAgain
			}
			if k.relay {
				body.Close()
				return nil, errCodexSocketRelayError
			}
			// ahead of the reply: it stands for the HTTP route's status
			if res, fall := codexSocketError(req, header, frame, k.copilot); fall || res != nil {
				body.Close()
				if fall {
					return nil, errCodexSocketFallBack
				}
				return res, nil
			}
		}
		if after != "" && kind != "codex.rate_limits" && kind != "codex.response.metadata" &&
			(kind != "response.created" || gjson.GetBytes(frame, "response.previous_response_id").String() != after) {
			// a relay that answers the new input alone as a whole
			// conversation: nothing has been handed on, and the turn goes
			// whole on a new socket
			body.Close()
			codexDeltaIgnored(k.host)
			return nil, errCodexSocketAgain
		}
		body.pending = append(body.pending, sseEvent(kind, frame)...)
		if kind != "codex.rate_limits" && kind != "codex.response.metadata" {
			break
		}
	}
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          body,
		ContentLength: -1,
		Request:       req,
	}, nil
}

// socketTrace tells req's client trace (TryNet, the usage record's
// split) of the turn going out on the socket, as Go's client tells it of
// an HTTP request: a turn on a kept socket is a request on an open
// connection, which has no handshake of its own, and the frames' first is
// the reply's first byte. Without it a kept socket's turn, nearly every
// Codex turn after a conversation's first, wrote no request the trace
// saw, and the Routing page's "Where the time went" said only its
// masking (buhaodeshiyehao on X, Codex through Lody). On a new socket
// the handshake went through the client and the trace has its DNS,
// connect and TLS; the socket is the connection, got once the handshake
// is answered, so the handshake's round trip is the wait for it. The
// caller tells it WroteRequest once the message is written, and
// GotFirstResponseByte at the first frame.
func socketTrace(req *http.Request, reused bool) *httptrace.ClientTrace {
	trace := httptrace.ContextClientTrace(req.Context())
	if trace == nil {
		return nil
	}
	if reused && trace.GetConn != nil {
		trace.GetConn(req.URL.Host)
	}
	if trace.GotConn != nil {
		trace.GotConn(httptrace.GotConnInfo{Reused: reused})
	}
	return trace
}

// codexSocketEnded is the turn going over HTTP after the socket ended,
// or failed to take the message, before any of the reply was handed on:
// whatever the close (1009 message too big, 1011, 1012, 1013, a dropped
// connection, a normal close with no reply), nothing has reached the
// caller and the backend has begun no reply, so the same body is asked
// over HTTP, which answers as the route does. A 1009 is remembered for
// that host (refusedTooBig).
func codexSocketEnded(host string, size int, err error) error {
	if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
		refusedTooBig(host, size)
	}
	return errCodexSocketFallBack
}

// codexSocketError is the HTTP reply an error frame that comes ahead of
// the reply stands for, and true when the turn is to go over HTTP
// instead; neither for one the stream carries on with.
func codexSocketError(req *http.Request, header http.Header, frame []byte, copilot bool) (*http.Response, bool) {
	code := gjson.GetBytes(frame, "error.code").String()
	if code == "websocket_connection_limit_reached" {
		return nil, true
	}
	status := int(gjson.GetBytes(frame, "status").Int())
	if status == 0 {
		status = int(gjson.GetBytes(frame, "status_code").Int())
	}
	if status < 400 || status > 599 {
		// Copilot's error frames name no status
		// ({"type":"error","error":{"code":"bad_request",...}}): the
		// turn is asked over HTTP, whose reply has the status the
		// account's resting and retries read
		return nil, copilot
	}
	h := header.Clone()
	for _, path := range []string{"headers", "error.headers"} {
		gjson.GetBytes(frame, path).ForEach(func(k, v gjson.Result) bool {
			h.Set(k.String(), v.String())
			return true
		})
	}
	h.Set("Content-Type", "application/json")
	var b []byte
	if e := gjson.GetBytes(frame, "error"); e.IsObject() {
		b = []byte(`{"error":` + e.Raw + `}`)
	} else {
		b = frame
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(b)),
		ContentLength: int64(len(b)),
		Request:       req,
	}, false
}

// codexLimitHeaders tells a codex.rate_limits event in the x-codex-*
// headers the HTTP route carries (codex-api's parse_rate_limit_for_limit),
// each only where the handshake didn't say it already.
func codexLimitHeaders(h http.Header, frame []byte) {
	ev := gjson.ParseBytes(frame)
	name := ev.Get("metered_limit_name").String()
	if name == "" {
		name = ev.Get("limit_name").String()
	}
	name = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
	if name == "" {
		name = "codex"
	}
	set := func(k, v string) {
		if v != "" && h.Get(k) == "" {
			h.Set(k, v)
		}
	}
	num := func(r gjson.Result) string {
		if !r.Exists() || r.Type != gjson.Number {
			return ""
		}
		return strconv.FormatFloat(r.Float(), 'f', -1, 64)
	}
	if p := ev.Get("plan_type").String(); p != "" {
		set("x-codex-plan-type", p)
	}
	for _, which := range []string{"primary", "secondary"} {
		w := ev.Get("rate_limits." + which)
		if !w.IsObject() {
			continue
		}
		pre := "x-" + name + "-" + which + "-"
		set(pre+"used-percent", num(w.Get("used_percent")))
		set(pre+"window-minutes", num(w.Get("window_minutes")))
		set(pre+"reset-at", num(w.Get("reset_at")))
		set(pre+"reset-after-seconds", num(w.Get("reset_after_seconds")))
	}
	if cr := ev.Get("credits"); cr.IsObject() {
		tf := func(r gjson.Result) string {
			if r.Type == gjson.True {
				return "True"
			}
			if r.Type == gjson.False {
				return "False"
			}
			return ""
		}
		set("x-codex-credits-has-credits", tf(cr.Get("has_credits")))
		set("x-codex-credits-unlimited", tf(cr.Get("unlimited")))
		if b := cr.Get("balance"); b.Exists() && b.Type != gjson.Null {
			set("x-codex-credits-balance", b.String())
		}
	}
}

// sseEvent is a frame as the HTTP route sends it.
func sseEvent(kind string, frame []byte) []byte {
	if bytes.ContainsAny(frame, "\r\n") {
		var b bytes.Buffer
		if json.Compact(&b, frame) == nil {
			frame = b.Bytes()
		}
	}
	out := make([]byte, 0, len(kind)+len(frame)+18)
	if kind != "" {
		out = append(out, "event: "...)
		out = append(out, kind...)
		out = append(out, '\n')
	}
	out = append(out, "data: "...)
	out = append(out, frame...)
	return append(out, "\n\n"...)
}

// codexSocketBody is the socket's frames read as server-sent events.
type codexSocketBody struct {
	c      *websocket.Conn
	ctx    context.Context
	k      *codexKept
	shape  *codexTurnShape // nil: the socket isn't kept after the turn
	output []itemSum
	// sealed: which of output are reasoning items (codexKept.sealed)
	sealed  []bool
	pending []byte
	done    bool
	err     error
	closed  atomic.Bool
}

// frame is the socket's next frame, nil for one the reply leaves out.
func (b *codexSocketBody) frame() ([]byte, error) {
	if b.done {
		return nil, io.EOF
	}
	typ, data, err := b.c.Read(b.ctx)
	if err != nil {
		b.done = true
		b.k.close()
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			return nil, io.EOF
		}
		if b.closed.Load() {
			return nil, io.ErrClosedPipe
		}
		return nil, fmt.Errorf("codex websocket: %w", err)
	}
	if typ != websocket.MessageText {
		return nil, nil
	}
	data = bytes.TrimSpace(data)
	switch gjson.GetBytes(data, "type").String() {
	case "responsesapi.websocket_timing":
		return nil, nil
	case "response.output_item.done":
		if b.shape != nil {
			if sum, ok := codexReplyItem(data); ok {
				b.output = append(b.output, sum)
				b.sealed = append(b.sealed, gjson.GetBytes(data, "item.type").String() == "reasoning")
			} else {
				b.shape = nil
			}
		}
	case "response.completed":
		b.done = true
		if id := gjson.GetBytes(data, "response.id").String(); id != "" && b.shape != nil {
			// kept for the conversation's next turn, which may carry on
			// from this one
			b.k.fields, b.k.input = b.shape.fields, b.shape.sums
			b.k.output, b.k.sealed, b.k.replyID = b.output, b.sealed, id
			codexKeepPut(b.k)
		} else {
			b.k.closeNormally()
		}
	case "response.failed", "response.incomplete", "response.done", "error":
		b.done = true
		b.k.closeNormally()
	}
	return data, nil
}

func (b *codexSocketBody) Read(p []byte) (int, error) {
	for len(b.pending) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		if b.done {
			return 0, io.EOF
		}
		frame, err := b.frame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// the backend ended the socket before the reply's end:
				// the stream is cut, as an HTTP reply cut short is
				err = io.ErrUnexpectedEOF
			}
			b.err = err
			continue
		}
		if frame != nil {
			b.pending = sseEvent(gjson.GetBytes(frame, "type").String(), frame)
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

// Close ends a turn read only in part, and its socket; a socket whose
// turn was read to its end is kept or closed already.
func (b *codexSocketBody) Close() error {
	if b.closed.Swap(true) {
		return nil
	}
	if !b.done {
		b.k.close()
	}
	return nil
}
