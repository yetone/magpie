package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// faithleysath (#5781, from matrix0487 on Discord): a Responses API
// provider, a relay that speaks the Responses WebSocket as chatgpt.com
// does, can have its turns go over that socket with `magpie provider set
// <id> socket=auto|on`. These tests are those turns against a fake
// upstream on this computer.

// relaySocket adds a Responses provider "relay" at the fake upstream f
// answers (counting every handshake asked for, a refused one's too) with
// Socket sock, and returns the upstream.
func relaySocket(t *testing.T, f *fakeSocketBackend, sock string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f.path = "/v1/responses"
	var asked atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			asked.Add(1)
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	addRelay(t, "relay", up.URL, sock)
	return up, &asked
}

func addRelay(t *testing.T, id, base, sock string) {
	t.Helper()
	p := provider.Provider{ID: id, Name: id, Key: "sk-" + id, Models: []string{"m1"}, Responses: base + "/v1", Socket: sock}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func relayPost(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") ||
		!strings.Contains(rec.Body.String(), "event: response.completed\n") {
		t.Fatalf("%d, a reply without its end: %s", rec.Code, rec.Body.String())
	}
	return rec
}

const relayPing = `{"model":"relay/m1","stream":true,"input":"ping"}`

func TestResponsesProviderTurnGoesOverWebSocket(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{whole: true}
	f.frames = keptReply(f)
	relaySocket(t, f, provider.SocketAuto)

	rec := relayPost(t, relayPing)
	if posts, sockets := f.counts(); posts != 0 || sockets != 1 {
		t.Fatalf("%d HTTP posts, %d socket turns; want the turn on the socket", posts, sockets)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type %q, want the stream's", ct)
	}
	turn := f.turn(0)
	if got := turn.header.Get("Authorization"); got != "Bearer sk-relay" {
		t.Errorf("handshake Authorization %q, want the provider's key", got)
	}
	if got := turn.header.Get("OpenAI-Beta"); got != codexSocketBeta {
		t.Errorf("OpenAI-Beta %q", got)
	}
	m := gjson.ParseBytes(turn.msg)
	if m.Get("type").String() != "response.create" || m.Get("model").String() != "m1" || m.Get("input").String() != "ping" {
		t.Errorf("message %s", turn.msg)
	}
}

// A provider without Socket never dials one, the default; not even
// when another provider on the same host has it set.
func TestResponsesProviderWithoutSocketNeverDials(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{whole: true}
	f.frames = keptReply(f)
	up, asked := relaySocket(t, f, "")
	addRelay(t, "near", up.URL, provider.SocketOn)

	relayPost(t, relayPing)
	if posts, _ := f.counts(); posts != 1 || asked.Load() != 0 {
		t.Fatalf("%d posts, %d handshakes; want HTTP alone", posts, asked.Load())
	}
	relayPost(t, strings.Replace(relayPing, "relay/m1", "near/m1", 1))
	if posts, sockets := f.counts(); posts != 1 || sockets != 1 {
		t.Fatalf("the provider with Socket: %d posts, %d socket turns", posts, sockets)
	}
	relayPost(t, relayPing)
	if posts, _ := f.counts(); posts != 2 || asked.Load() != 1 {
		t.Fatalf("after the other's socket: %d posts, %d handshakes; want HTTP alone", posts, asked.Load())
	}
}

// auto: a refused handshake has the turn go over HTTP, and the host's
// next turns too for a while, unasked.
func TestResponsesProviderSocketRefusedAutoShuns(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{whole: true, refuse: http.StatusForbidden}
	up, asked := relaySocket(t, f, provider.SocketAuto)
	relayPost(t, relayPing)
	relayPost(t, relayPing)
	if posts, _ := f.counts(); posts != 2 || asked.Load() != 1 {
		t.Fatalf("%d posts, %d handshakes; want both over HTTP after one refusal", posts, asked.Load())
	}
	if u, _ := url.Parse(up.URL); !codexShunned(u.Host) {
		t.Error("the host isn't left to HTTP")
	}
}

// on: a refused handshake has the turn go over HTTP, and the next turn
// asks for the socket again.
func TestResponsesProviderSocketRefusedOnTriesAgain(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{whole: true, refuse: http.StatusForbidden}
	up, asked := relaySocket(t, f, provider.SocketOn)
	relayPost(t, relayPing)
	relayPost(t, relayPing)
	if posts, _ := f.counts(); posts != 2 || asked.Load() != 2 {
		t.Fatalf("%d posts, %d handshakes; want both over HTTP, each asking first", posts, asked.Load())
	}
	if u, _ := url.Parse(up.URL); codexShunned(u.Host) {
		t.Error("on left the host to HTTP")
	}
}

// A socket that closes before the reply's first event has the turn go
// over HTTP, nothing of it lost.
func TestResponsesProviderSocketClosedEarlyGoesOverHTTP(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{whole: true, closeWith: websocket.StatusTryAgainLater}
	f.frames = func(socketTurn) []string { return nil }
	relaySocket(t, f, provider.SocketAuto)
	rec := relayPost(t, relayPing)
	if posts, sockets := f.counts(); posts != 1 || sockets != 1 {
		t.Fatalf("%d posts, %d socket turns; want the socket tried, then HTTP", posts, sockets)
	}
	if n := strings.Count(rec.Body.String(), "event: response.created\n"); n != 1 {
		t.Errorf("%d response.created; want the HTTP reply alone: %s", n, rec.Body.String())
	}
}

// A conversation's next turn goes on its kept socket as a delta after
// previous_response_id, as chatgpt.com's does.
func TestResponsesProviderKeptSocketSendsTheDelta(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true, whole: true}
	f.frames = keptReply(f)
	relaySocket(t, f, provider.SocketAuto)

	relayPost(t, keptTurn("conv-1", "relay/m1", userPing))
	relayPost(t, keptTurn("conv-1", "relay/m1", userPing, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 0 || sockets != 2 || f.opened() != 1 {
		t.Fatalf("%d posts, %d turns on %d sockets; want both turns on one socket", posts, sockets, f.opened())
	}
	second := gjson.ParseBytes(f.turn(1).msg)
	if got := second.Get("previous_response_id").String(); got != "resp-1" {
		t.Errorf("previous_response_id %q, want resp-1: %s", got, f.turn(1).msg)
	}
	if in := second.Get("input").Array(); len(in) != 1 || in[0].Get("content.0.text").String() != "again" {
		t.Errorf("delta input %s, want only the new user message", second.Get("input").Raw)
	}
}

// The socket's reply is said to be the event stream it is, as the
// provider's HTTP reply is, for whatever reads its headers.
func TestResponsesProviderSocketReplyIsAStream(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{whole: true, path: "/v1/responses"}
	f.frames = keptReply(f)
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "relay", Key: "k", Responses: up.URL + "/v1", Socket: provider.SocketAuto}
	body := relayPing
	req, err := http.NewRequestWithContext(p.Via(t.Context()), "POST", up.URL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := codexSocket{next: http.DefaultTransport}.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, sockets := f.counts(); sockets != 1 {
		t.Fatalf("%d socket turns", sockets)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type %q", ct)
	}
}
