package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/artifact"
)

// livePage is one page's room socket, as runtime.js opens it: from its
// opaque origin, with its token.
type livePage struct {
	t   *testing.T
	ws  *websocket.Conn
	in  chan map[string]any
	err error // why it closed, once in is
}

func openLive(t *testing.T, srv *httptest.Server, id, tok string) *livePage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + ArtifactPagePath + id + "/_api/live?t=" + tok
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"null"}}})
	if err != nil {
		t.Fatal(err)
	}
	p := &livePage{t: t, ws: ws, in: make(chan map[string]any, 64)}
	t.Cleanup(func() { ws.CloseNow() })
	go func() {
		defer close(p.in)
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				p.err = err
				return
			}
			var m map[string]any
			json.Unmarshal(b, &m)
			p.in <- m
		}
	}()
	if hi := p.next("hi"); hi["sid"] == "" {
		t.Fatalf("hi: %v", hi)
	}
	return p
}

func (p *livePage) send(m map[string]any) {
	p.t.Helper()
	b, _ := json.Marshal(m)
	if err := p.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		p.t.Fatal(err)
	}
}

// next is the next message of kind t; others before it are skipped.
func (p *livePage) next(t string) map[string]any {
	p.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-p.in:
			if !ok {
				p.t.Fatalf("closed waiting for %s", t)
			}
			if m["t"] == t {
				return m
			}
		case <-timeout:
			p.t.Fatalf("no %s", t)
		}
	}
}

// quiet: nothing comes within a moment (what isn't for this page).
func (p *livePage) quiet(what string) {
	p.t.Helper()
	select {
	case m := <-p.in:
		p.t.Fatalf("%s: heard %v", what, m)
	case <-time.After(300 * time.Millisecond):
	}
}

// Two tabs of one artifact's page are in its room: each sees the other
// come, set its presence and go, hears every emit (its own too, as
// sameTab), and a named room's emits only reach its joiners.
func TestArtifactLiveRoom(t *testing.T) {
	h := artifacts(t)
	a, err := artifact.PublishPage(artifact.Publish{Page: []byte("<p>a"), Capabilities: artifact.Capabilities{"room": json.RawMessage(`{}`), "user": json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	tok := pageToken(t, h, ArtifactPagePath+a.ID+"/")
	srv := httptest.NewServer(h)
	defer srv.Close()

	one := openLive(t, srv, a.ID, tok)
	one.send(map[string]any{"t": "join", "room": "", "ref": 1})
	j := one.next("joined")
	peers, _ := j["peers"].([]any)
	if j["ref"] != float64(1) || len(peers) != 1 || !peers[0].(map[string]any)["sameTab"].(bool) {
		t.Fatalf("joined: %v", j)
	}
	me := peers[0].(map[string]any)
	if by, _ := me["by"].(string); !strings.HasPrefix(by, "u_") || me["isMe"] != true {
		t.Fatalf("a page that declares user is by its viewer: %v", me)
	}

	two := openLive(t, srv, a.ID, tok)
	two.send(map[string]any{"t": "join", "room": "", "ref": 1})
	if j := two.next("joined"); len(j["peers"].([]any)) != 2 {
		t.Fatalf("the second tab joined: %v", j)
	}
	p := one.next("peer")["peer"].(map[string]any)
	if p["sameTab"] != false || p["isMe"] != true {
		t.Fatalf("the second tab, as the first sees it: %v", p)
	}

	two.send(map[string]any{"t": "presence", "room": "", "patch": map[string]any{"x": 3, "sel": "a<b"}})
	p = one.next("peer")["peer"].(map[string]any)
	if pr := p["presence"].(map[string]any); pr["x"] != float64(3) || pr["sel"] != "a<b" {
		t.Fatalf("presence: %v", p)
	}
	two.send(map[string]any{"t": "presence", "room": "", "patch": map[string]any{"x": nil}})
	if pr := one.next("peer")["peer"].(map[string]any)["presence"].(map[string]any); len(pr) != 1 || pr["sel"] != "a<b" {
		t.Fatalf("a null removes its key: %v", pr)
	}

	one.send(map[string]any{"t": "emit", "room": "", "topic": "move", "data": map[string]any{"to": 4}})
	for _, c := range []struct {
		p    *livePage
		same bool
	}{{one, true}, {two, false}} {
		m := c.p.next("msg")
		from := m["from"].(map[string]any)
		if m["topic"] != "move" || m["data"].(map[string]any)["to"] != float64(4) || from["sameTab"] != c.same {
			t.Fatalf("emit heard: %v", m)
		}
	}

	// a named room: only its joiners hear it
	two.send(map[string]any{"t": "join", "room": "game-1", "ref": 2})
	if j := two.next("joined"); j["room"] != "game-1" || len(j["peers"].([]any)) != 1 {
		t.Fatalf("named: %v", j)
	}
	two.send(map[string]any{"t": "emit", "room": "game-1", "topic": "roll", "data": 6})
	if m := two.next("msg"); m["room"] != "game-1" {
		t.Fatalf("named emit: %v", m)
	}
	one.quiet("the lobby heard a named room")

	// too much, and wrong, is refused, with the ref
	two.send(map[string]any{"t": "emit", "room": "", "topic": "big", "data": strings.Repeat("x", 5000), "ref": 7})
	if e := two.next("error"); e["code"] != "invalid_argument" {
		t.Fatalf("5000 bytes: %v", e)
	}
	deep := any(1)
	for range 9 {
		deep = []any{deep}
	}
	two.send(map[string]any{"t": "presence", "room": "", "patch": map[string]any{"d": deep}})
	if e := two.next("error"); e["code"] != "invalid_argument" {
		t.Fatalf("10 deep: %v", e)
	}
	two.send(map[string]any{"t": "emit", "room": "", "topic": "Bad"})
	two.send(map[string]any{"t": "join", "room": "Bad", "ref": 9})
	if e := two.next("error"); e["code"] != "invalid_argument" {
		t.Fatalf("a bad topic: %v", e)
	}
	if e := two.next("error"); e["code"] != "invalid_argument" || e["ref"] != float64(9) {
		t.Fatalf("a bad room: %v", e)
	}
	for i := range 16 {
		two.send(map[string]any{"t": "join", "room": "r" + string(rune('a'+i)), "ref": 100 + i})
	}
	for range 15 {
		two.next("joined")
	}
	if e := two.next("error"); e["code"] != "limit_reached" || e["ref"] != float64(115) {
		t.Fatalf("the 17th room: %v", e)
	}

	// a tab that goes is gone from the others
	two.ws.Close(websocket.StatusNormalClosure, "")
	if g := one.next("gone"); g["peer"] != p["peer"] {
		t.Fatalf("gone: %v, want %v", g, p["peer"])
	}
}

// A page with no user declared sends no viewer id; one that doesn't
// declare room, or has no token, gets no socket; and the room ends when
// the artifact goes (4001), which the page doesn't dial again.
func TestArtifactLiveRefused(t *testing.T) {
	h := artifacts(t)
	room, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<p>a"), Capabilities: artifact.Capabilities{"room": json.RawMessage(`{}`)}})
	plain, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<p>b"), Capabilities: artifact.Capabilities{"db": json.RawMessage(`{}`)}})
	tok := pageToken(t, h, ArtifactPagePath+room.ID+"/")
	ptok := pageToken(t, h, ArtifactPagePath+plain.ID+"/")
	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, c := range []struct{ why, id, tok string }{
		{"no room declared", plain.ID, ptok},
		{"another page's token", room.ID, ptok},
		{"no token", room.ID, ""},
	} {
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + ArtifactPagePath + c.id + "/_api/live?t=" + c.tok
		ws, res, err := websocket.Dial(context.Background(), u, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"null"}}})
		if err == nil {
			ws.CloseNow()
			t.Fatalf("%s: a socket", c.why)
		}
		if res == nil || res.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: %v", c.why, res)
		}
	}
	// another site's page is refused before its token is looked at
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + ArtifactPagePath + room.ID + "/_api/live?t=" + tok
	if ws, _, err := websocket.Dial(context.Background(), u, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://example.com"}}}); err == nil {
		ws.CloseNow()
		t.Fatal("a web page's socket")
	}

	p := openLive(t, srv, room.ID, tok)
	p.send(map[string]any{"t": "join", "room": "", "ref": 1})
	if me := p.next("joined")["peers"].([]any)[0].(map[string]any); me["by"] != nil {
		t.Fatalf("by with no user declared: %v", me)
	}
	if err := artifact.Delete(room.ID); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-p.in:
			if ok {
				continue
			}
			if websocket.CloseStatus(p.err) != 4001 {
				t.Fatalf("closed with %v, want 4001", p.err)
			}
			return
		case <-timeout:
			t.Fatal("still open after the artifact went")
		}
	}
}
