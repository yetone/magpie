package gateway

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// streamVendor streams a short reply on chat completions, Responses and
// Messages; fail is how many requests it answers 500 "overloaded" first,
// after the first it lets through.
type streamVendor struct {
	mu    sync.Mutex
	seen  int
	fail  int
	block chan struct{} // the first request waits on it, if set
}

func (v *streamVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	v.mu.Lock()
	v.seen++
	n := v.seen
	failing := n > 1 && v.fail > 0
	if failing {
		v.fail--
	}
	v.mu.Unlock()
	if n == 1 && v.block != nil {
		select {
		case <-v.block:
		case <-r.Context().Done():
			return
		}
	}
	if failing {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"type":"api_error","message":"the vendor is overloaded"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	switch {
	case strings.HasSuffix(r.URL.Path, "/messages"):
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"queued ok\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	case strings.HasSuffix(r.URL.Path, "/responses"):
		io.WriteString(w, quietCreated+quietDelta("queued ok")+quietDone)
	default:
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"queued ok\"}}]}\n\n"+
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"+
			"data: [DONE]\n\n")
	}
}

// idleConn is a connection whose reads time out after idle with nothing
// read, as an agent's HTTP client with an idle timeout (WorkBuddy, Trae,
// Qoder): any byte, a comment's too, starts it again.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c idleConn) Read(b []byte) (int, error) {
	c.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(b)
}

// idleClient is an HTTP client that gives up once it hears nothing for
// idle, headers or body.
func idleClient(idle time.Duration) *http.Client {
	d := &net.Dialer{}
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return idleConn{c, idle}, nil
		},
	}}
}

// queueGateway is the real server (lanGuard in front) with a provider "q"
// of one key, set up by p, and every keepalive within milliseconds.
func queueGateway(t *testing.T, v *streamVendor, set func(*provider.Provider)) string {
	t.Helper()
	fresh(t)
	keepFast(t, keepaliveGap, 40*time.Millisecond, keepaliveLongest)
	k, w := keepHeldAfter, watchEvery
	keepHeldAfter, watchEvery = 80*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery = k, w })
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "q", Name: "Queued", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}
	set(&p)
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(lanGuard(New().Handler()))
	t.Cleanup(gw.Close)
	return gw.URL
}

var queueAsks = []struct{ name, path, body string }{
	{"chat", "/v1/chat/completions", `{"model":"q/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	{"responses", "/v1/responses", `{"model":"q/m","stream":true,"input":"hi"}`},
	{"messages", "/v1/messages", `{"model":"q/m","stream":true,"max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`},
}

// askIdle sends body to path with an agent that gives up after idle of
// silence, and returns the status, the stream read, and the read's error.
func askIdle(gw, path, body string, idle time.Duration) (int, string, error) {
	req, _ := http.NewRequest("POST", gw+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := idleClient(idle).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	var b strings.Builder
	_, err = io.Copy(&b, bufio.NewReader(res.Body))
	return res.StatusCode, b.String(), err
}

// coeo91 on Discord: with a Requests per minute limit, a request held for
// room in the minute had its agent hear nothing at all — WorkBuddy, Trae
// and Qoder said it timed out. Through the real server, a streaming
// request that waits 1.2s for the minute with an agent that gives up
// after 400ms of silence is sent the stream's 200 and keepalives
// meanwhile, then the vendor's reply, in each protocol's stream.
func TestQueuedStreamKeptAliveForTheMinute(t *testing.T) {
	for _, a := range queueAsks {
		t.Run(a.name, func(t *testing.T) {
			shortMinute(t, 1200*time.Millisecond, time.Minute)
			gw := queueGateway(t, &streamVendor{}, func(p *provider.Provider) { p.MaxRPM = 1 })
			if code, body, err := askIdle(gw, a.path, a.body, 400*time.Millisecond); err != nil || code != 200 {
				t.Fatalf("first: %d %v %s", code, err, body)
			}
			began := time.Now()
			code, body, err := askIdle(gw, a.path, a.body, 400*time.Millisecond)
			if err != nil {
				t.Fatalf("the agent gave up after %s of a queued request: %v (read %q)", time.Since(began).Round(time.Millisecond), err, body)
			}
			if code != 200 || !strings.Contains(body, ": keepalive") || !strings.Contains(body, "queued ok") {
				t.Fatalf("queued = %d %q, want 200, keepalives, then the reply", code, body)
			}
			if d := time.Since(began); d < time.Second {
				t.Fatalf("answered after %s, inside the minute", d)
			}
		})
	}
}

// A slot of MaxConcurrency waited for is kept alive the same way.
func TestQueuedStreamKeptAliveForASlot(t *testing.T) {
	one := 1
	v := &streamVendor{block: make(chan struct{})}
	gw := queueGateway(t, v, func(p *provider.Provider) { p.MaxConcurrency = &one })
	first := make(chan error, 1)
	go func() {
		_, _, err := askIdle(gw, queueAsks[0].path, queueAsks[0].body, 5*time.Second)
		first <- err
	}()
	time.AfterFunc(time.Second, func() { close(v.block) })
	time.Sleep(100 * time.Millisecond) // the first has the slot
	code, body, err := askIdle(gw, queueAsks[0].path, queueAsks[0].body, 400*time.Millisecond)
	if err != nil || code != 200 || !strings.Contains(body, ": keepalive") || !strings.Contains(body, "queued ok") {
		t.Fatalf("queued for a slot = %d %v %q, want 200, keepalives, then the reply", code, err, body)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

// Once a queued request's agent has the stream's 200, the vendor failing
// after it reaches the agent as its stream's error event, in its
// protocol's own shape, not as a status the agent can no longer be told,
// nor a JSON error body inside the stream.
func TestQueuedStreamFailureIsTheStreamsError(t *testing.T) {
	wants := map[string][]string{
		"chat":      {`"error"`, "the vendor is overloaded"},
		"responses": {"event: response.failed", `"status":"failed"`, "the vendor is overloaded"},
		"messages":  {"event: error", `"type":"error"`, "the vendor is overloaded"},
	}
	for _, a := range queueAsks {
		t.Run(a.name, func(t *testing.T) {
			shortMinute(t, 1200*time.Millisecond, time.Minute)
			gw := queueGateway(t, &streamVendor{fail: 10}, func(p *provider.Provider) { p.MaxRPM = 1 })
			if code, body, err := askIdle(gw, a.path, a.body, 400*time.Millisecond); err != nil || code != 200 {
				t.Fatalf("first: %d %v %s", code, err, body)
			}
			code, body, err := askIdle(gw, a.path, a.body, 2*time.Second)
			if err != nil || code != 200 || !strings.Contains(body, ": keepalive") {
				t.Fatalf("queued = %d %v %q, want the stream's 200 and keepalives", code, err, body)
			}
			for _, w := range wants[a.name] {
				if !strings.Contains(body, w) {
					t.Fatalf("stream %q doesn't say %s", body, w)
				}
			}
			if strings.Contains(body, `{"error":{"type":"api_error","message":"the vendor is overloaded"}}`+"\n") || strings.HasPrefix(strings.TrimLeft(strings.ReplaceAll(body, ": keepalive\n", ""), "\n"), "{") {
				t.Fatalf("the vendor's JSON error went into the stream raw: %q", body)
			}
		})
	}
}

// A request that waits out its queue (QueueWait) after keepalives were
// sent is told so as its stream's error, saying the limit.
func TestQueuedStreamWaitedOutIsTheStreamsError(t *testing.T) {
	one := 1
	v := &streamVendor{block: make(chan struct{})}
	defer close(v.block)
	gw := queueGateway(t, v, func(p *provider.Provider) { p.MaxConcurrency, p.QueueWait = &one, 1 })
	go askIdle(gw, queueAsks[2].path, queueAsks[2].body, 5*time.Second)
	time.Sleep(100 * time.Millisecond)
	code, body, err := askIdle(gw, queueAsks[2].path, queueAsks[2].body, 400*time.Millisecond)
	if err != nil || code != 200 || !strings.Contains(body, ": keepalive") || !strings.Contains(body, "event: error") || !strings.Contains(body, "requests at once is its limit") {
		t.Fatalf("waited out = %d %v %q, want keepalives, then the limit as the stream's error", code, err, body)
	}
}

// A wait shorter than keepHeldAfter sends nothing ahead: turned away, it
// is still a 429 with its Retry-After.
func TestQueuedBrieflyStill429(t *testing.T) {
	shortMinute(t, 1500*time.Millisecond, 50*time.Millisecond)
	gw := queueGateway(t, &streamVendor{}, func(p *provider.Provider) { p.MaxRPM = 1 })
	if code, _, err := askIdle(gw, queueAsks[0].path, queueAsks[0].body, time.Second); err != nil || code != 200 {
		t.Fatalf("first: %d %v", code, err)
	}
	code, body, err := askIdle(gw, queueAsks[0].path, queueAsks[0].body, time.Second)
	if err != nil || code != 429 || strings.Contains(body, "keepalive") {
		t.Fatalf("second = %d %v %q, want a plain 429", code, err, body)
	}
}

// A try that wasn't held — the last one, its retries spent — and whose
// agent was kept alive while it waited is held from then on: the vendor's
// error is the stream's to tell (failTo), not a JSON body written into
// the stream the agent already has.
func TestQueuedUnheldTryHeldOnceKeptAlive(t *testing.T) {
	k, w := keepHeldAfter, watchEvery
	keepHeldAfter, watchEvery = 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery = k, w })
	rec := httptest.NewRecorder()
	hw := newHoldWriter(rec, false)
	hw.alive, hw.streams = &keptAlive{proto: provider.Chat}, true
	stop := hw.keepQueued()
	time.Sleep(80 * time.Millisecond)
	stop()
	if !hw.alive.sent {
		t.Fatal("the agent wasn't kept alive while its try waited")
	}
	hw.Header().Set("Content-Type", "application/json")
	hw.WriteHeader(400)
	hw.Write([]byte(`{"error":{"message":"bad request"}}`))
	if hw.passing || strings.Contains(rec.Body.String(), "bad request") {
		t.Fatalf("the vendor's error went into the kept-alive stream: passing %v, %q", hw.passing, rec.Body.String())
	}
}
