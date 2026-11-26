package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/yetone/magpie/internal/provider"
)

// M4n5ter on X (after #6163): codex/* through magpie often hangs. A
// kept socket waited for its next turn with nothing reading it, no ping
// and no read deadline; when the path under it had died without a close
// (a proxy's TUN or a NAT dropping the flow), the next turn's Write went
// into the local buffers and its Read waited for ever, so
// errCodexSocketAgain never came. These tests put such a path between
// magpie and a fake backend.

// hole is a TCP relay to a backend whose connections can be cut as a
// proxy cuts a flow it dropped: magpie's side of each stays open, and
// nothing comes back. A dead connection takes what magpie writes and
// throws it away; a stuck one stops taking it, so magpie's write waits.
type hole struct {
	ln    net.Listener
	to    string
	mu    sync.Mutex
	conns []*holeConn
	after *string // cutAfter's, for each connection opened
}

type holeConn struct {
	client, up net.Conn
	dead       atomic.Bool
	stuck      chan struct{}
	stuckOnce  sync.Once
	// after: once a frame with this is relayed to magpie, the connection
	// dies
	after atomic.Pointer[string]
}

func newHole(t *testing.T, to string) *hole {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &hole{ln: ln, to: to}
	go h.serve()
	t.Cleanup(func() {
		ln.Close()
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, c := range h.conns {
			c.client.Close()
			c.up.Close()
		}
	})
	return h
}

func (h *hole) serve() {
	for {
		client, err := h.ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", h.to)
		if err != nil {
			client.Close()
			continue
		}
		c := &holeConn{client: client, up: up, stuck: make(chan struct{})}
		h.mu.Lock()
		c.after.Store(h.after)
		h.conns = append(h.conns, c)
		h.mu.Unlock()
		go c.toUp()
		go c.toClient()
	}
}

func (c *holeConn) toUp() {
	buf := make([]byte, 32<<10)
	for {
		select {
		case <-c.stuck:
			// takes nothing more: what magpie writes stays in its buffers
			select {}
		default:
		}
		n, err := c.client.Read(buf)
		if err != nil {
			c.up.Close()
			return
		}
		if c.dead.Load() {
			continue
		}
		if _, err := c.up.Write(buf[:n]); err != nil {
			return
		}
	}
}

func (c *holeConn) toClient() {
	buf := make([]byte, 32<<10)
	for {
		n, err := c.up.Read(buf)
		if err != nil {
			if !c.dead.Load() {
				c.client.Close()
			}
			return
		}
		if c.dead.Load() {
			continue
		}
		if _, err := c.client.Write(buf[:n]); err != nil {
			return
		}
		if a := c.after.Load(); a != nil && bytes.Contains(buf[:n], []byte(*a)) {
			c.dead.Store(true)
		}
	}
}

// cut leaves every connection open so far dead.
func (h *hole) cut() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.conns {
		c.dead.Store(true)
	}
}

// jam leaves every connection open so far stuck.
func (h *hole) jam() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.conns {
		c.dead.Store(true)
		c.stuckOnce.Do(func() { close(c.stuck) })
	}
}

// cutAfter has the connections opened from now on die once a frame
// carrying s has reached magpie.
func (h *hole) cutAfter(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.after = &s
}

// thinkingBackend answers each turn after hold, reading the socket the
// whole time, so it answers pings while it thinks, as a live backend
// does. mid, when set, is sent between the reply's first frames and its
// end, after pause.
type thinkingBackend struct {
	mu         sync.Mutex
	handshakes int
	turns      int
	posts      int
	hold       time.Duration
	pause      time.Duration
	// closeIdle closes the socket this long after a reply, as the backend
	// ending a connection that waits for its next turn
	closeIdle time.Duration
}

func (f *thinkingBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		io.ReadAll(r.Body)
		f.mu.Lock()
		f.posts++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"h1"}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"h1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(-1)
	f.mu.Lock()
	f.handshakes++
	f.mu.Unlock()
	msgs := make(chan []byte, 16)
	go func() {
		defer close(msgs)
		for {
			_, m, err := c.Read(r.Context())
			if err != nil {
				return
			}
			msgs <- m
		}
	}()
	for served := 0; ; served++ {
		var idle <-chan time.Time
		if f.closeIdle > 0 && served > 0 {
			idle = time.After(f.closeIdle)
		}
		select {
		case <-idle:
			c.Close(websocket.StatusNormalClosure, "")
			return
		case _, ok := <-msgs:
			if !ok {
				return
			}
		}
		f.mu.Lock()
		f.turns++
		n := f.turns
		f.mu.Unlock()
		time.Sleep(f.hold)
		frames := []string{
			fmt.Sprintf(`{"type":"response.created","response":{"id":"resp-%d"}}`, n),
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_a","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","delta":"pong"}`,
		}
		rest := []string{
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_a","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"pong"}]}}`,
			fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%d","usage":{"input_tokens":7,"output_tokens":1}}}`, n),
		}
		for _, fr := range frames {
			if c.Write(r.Context(), websocket.MessageText, []byte(fr)) != nil {
				return
			}
		}
		time.Sleep(f.pause)
		for _, fr := range rest {
			if c.Write(r.Context(), websocket.MessageText, []byte(fr)) != nil {
				return
			}
		}
	}
}

func (f *thinkingBackend) counts() (handshakes, turns, posts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handshakes, f.turns, f.posts
}

// behindHole has the ChatGPT backend be f, reached through a hole.
func behindHole(t *testing.T, f http.Handler) *hole {
	t.Helper()
	up := chatgpt(t, f.ServeHTTP)
	h := newHole(t, up.Listener.Addr().String())
	provider.CodexBase = "http://" + h.ln.Addr().String() + "/backend-api/codex"
	return h
}

// alivePost sends body as acct's Codex turn and gives up after within,
// saying how long it took.
func alivePost(t *testing.T, acct, body string, within time.Duration) (int, string, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer chatgpt-token-"+acct)
	req.Header.Set("chatgpt-account-id", acct)
	start := time.Now()
	New().Handler().ServeHTTP(rec, req)
	took := time.Since(start)
	if ctx.Err() != nil {
		t.Errorf("the turn waited %s, until its client gave up: %s", took.Round(time.Millisecond), rec.Body.String())
	}
	return rec.Code, rec.Body.String(), took
}

func wholeTurn(t *testing.T, code int, body string) {
	t.Helper()
	if code != 200 || !strings.Contains(body, "pong") || !strings.Contains(body, "event: response.completed\n") {
		t.Errorf("%d, a reply without its end: %s", code, body)
	}
}

// A kept socket whose path died while it waited is found dead by the
// ping after the next turn is written, and the turn goes whole on a new
// socket instead of waiting for ever.
func TestCodexKeptSocketGoneSilentIsAskedAgain(t *testing.T) {
	socketOn(t)
	aliveKnobs(t, time.Minute, 300*time.Millisecond, time.Minute)
	f := &thinkingBackend{}
	h := behindHole(t, f)

	code, body, _ := alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing), 10*time.Second)
	wholeTurn(t, code, body)
	if codexKeptCount() != 1 {
		t.Fatalf("%d sockets kept, want the first turn's", codexKeptCount())
	}
	h.cut()
	code, body, took := alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing, asstPong, userAgain), 15*time.Second)
	wholeTurn(t, code, body)
	if hs, turns, posts := f.counts(); hs != 2 || turns != 2 || posts != 0 {
		t.Errorf("%d handshakes, %d turns over the socket, %d over HTTP; want the second turn whole on a new socket", hs, turns, posts)
	}
	if took > 5*time.Second {
		t.Errorf("the dead socket was found after %s", took)
	}
}

// A kept socket whose writes stopped going anywhere (#7448: the turn
// waited 10 minutes in the reused socket's Write) is found stuck, and
// the turn goes on a new socket.
func TestCodexKeptSocketStuckWriteIsAskedAgain(t *testing.T) {
	socketOn(t)
	aliveKnobs(t, time.Minute, time.Minute, 500*time.Millisecond)
	f := &thinkingBackend{}
	h := behindHole(t, f)

	code, body, _ := alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing), 10*time.Second)
	wholeTurn(t, code, body)
	h.jam()
	// more than the loopback's buffers take, so the write waits
	big := `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("a", 24<<20) + `"}]}`
	code, body, took := alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing, asstPong, big), 60*time.Second)
	wholeTurn(t, code, body)
	if hs, turns, posts := f.counts(); hs != 2 || turns != 2 || posts != 0 {
		t.Errorf("%d handshakes, %d turns over the socket, %d over HTTP; want the second turn whole on a new socket", hs, turns, posts)
	}
	if took > 30*time.Second {
		t.Errorf("the stuck write was found after %s", took)
	}
}

// A model that thinks longer than a ping and its pong take, on a live
// socket, is waited for: the socket answers the pings, so it isn't dead
// (#6163 saw 66 to 155 s with no event).
func TestCodexSocketThinkingIsNotDead(t *testing.T) {
	socketOn(t)
	aliveKnobs(t, 100*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond)
	f := &thinkingBackend{hold: 2 * time.Second, pause: 2 * time.Second}
	behindHole(t, f)

	code, body, _ := alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing), 20*time.Second)
	wholeTurn(t, code, body)
	code, body, _ = alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing, asstPong, userAgain), 20*time.Second)
	wholeTurn(t, code, body)
	if hs, turns, posts := f.counts(); hs != 1 || turns != 2 || posts != 0 {
		t.Errorf("%d handshakes, %d turns over the socket, %d over HTTP; want both turns on one socket", hs, turns, posts)
	}
}

// A socket whose path dies mid-reply ends the reply as cut, once a ping
// goes unanswered, and the turn isn't sent again: part of it reached
// the client.
func TestCodexSocketGoneMidReplyEnds(t *testing.T) {
	socketOn(t)
	aliveKnobs(t, 200*time.Millisecond, 300*time.Millisecond, time.Minute)
	f := &thinkingBackend{pause: time.Minute}
	h := behindHole(t, f)
	h.cutAfter("output_text.delta")

	code, body, took := alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing), 20*time.Second)
	if code != 200 || !strings.Contains(body, "pong") || strings.Contains(body, `"type":"response.completed"`) {
		t.Errorf("%d, want the reply's start and no end: %s", code, body)
	}
	if hs, turns, posts := f.counts(); hs != 1 || turns != 1 || posts != 0 {
		t.Errorf("%d handshakes, %d turns over the socket, %d over HTTP; want the one turn sent once", hs, turns, posts)
	}
	if took > 5*time.Second {
		t.Errorf("the reply was cut after %s", took)
	}
	if codexKeptCount() != 0 {
		t.Errorf("%d sockets kept after a cut reply", codexKeptCount())
	}
}

// A kept socket the backend closes while it waits is let go then, not
// when its idle time is up.
func TestCodexKeptSocketClosedWhileKeptIsLetGo(t *testing.T) {
	socketOn(t)
	aliveKnobs(t, time.Minute, time.Minute, time.Minute)
	f := &thinkingBackend{closeIdle: 100 * time.Millisecond}
	behindHole(t, f)

	code, body, _ := alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing), 10*time.Second)
	wholeTurn(t, code, body)
	waitFor(t, "the socket the backend closed let go", func() bool { return codexKeptCount() == 0 })
}

// A kept socket whose path dies while it waits is let go once its ping
// goes unanswered.
func TestCodexKeptSocketGoneWhileKeptIsLetGo(t *testing.T) {
	socketOn(t)
	aliveKnobs(t, 100*time.Millisecond, 200*time.Millisecond, time.Minute)
	f := &thinkingBackend{}
	h := behindHole(t, f)

	code, body, _ := alivePost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing), 10*time.Second)
	wholeTurn(t, code, body)
	// pings answered: still kept
	time.Sleep(time.Second)
	if codexKeptCount() != 1 {
		t.Fatalf("%d sockets kept on a live path", codexKeptCount())
	}
	h.cut()
	waitFor(t, "the socket whose path died let go", func() bool { return codexKeptCount() == 0 })
}

// aliveKnobs sets the socket's liveness bounds for one test.
func aliveKnobs(t *testing.T, quiet, pong, stall time.Duration) {
	t.Helper()
	q, p, s, k := codexSocketQuiet, codexSocketPong, codexSocketStall, codexSocketTick
	codexSocketQuiet, codexSocketPong, codexSocketStall = quiet, pong, stall
	codexSocketTick = max(min(quiet, pong, stall)/4, 10*time.Millisecond)
	t.Cleanup(func() {
		codexSocketQuiet, codexSocketPong, codexSocketStall, codexSocketTick = q, p, s, k
	})
}
