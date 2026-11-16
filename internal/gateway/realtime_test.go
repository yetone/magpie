package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// rtUp is a realtime vendor: DashScope's /api-ws/v1/realtime and OpenAI's
// /v1/realtime. Each text frame comes back as {"type":"echo","got":…},
// a binary frame as it came, a response.create gets a response.done with
// usage, and {"type":"close.me"} closes the session 1000 "bye".
type rtUp struct {
	mu     sync.Mutex
	opened []*http.Request
	frames []string
	closed chan websocket.StatusCode // how each session's client side ended
	url    string
}

func (u *rtUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api-ws/v1/realtime" && r.URL.Path != "/v1/realtime" {
		http.Error(w, `{"error":{"message":"no such path"}}`, http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer sk-sp-k" {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
		return
	}
	u.mu.Lock()
	u.opened = append(u.opened, r.Clone(context.Background()))
	u.mu.Unlock()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"realtime"}})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := context.Background()
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			u.closed <- websocket.CloseStatus(err)
			return
		}
		if typ == websocket.MessageBinary {
			c.Write(ctx, typ, b)
			continue
		}
		u.mu.Lock()
		u.frames = append(u.frames, string(b))
		u.mu.Unlock()
		switch gjson.GetBytes(b, "type").String() {
		case "close.me":
			c.Close(websocket.StatusNormalClosure, "bye")
			u.closed <- websocket.StatusNormalClosure
			return
		case "response.create":
			c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.audio.delta","delta":"QUJD"}`))
			c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.done","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":20,"input_token_details":{"cached_tokens":40}}}}`))
		default:
			c.Write(ctx, websocket.MessageText, []byte(`{"type":"echo","got":`+string(b)+`}`))
		}
	}
}

func (u *rtUp) sessions() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.opened)
}

// rtServe has a Token Plan provider at the fake's DashScope base and an
// OpenAI-style one at its /v1, and the gateway served as the real server
// does; every session ends before the test's folders go.
func rtServe(t *testing.T) (*rtUp, *Server, *httptest.Server) {
	t.Helper()
	fresh(t)
	u := &rtUp{closed: make(chan websocket.StatusCode, 16)}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	u.url = up.URL
	for _, p := range []provider.Provider{
		{ID: "plan", Name: "Plan", Key: "sk-sp-k", Chat: up.URL + "/compatible-mode/v1", Models: []string{"qwen-audio-3.0-realtime-plus"}},
		{ID: "oa", Name: "OA", Key: "sk-sp-k", Chat: up.URL + "/v1", Models: []string{"gpt-realtime"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s, ts := sockServer(t)
	t.Cleanup(func() {
		s.realtimes.end()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.realtimes.wait(ctx)
	})
	return u, s, ts
}

func rtDial(t *testing.T, ts *httptest.Server, path string, h http.Header, protos ...string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+path, &websocket.DialOptions{HTTPHeader: h, Subprotocols: protos})
	if err == nil {
		t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

func rtRead(t *testing.T, c *websocket.Conn) (websocket.MessageType, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	typ, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return typ, string(b)
}

func rtSend(t *testing.T, c *websocket.Conn, typ websocket.MessageType, s string) {
	t.Helper()
	if err := c.Write(context.Background(), typ, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

func rtClosed(t *testing.T, u *rtUp) websocket.StatusCode {
	t.Helper()
	select {
	case st := <-u.closed:
		return st
	case <-time.After(30 * time.Second):
		t.Fatal("the vendor's side never ended")
		return 0
	}
}

// A Token Plan's realtime model: the session opens at the plan host's
// /api-ws/v1/realtime with the plan's key and the provider's own model id,
// text and audio frames go both ways as they are, a response.done's usage
// is recorded, and the vendor's close reaches the client with its code.
func TestRealtimeTokenPlanSession(t *testing.T) {
	u, _, ts := rtServe(t)
	c, resp, err := rtDial(t, ts, "/v1/realtime?model=plan/qwen-audio-3.0-realtime-plus", nil)
	if err != nil {
		t.Fatalf("%v %v", err, resp)
	}
	if u.sessions() != 1 {
		t.Fatalf("%d sessions", u.sessions())
	}
	got := u.opened[0]
	if got.URL.Path != "/api-ws/v1/realtime" || got.URL.Query().Get("model") != "qwen-audio-3.0-realtime-plus" {
		t.Fatalf("opened %s", got.URL)
	}
	update := `{"type":"session.update","session":{"modalities":["text","audio"],"voice":"Cherry"}}`
	rtSend(t, c, websocket.MessageText, update)
	if typ, b := rtRead(t, c); typ != websocket.MessageText || gjson.Get(b, "got").Raw != update {
		t.Fatalf("echo %v %s", typ, b)
	}
	rtSend(t, c, websocket.MessageBinary, "\x00\x01pcm")
	if typ, b := rtRead(t, c); typ != websocket.MessageBinary || b != "\x00\x01pcm" {
		t.Fatalf("binary %v %q", typ, b)
	}
	rtSend(t, c, websocket.MessageText, `{"type":"input_audio_buffer.append","audio":"UklGRg=="}`)
	rtRead(t, c)
	rtSend(t, c, websocket.MessageText, `{"type":"response.create"}`)
	if _, b := rtRead(t, c); b != `{"type":"response.audio.delta","delta":"QUJD"}` {
		t.Fatalf("delta %s", b)
	}
	if _, b := rtRead(t, c); gjson.Get(b, "type").String() != "response.done" {
		t.Fatalf("done %s", b)
	}
	rtSend(t, c, websocket.MessageText, `{"type":"close.me"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusNormalClosure || ce.Reason != "bye" {
		t.Fatalf("the client's session ended %v", err)
	}
	rtClosed(t, u)

	var recs []usage.Record
	for i := 0; i < 250; i++ {
		if recs = usage.Load(time.Time{}); len(recs) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(recs) != 1 || recs[0].Operation != "realtime" || recs[0].Provider != "plan" || recs[0].Model != "qwen-audio-3.0-realtime-plus" ||
		recs[0].Requested != "plan/qwen-audio-3.0-realtime-plus" || recs[0].Input != 60 || recs[0].CacheRead != 40 || recs[0].Output != 20 || recs[0].Status != 200 {
		t.Fatalf("usage %+v", recs)
	}
}

// An OpenAI-style provider's session opens at base + /realtime; the
// client's subprotocols go on, less one carrying a key, and the vendor's
// choice comes back. A bare id no provider lists goes to the one
// DashScope provider. The client closing ends the vendor's side too.
func TestRealtimeOpenAIStyleAndClientClose(t *testing.T) {
	u, _, ts := rtServe(t)
	c, resp, err := rtDial(t, ts, "/v1/realtime?model=oa/gpt-realtime", http.Header{"OpenAI-Beta": {"realtime=v1"}}, "realtime", "openai-insecure-api-key.sk-leak")
	if err != nil {
		t.Fatalf("%v %v", err, resp)
	}
	got := u.opened[0]
	if got.URL.Path != "/v1/realtime" || got.URL.Query().Get("model") != "gpt-realtime" || got.Header.Get("OpenAI-Beta") != "realtime=v1" {
		t.Fatalf("opened %s %v", got.URL, got.Header)
	}
	if p := got.Header.Get("Sec-WebSocket-Protocol"); strings.Contains(p, "sk-leak") || !strings.Contains(p, "realtime") {
		t.Fatalf("the vendor was offered %q", p)
	}
	if c.Subprotocol() != "realtime" {
		t.Fatalf("subprotocol %q", c.Subprotocol())
	}
	c.Close(websocket.StatusNormalClosure, "")
	if st := rtClosed(t, u); st != websocket.StatusNormalClosure {
		t.Fatalf("the vendor's side ended %d", st)
	}

	c, resp, err = rtDial(t, ts, "/realtime?model=qwen3-livetranslate-flash-realtime", nil)
	if err != nil {
		t.Fatalf("%v %v", err, resp)
	}
	if got := u.opened[1]; got.URL.Path != "/api-ws/v1/realtime" || got.URL.Query().Get("model") != "qwen3-livetranslate-flash-realtime" {
		t.Fatalf("opened %s", got.URL)
	}
	c.Close(websocket.StatusNormalClosure, "")
	rtClosed(t, u)
}

// Every guard an HTTP request meets holds a realtime session: no Upgrade
// is told what to send, no model and an unknown one are refused, a web
// page elsewhere, a sandboxed frame (Origin null), a localhost subdomain
// and a rebound name can't open one, a remote caller needs a key, a key
// held to other models is refused, and a vendor's refusal comes back as
// its status. None reaches the vendor but the last.
func TestRealtimeGuards(t *testing.T) {
	u, _, ts := rtServe(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	budget.Forget()
	keys, secrets := newCaller(t, "Laptop", "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"oa/*"}}); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(ts.URL + "/v1/realtime?model=plan/qwen-audio-3.0-realtime-plus")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusUpgradeRequired || !strings.Contains(string(b), "WebSocket") {
		t.Errorf("a plain GET: %d %s", res.StatusCode, b)
	}
	remote := func(secret string) http.Header {
		h := http.Header{"X-Test-Remote": {"192.168.1.9:5000"}}
		if secret != "" {
			h.Set("Authorization", "Bearer "+secret)
		}
		return h
	}
	const plan = "/v1/realtime?model=plan/qwen-audio-3.0-realtime-plus"
	refused := func(what, path string, h http.Header, want int) {
		t.Helper()
		_, resp, err := rtDial(t, ts, path, h)
		if err == nil {
			t.Errorf("%s opened a session", what)
			return
		}
		if resp == nil || resp.StatusCode != want {
			t.Errorf("%s: %v %v, want %d", what, err, resp, want)
		}
	}
	refused("no model", "/v1/realtime", nil, http.StatusBadRequest)
	refused("an unknown model", "/v1/realtime?model=nope/x", nil, http.StatusNotFound)
	for _, origin := range []string{"https://evil.example", "null", "http://evil.localhost:3425"} {
		refused("Origin "+origin, plan, http.Header{"Origin": {origin}}, http.StatusForbidden)
	}
	_, resp, err := rtDial(t, ts, plan, http.Header{
		"X-Test-Host": {"rebind.attacker.example:3425"},
		"User-Agent":  {"Mozilla/5.0 (Macintosh) Chrome/140"}, "Sec-Fetch-Site": {"same-origin"}, "Sec-Fetch-Mode": {"websocket"},
	})
	if err == nil || resp == nil || resp.StatusCode < 400 {
		t.Errorf("a rebound page: %v %v", err, resp)
	}
	refused("a remote caller without a key", plan, remote(""), http.StatusUnauthorized)
	refused("a key held to other models", plan, remote(secrets[1]), http.StatusForbidden)
	if u.sessions() != 0 {
		t.Fatalf("a refused session reached the vendor")
	}

	// a key: served
	c, resp, err := rtDial(t, ts, plan, remote(secrets[0]))
	if err != nil {
		t.Fatalf("keyed: %v %v", err, resp)
	}
	c.Close(websocket.StatusNormalClosure, "")
	rtClosed(t, u)

	// the vendor refusing the key: its status and message
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "sk-sp-wrong", Chat: u.url + "/compatible-mode/v1", Models: []string{"qwen-audio-3.0-realtime-plus"}}); err != nil {
		t.Fatal(err)
	}
	_, resp, err = rtDial(t, ts, plan, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a refused key: %v %v", err, resp)
	}
	b, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "bad key") {
		t.Errorf("the vendor's message: %s", b)
	}
}

// With secrets kept, a client's text frame goes out masked and the
// vendor's frames get the values back; an audio frame goes as it is.
func TestRealtimeMasked(t *testing.T) {
	u, _, ts := rtServe(t)
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	c, resp, err := rtDial(t, ts, "/v1/realtime?model=plan/qwen-audio-3.0-realtime-plus", nil)
	if err != nil {
		t.Fatalf("%v %v", err, resp)
	}
	secret := "sk-ant-api03-" + strings.Repeat("Ab3x", 20)
	rtSend(t, c, websocket.MessageText, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"key `+secret+`"}]}}`)
	_, b := rtRead(t, c)
	if !strings.Contains(b, secret) {
		t.Errorf("the echo didn't get the value back: %s", b)
	}
	u.mu.Lock()
	sent := u.frames[0]
	u.mu.Unlock()
	if strings.Contains(sent, secret) || !strings.Contains(sent, "key ") {
		t.Fatalf("the vendor was sent %s", sent)
	}
	c.Close(websocket.StatusNormalClosure, "")
	rtClosed(t, u)
}
