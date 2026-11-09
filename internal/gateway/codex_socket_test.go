package gateway

import (
	"cmp"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// socketTurn is what a fake ChatGPT backend saw of one turn.
type socketTurn struct {
	header http.Header
	msg    []byte
	socket int // which handshake, from 1
}

// fakeSocketBackend stands in for the ChatGPT backend: a turn over its
// WebSocket is answered with frames(the turn); one over HTTP with a reply
// cut before response.completed, as brickhao's network cut every streamed
// HTTP reply from chatgpt.com at about 120 s. refuse answers the
// handshake with that status instead.
type fakeSocketBackend struct {
	mu      sync.Mutex
	sockets []socketTurn
	posts   int
	// handshakes counts the sockets opened, a refused message's too
	handshakes int
	refuse     int
	frames     func(socketTurn) []string
	// limit is the socket's read limit (0: none); a bigger message is
	// closed 1009 as coder/websocket does it
	limit int64
	// closeWith, when set, closes the socket with that status after the
	// frames, instead of a normal close
	closeWith websocket.StatusCode
	// keep reads the socket's next turn after a reply, as the backend
	// does, until the client closes it
	keep bool
	// path is the Responses path it answers at, chatgpt.com's when "";
	// whole has its HTTP reply run to response.completed
	path  string
	whole bool
}

func (f *fakeSocketBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, cmp.Or(f.path, "/backend-api/codex/responses")) {
		http.NotFound(w, r)
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if f.refuse != 0 {
			http.Error(w, "no sockets here", f.refuse)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		// counted before the read: a message over the limit is closed
		// 1009 inside Read, and the client can finish its turns over HTTP
		// before Read returns
		f.mu.Lock()
		f.handshakes++
		n := f.handshakes
		f.mu.Unlock()
		c.SetReadLimit(-1)
		if f.limit > 0 {
			c.SetReadLimit(f.limit)
		}
		for {
			_, msg, err := c.Read(r.Context())
			if err != nil {
				return
			}
			turn := socketTurn{header: r.Header.Clone(), msg: msg, socket: n}
			f.mu.Lock()
			f.sockets = append(f.sockets, turn)
			f.mu.Unlock()
			for _, fr := range f.frames(turn) {
				if c.Write(r.Context(), websocket.MessageText, []byte(fr)) != nil {
					return
				}
			}
			if !f.keep {
				break
			}
		}
		if f.closeWith != 0 {
			c.Close(f.closeWith, "")
			return
		}
		c.Close(websocket.StatusNormalClosure, "")
		return
	}
	io.ReadAll(r.Body)
	f.mu.Lock()
	f.posts++
	f.mu.Unlock()
	if f.whole {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"h1"}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"h1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		return
	}
	w.Header()["Content-Type"] = nil
	io.WriteString(w, sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1"}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"po"}`))
	// cut here, before response.completed
}

// counts are the turns seen over HTTP and over the socket.
func (f *fakeSocketBackend) counts() (posts, sockets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts, len(f.sockets)
}

func socketOn(t *testing.T) {
	t.Helper()
	codexSocketOff.Store(false)
	t.Cleanup(func() {
		codexSocketOff.Store(true)
		codexKeepDrain()
		codexShunMu.Lock()
		clear(codexShun)
		clear(codexTooBig)
		codexShunMu.Unlock()
	})
}

// a whole reply, as the backend sends it over its socket
func wholeReply(socketTurn) []string {
	return []string{
		`{"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":42,"window_minutes":300,"reset_after_seconds":600,"reset_at":1792000000},"secondary":{"used_percent":7.5,"window_minutes":10080,"reset_after_seconds":6000,"reset_at":1792500000}},"credits":{"has_credits":false,"unlimited":false,"balance":"0"}}`,
		`{"type":"codex.response.metadata","headers":{"x-codex-turn-state":"ts-1","x-models-etag":"W/\"e9\""}}`,
		`{"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
		`{"type":"responsesapi.websocket_timing","timing":{"ms":3}}`,
		"{\"type\":\"response.output_text.delta\",\n\"delta\":\"pong\"}",
		`{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`,
	}
}

// brickhao on Discord: a Codex turn through magpie that streams past
// about 120 s was cut ("stream closed before response.completed") on a
// network that cuts chatgpt.com's long HTTP replies, while Codex's own
// WebSocket to the same backend ran to the end. magpie now sends a
// streamed turn over the backend's WebSocket, as Codex does, and hands
// Codex the same events, and the headers the HTTP route says, over HTTP.
func TestCodexTurnGoesOverWebSocket(t *testing.T) {
	socketOn(t)
	defer provider.CodexZstdForTests(true)() // as the real turn goes
	f := &fakeSocketBackend{frames: wholeReply}
	chatgpt(t, f.ServeHTTP)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(
		`{"model":"gpt-5.5","stream":true,"input":"ping","client_metadata":{"session_id":"s1"}}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Codex-Turn-State", "ts-0")
	req.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	New().Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "pong") || !strings.Contains(body, "event: response.completed\ndata: ") {
		t.Fatalf("%d, a reply without its end: %s", rec.Code, body)
	}
	if strings.Contains(body, "websocket_timing") {
		t.Errorf("the socket's timing frame reached Codex: %s", body)
	}
	if posts, sockets := f.counts(); posts != 0 || sockets != 1 {
		t.Fatalf("%d HTTP posts, %d sockets; want the turn on one socket", posts, sockets)
	}
	for k, want := range map[string]string{
		"x-codex-turn-state":             "ts-1",
		"x-codex-primary-used-percent":   "42",
		"x-codex-primary-window-minutes": "300",
		"x-codex-primary-reset-at":       "1792000000",
		"x-codex-secondary-used-percent": "7.5",
		"x-codex-plan-type":              "plus",
		"x-codex-credits-has-credits":    "False",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	// magpie's own models are added to the backend's etag
	if got := rec.Header().Get("x-models-etag"); !strings.HasPrefix(got, `W/"e9`) {
		t.Errorf("x-models-etag %q, not the backend's", got)
	}
	turn := f.sockets[0]
	if got := turn.header.Get("OpenAI-Beta"); got != codexSocketBeta {
		t.Errorf("OpenAI-Beta %q", got)
	}
	if turn.header.Get("Authorization") != "Bearer chatgpt-token" || turn.header.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("handshake lost the sign-in: %v", turn.header)
	}
	for _, k := range []string{"X-Codex-Turn-State", "X-OpenAI-Internal-Codex-Responses-Lite", "Content-Encoding", "Accept"} {
		if v := turn.header.Get(k); v != "" {
			t.Errorf("handshake carries %s: %q", k, v)
		}
	}
	m := gjson.ParseBytes(turn.msg)
	if m.Get("type").String() != "response.create" || !strings.HasPrefix(string(turn.msg), `{"type":"response.create",`) ||
		m.Get("model").String() != "gpt-5.5" || !m.Get("stream").Bool() || m.Get("input").String() != "ping" {
		t.Errorf("message %s", turn.msg)
	}
	if m.Get("client_metadata.session_id").String() != "s1" ||
		m.Get("client_metadata.x-codex-turn-state").String() != "ts-0" ||
		m.Get("client_metadata.ws_request_header_x_openai_internal_codex_responses_lite").String() != "true" {
		t.Errorf("client_metadata %s", m.Get("client_metadata").Raw)
	}
}

// A backend that won't take the socket is asked over HTTP, the same turn,
// and isn't asked for a socket again for a while.
func TestCodexSocketRefusedGoesOverHTTP(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{refuse: http.StatusForbidden}
	chatgpt(t, f.ServeHTTP)
	var handshakes atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			handshakes.Add(1)
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	provider.CodexBase = up.URL + "/backend-api/codex"
	for i := range 2 {
		code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
		if code != 200 || !strings.Contains(body, `"delta":"po"`) {
			t.Fatalf("turn %d: %d %s", i, code, body)
		}
	}
	if posts, _ := f.counts(); posts != 2 || handshakes.Load() != 1 {
		t.Fatalf("%d posts, %d handshakes; want 2 posts and one handshake", posts, handshakes.Load())
	}
}

// An error the backend says on the socket before the reply is the status
// it would have answered over HTTP: a used-up account (429) rests and the
// turn goes on to the next account, whose turn goes over the socket too.
func TestCodexSocketErrorIsItsStatus(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	socketOn(t)
	var tried []string // under f.mu
	var f *fakeSocketBackend
	f = &fakeSocketBackend{frames: func(turn socketTurn) []string {
		f.mu.Lock()
		tried = append(tried, turn.header.Get("chatgpt-account-id"))
		f.mu.Unlock()
		if turn.header.Get("chatgpt-account-id") == "acct-1" {
			return []string{`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200},"headers":{"x-codex-primary-used-percent":"100"}}`}
		}
		return wholeReply(turn)
	}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") || !strings.Contains(body, "response.completed") {
		t.Fatalf("%d %s", code, body)
	}
	posts, _ := f.counts()
	f.mu.Lock()
	got := strings.Join(tried, ",")
	f.mu.Unlock()
	if got != "acct-1,acct-2" || posts != 0 {
		t.Fatalf("tried %v over the socket, %d posts", got, posts)
	}
	// the reply the backend's transport hands on is that status
	req, _ := http.NewRequest("POST", provider.CodexBase+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	req.Header.Set("chatgpt-account-id", "acct-1")
	res, err := New().client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 429 || gjson.GetBytes(b, "error.type").String() != "usage_limit_reached" ||
		res.Header.Get("x-codex-primary-used-percent") != "100" {
		t.Errorf("%d %v %s; want the frame's 429, its headers and error", res.StatusCode, res.Header, b)
	}
	restingUntil.Lock()
	n := len(restingUntil.m)
	restingUntil.Unlock()
	if n == 0 {
		t.Error("the used-up account doesn't rest")
	}
}

// A socket the backend closes before the reply's end is a cut stream,
// not a whole one.
func TestCodexSocketClosedEarlyIsCut(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{frames: func(socketTurn) []string {
		return wholeReply(socketTurn{})[:5]
	}}
	chatgpt(t, f.ServeHTTP)
	res, err := New().client.Post(provider.CodexBase+"/responses", "application/json",
		strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(string(b), "pong") {
		t.Fatalf("read %q, %v; want the reply so far and a cut", b, err)
	}
}

// A request that isn't a streamed turn goes over HTTP as before.
func TestCodexSocketOnlyForStreamedTurns(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{frames: wholeReply}
	chatgpt(t, f.ServeHTTP)
	res, err := New().client.Post(provider.CodexBase+"/responses", "application/json",
		strings.NewReader(`{"model":"gpt-5.5","stream":false,"input":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(res.Body)
	res.Body.Close()
	if posts, sockets := f.counts(); posts != 1 || sockets != 0 {
		t.Fatalf("%d posts, %d sockets", posts, sockets)
	}
}

// postsOpened are the turns seen over HTTP and the sockets opened,
// a socket whose message was refused too.
func (f *fakeSocketBackend) postsOpened() (posts, opened int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts, f.handshakes
}

// Lamrfu on Discord: on a long conversation chatgpt.com closed the socket
// 1009 (message too big) on the turn's message, and Codex got a 502
// ("codex websocket: failed to get reader: received close frame: status =
// StatusMessageTooBig"), retried and got it again. The same turn now goes
// over HTTP, and a message as big doesn't try the socket again.
func TestCodexSocketTooBigGoesOverHTTP(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{frames: wholeReply, limit: 200}
	chatgpt(t, f.ServeHTTP)
	big := `{"model":"gpt-5.5","stream":true,"input":"` + strings.Repeat("long conversation ", 40) + `"}`
	for i := range 2 {
		code, body := codexPost(t, big)
		if code != 200 || !strings.Contains(body, `"delta":"po"`) {
			t.Fatalf("turn %d: %d %s; want the HTTP reply", i, code, body)
		}
	}
	if posts, opened := f.postsOpened(); posts != 2 || opened != 1 {
		t.Fatalf("%d posts, %d sockets; want both turns over HTTP and one socket tried", posts, opened)
	}
	// a small turn still goes over the socket
	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") || !strings.Contains(body, "response.completed") {
		t.Fatalf("small turn: %d %s", code, body)
	}
	if posts, sockets := f.counts(); posts != 2 || sockets != 1 {
		t.Fatalf("%d posts, %d sockets; want the small turn on the socket", posts, sockets)
	}
}

// Any end of the socket before a frame of the reply was handed on goes
// over HTTP the same way: a close after the message with any status, a
// normal close with no reply, a close after only the headers' frames.
func TestCodexSocketEndedBeforeReplyGoesOverHTTP(t *testing.T) {
	limits := func(socketTurn) []string { return wholeReply(socketTurn{})[:2] }
	none := func(socketTurn) []string { return nil }
	for _, tc := range []struct {
		name   string
		frames func(socketTurn) []string
		status websocket.StatusCode
	}{
		{"too big after the message", none, websocket.StatusMessageTooBig},
		{"too big after the limits", limits, websocket.StatusMessageTooBig},
		{"internal error", none, websocket.StatusInternalError},
		{"service restart", none, websocket.StatusServiceRestart},
		{"try again later", none, websocket.StatusTryAgainLater},
		{"policy violation", none, websocket.StatusPolicyViolation},
		{"going away", limits, websocket.StatusGoingAway},
		{"normal, no reply", none, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socketOn(t)
			f := &fakeSocketBackend{frames: tc.frames, closeWith: tc.status}
			chatgpt(t, f.ServeHTTP)
			code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
			if code != 200 || !strings.Contains(body, `"delta":"po"`) {
				t.Fatalf("%d %s; want the HTTP reply", code, body)
			}
			if posts, sockets := f.counts(); posts != 1 || sockets != 1 {
				t.Fatalf("%d posts, %d sockets", posts, sockets)
			}
		})
	}
}

// Once a frame of the reply was handed on, a 1009 close is a cut stream,
// and the turn is not asked again over HTTP.
func TestCodexSocketTooBigMidReplyIsCut(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{frames: func(socketTurn) []string {
		return wholeReply(socketTurn{})[:5]
	}, closeWith: websocket.StatusMessageTooBig}
	chatgpt(t, f.ServeHTTP)
	res, err := New().client.Post(provider.CodexBase+"/responses", "application/json",
		strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err == nil || !strings.Contains(string(b), "pong") {
		t.Fatalf("read %q, %v; want the reply so far and a cut", b, err)
	}
	if posts, _ := f.counts(); posts != 0 {
		t.Fatalf("%d HTTP posts; the turn was asked twice", posts)
	}
}
