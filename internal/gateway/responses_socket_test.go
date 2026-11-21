package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// A provider at a plain http address without Socket never dials one;
// not even when another provider on the same host has it set.
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

// openAIAt adds "oai", a key's provider at OpenAI's own Responses URL
// with Socket sock, whose api.openai.com is connected to the fake f on
// this computer (Resolve, its test certificate let through), so nothing
// reaches the network. It returns the handshakes the fake was asked for.
func openAIAt(t *testing.T, f *fakeSocketBackend, sock string) *atomic.Int32 {
	t.Helper()
	return httpsAt(t, f, "oai", "api.openai.com", sock)
}

// httpsAt adds provider id at https://host/v1 with Socket sock, host
// connected to the fake f as openAIAt's is.
func httpsAt(t *testing.T, f *fakeSocketBackend, id, host, sock string) *atomic.Int32 {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f.path = "/v1/responses"
	var asked atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "host "+r.Host, http.StatusMisdirectedRequest)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			asked.Add(1)
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: id, Key: "sk-" + id, Models: []string{"m1"}, Responses: "https://" + host + "/v1", Socket: sock,
		InsecureTLS: true, Resolve: map[string][]string{host: {up.Listener.Addr().String()}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return &asked
}

// KawaiiZapic on X: Codex on an OpenAI API key sent the whole
// conversation up every turn, since Codex says store false and only the
// Responses WebSocket keeps a turn for the next one then. Codex's own
// OpenAI provider speaks that socket to api.openai.com, and so does magpie
// now with Socket unset: the second turn is the new input alone after
// previous_response_id, on the first turn's socket.
func TestOpenAIProviderTurnsGoOverWebSocketByDefault(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true, whole: true}
	f.frames = keptReply(f)
	asked := openAIAt(t, f, "")

	relayPost(t, keptTurn("conv-1", "oai/m1", userPing))
	relayPost(t, keptTurn("conv-1", "oai/m1", userPing, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 0 || sockets != 2 || asked.Load() != 1 {
		t.Fatalf("%d HTTP posts, %d socket turns on %d handshakes; want both turns on one socket", posts, sockets, asked.Load())
	}
	second := gjson.ParseBytes(f.turn(1).msg)
	if got := second.Get("previous_response_id").String(); got != "resp-1" {
		t.Errorf("previous_response_id %q, want resp-1: %s", got, f.turn(1).msg)
	}
	if in := second.Get("input").Array(); len(in) != 1 || in[0].Get("content.0.text").String() != "again" {
		t.Errorf("delta input %s, want only the new user message", second.Get("input").Raw)
	}
}

// Set off, OpenAI's API is spoken HTTP alone, as before.
func TestOpenAIProviderSocketOffNeverDials(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true, whole: true}
	f.frames = keptReply(f)
	asked := openAIAt(t, f, provider.SocketOff)

	relayPost(t, keptTurn("conv-1", "oai/m1", userPing))
	relayPost(t, keptTurn("conv-1", "oai/m1", userPing, asstPong, userAgain))
	if posts, _ := f.counts(); posts != 2 || asked.Load() != 0 {
		t.Fatalf("%d posts, %d handshakes; want HTTP alone", posts, asked.Load())
	}
}

// KawaiiZapic on X, then the owner: relays too. A relay at an https
// Responses address, Socket unset, has its turns go over the socket, the
// next one as the new input after previous_response_id, while the relay
// says it answered after that response.
func TestRelayTurnsGoOverWebSocketByDefault(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true, whole: true}
	f.frames = keptReply(f)
	asked := httpsAt(t, f, "relay", "relay.example", "")

	relayPost(t, keptTurn("conv-1", "relay/m1", userPing))
	rec := relayPost(t, keptTurn("conv-1", "relay/m1", userPing, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 0 || sockets != 2 || asked.Load() != 1 {
		t.Fatalf("%d HTTP posts, %d socket turns on %d handshakes; want both turns on one socket", posts, sockets, asked.Load())
	}
	second := gjson.ParseBytes(f.turn(1).msg)
	if got := second.Get("previous_response_id").String(); got != "resp-1" {
		t.Errorf("previous_response_id %q, want resp-1: %s", got, f.turn(1).msg)
	}
	if in := second.Get("input").Array(); len(in) != 1 {
		t.Errorf("delta input %s, want only the new user message", second.Get("input").Raw)
	}
	if n := strings.Count(rec.Body.String(), "event: response.created\n"); n != 1 {
		t.Errorf("%d response.created: %s", n, rec.Body.String())
	}
}

// Most relays refuse the handshake (DeepSeek 405, OpenRouter 404, tried
// 11-21), a 401 too: one that does is spoken HTTP for codexRelayShun, not
// asked again in ten minutes. OpenAI's own API keeps the short shun.
func TestRelayRefusedSocketIsLeftToHTTPForHours(t *testing.T) {
	for _, code := range []int{http.StatusMethodNotAllowed, http.StatusNotFound, http.StatusUnauthorized} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			socketOn(t)
			f := &fakeSocketBackend{whole: true, refuse: code}
			asked := httpsAt(t, f, "relay", "relay.example", "")
			relayPost(t, relayPing)
			relayPost(t, relayPing)
			if posts, _ := f.counts(); posts != 2 || asked.Load() != 1 {
				t.Fatalf("%d posts, %d handshakes; want both over HTTP after one refusal", posts, asked.Load())
			}
			codexShunMu.Lock()
			until := codexShun["relay.example"]
			codexShunMu.Unlock()
			if left := time.Until(until); left < codexRelayShun-time.Minute {
				t.Errorf("left to HTTP for %s, want %s", left, codexRelayShun)
			}
		})
	}
	t.Run("openai", func(t *testing.T) {
		socketOn(t)
		f := &fakeSocketBackend{whole: true, refuse: http.StatusNotFound}
		openAIAt(t, f, "")
		relayPost(t, strings.Replace(relayPing, "relay/m1", "oai/m1", 1))
		codexShunMu.Lock()
		until := codexShun["api.openai.com"]
		codexShunMu.Unlock()
		if left := time.Until(until); left <= 0 || left > codexSocketShun {
			t.Errorf("OpenAI left to HTTP for %s, want %s", left, codexSocketShun)
		}
	})
}

// A relay that takes the socket but drops previous_response_id answers
// the new input alone as the whole conversation (DeepSeek's HTTP does,
// with a 200). Its delta's response.created names no previous response:
// nothing of that reply is handed on, the turn goes whole on a new
// socket, and the relay's next turns go whole too.
func TestRelayDeltaNotAnsweredAsOneGoesWhole(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true, whole: true}
	reply := keptReply(f)
	f.frames = func(turn socketTurn) []string {
		turn.msg = []byte(`{}`) // as though it never read previous_response_id
		return reply(turn)
	}
	httpsAt(t, f, "relay", "relay.example", "")

	relayPost(t, keptTurn("conv-1", "relay/m1", userPing))
	rec := relayPost(t, keptTurn("conv-1", "relay/m1", userPing, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 0 || sockets != 3 || f.opened() != 2 {
		t.Fatalf("%d posts, %d turns on %d sockets; want the delta tried, then whole on a new socket", posts, sockets, f.opened())
	}
	if !gjson.GetBytes(f.turn(1).msg, "previous_response_id").Exists() {
		t.Fatalf("the second turn wasn't tried as a delta: %s", f.turn(1).msg)
	}
	whole := f.turn(2)
	if whole.socket != 2 || gjson.GetBytes(whole.msg, "previous_response_id").Exists() ||
		len(gjson.GetBytes(whole.msg, "input").Array()) != 3 {
		t.Errorf("the retry on socket %d: %s; want the whole turn on a new socket", whole.socket, whole.msg)
	}
	if n := strings.Count(rec.Body.String(), "event: response.created\n"); n != 1 || !strings.Contains(rec.Body.String(), `"id":"resp-3"`) {
		t.Errorf("%d response.created; want the whole turn's reply alone: %s", n, rec.Body.String())
	}
	relayPost(t, keptTurn("conv-1", "relay/m1", userPing, asstPong, userAgain, asstPong, userPing))
	third := f.turn(3)
	if third.socket != 2 || gjson.GetBytes(third.msg, "previous_response_id").Exists() ||
		len(gjson.GetBytes(third.msg, "input").Array()) != 5 {
		t.Errorf("the next turn on socket %d: %s; want it whole on the kept socket", third.socket, third.msg)
	}
}

// A relay's socket that answers a new turn with an error frame has the
// turn go over HTTP; when HTTP takes it, the socket isn't asked again
// for codexRelayShun.
func TestRelaySocketErrorGoesOverHTTP(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{whole: true}
	f.frames = func(socketTurn) []string {
		return []string{`{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"Unsupported"}}`}
	}
	asked := httpsAt(t, f, "relay", "relay.example", "")
	rec := relayPost(t, relayPing)
	if n := strings.Count(rec.Body.String(), "event: response.created\n"); n != 1 || strings.Contains(rec.Body.String(), "Unsupported") {
		t.Errorf("want the HTTP reply alone: %s", rec.Body.String())
	}
	relayPost(t, relayPing)
	if posts, sockets := f.counts(); posts != 2 || sockets != 1 || asked.Load() != 1 {
		t.Fatalf("%d posts, %d socket turns, %d handshakes; want the socket tried once", posts, sockets, asked.Load())
	}
	codexShunMu.Lock()
	until := codexShun["relay.example"]
	codexShunMu.Unlock()
	if left := time.Until(until); left < codexRelayShun-time.Minute {
		t.Errorf("left to HTTP for %s, want %s", left, codexRelayShun)
	}
}
