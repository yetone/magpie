package gateway

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
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
	at    []time.Time // when each request arrived
	fail  int
	block chan struct{} // the first request waits on it, if set
}

// arrived is when the vendor's n-th request (from 0) arrived.
func (v *streamVendor) arrived(n int) (time.Time, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if n < len(v.at) {
		return v.at[n], true
	}
	return time.Time{}, false
}

func (v *streamVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	v.mu.Lock()
	v.seen++
	v.at = append(v.at, time.Now())
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
	gw := httptest.NewUnstartedServer(lanGuard(New().Handler()))
	gw.Listener = wroteListener{gw.Listener}
	gw.Start()
	t.Cleanup(gw.Close)
	return gw.URL
}

// wrote is when the gateway first wrote to each agent's connection, by
// the agent's address: the time the gateway sent it, which a client's own
// read can't tell under load, its goroutine waiting to run.
var wrote sync.Map // string -> *atomic.Int64

type wroteListener struct{ net.Listener }

func (l wroteListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return c, err
	}
	at := new(atomic.Int64)
	wrote.Store(c.RemoteAddr().String(), at)
	return wroteConn{c, at}, nil
}

type wroteConn struct {
	net.Conn
	at *atomic.Int64
}

func (c wroteConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.at.CompareAndSwap(0, time.Now().UnixNano())
	}
	return n, err
}

var queueAsks = []struct{ name, path, body string }{
	{"chat", "/v1/chat/completions", `{"model":"q/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	{"responses", "/v1/responses", `{"model":"q/m","stream":true,"input":"hi"}`},
	{"messages", "/v1/messages", `{"model":"q/m","stream":true,"max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`},
}

// queueMinute is the minute of the tests that wait for room in it. A queued
// request waits for what is left of it when it arrives, which under load
// is late: it is long enough that what is left stays far over
// keepHeldAfter (a request ~1.4s late was seen with 8 busy goroutines per
// core).
const queueMinute = 3 * time.Second

// askHeard sends body to path as an agent would and returns the status, the
// stream read, when the gateway first wrote to the agent (wrote), and the
// read's error. heard, if set, is called once the agent has a first byte.
func askHeard(gw, path, body string, heard func()) (int, string, time.Time, error) {
	req, _ := http.NewRequest("POST", gw+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	var from string
	req = req.WithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(i httptrace.GotConnInfo) { from = i.Conn.LocalAddr().String() },
		GotFirstResponseByte: func() {
			if heard != nil {
				heard()
			}
		},
	}))
	res, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	defer res.Body.Close()
	var b strings.Builder
	_, err = io.Copy(&b, bufio.NewReader(res.Body))
	var first time.Time
	if at, ok := wrote.Load(from); ok {
		if n := at.(*atomic.Int64).Load(); n != 0 {
			first = time.Unix(0, n)
		}
	}
	return res.StatusCode, b.String(), first, err
}

// waitSeen waits until v has had n requests.
func waitSeen(t *testing.T, v *streamVendor, n int) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, ok := v.arrived(n - 1); ok {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("the vendor never had %d requests", n)
		}
	}
}

// coeo91 on Discord: with a Requests per minute limit, a request held for
// room in the minute had its agent hear nothing at all — WorkBuddy, Trae
// and Qoder said it timed out. Through the real server, a streaming
// request that waits for the minute (queueMinute) hears the stream's 200 and
// keepalives before the vendor is asked, then the vendor's reply, in each
// protocol's stream. The test asks for that order, as the gateway wrote
// it, rather than for a client's idle timeout: under load (machine load
// ~130, 20582866a) every gap a client measures stretched, the first
// request's too, the client's goroutine waiting to run.
func TestQueuedStreamKeptAliveForTheMinute(t *testing.T) {
	for _, a := range queueAsks {
		t.Run(a.name, func(t *testing.T) {
			shortMinute(t, queueMinute, time.Minute)
			v := &streamVendor{}
			gw := queueGateway(t, v, func(p *provider.Provider) { p.MaxRPM = 1 })
			if code, body, _, err := askHeard(gw, a.path, a.body, nil); err != nil || code != 200 {
				t.Fatalf("first: %d %v %s", code, err, body)
			}
			code, body, heard, err := askHeard(gw, a.path, a.body, nil)
			if err != nil || code != 200 || !strings.Contains(body, ": keepalive") || !strings.Contains(body, "queued ok") {
				t.Fatalf("queued = %d %v %q, want 200, keepalives, then the reply", code, err, body)
			}
			first, _ := v.arrived(0)
			asked, ok := v.arrived(1)
			if !ok || heard.IsZero() || !heard.Before(asked) {
				t.Fatalf("the gateway first wrote to the agent at %s, the vendor was asked at %s: nothing reached it while it waited", heard.Format("15:04:05.000"), asked.Format("15:04:05.000"))
			}
			// it waited for the minute: the vendor sees the first late
			// under load (rpmSlack, zero here), so half of it
			if d := asked.Sub(first); d < queueMinute/2 {
				t.Fatalf("the vendor was asked %s after the first, inside the minute", d)
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
		_, _, _, err := askHeard(gw, queueAsks[0].path, queueAsks[0].body, nil)
		first <- err
	}()
	waitSeen(t, v, 1) // the first has the slot
	// the slot is freed once the queued agent has heard something, or
	// after 5s when nothing reaches it
	var once sync.Once
	free := func() { once.Do(func() { close(v.block) }) }
	time.AfterFunc(5*time.Second, free)
	code, body, heard, err := askHeard(gw, queueAsks[0].path, queueAsks[0].body, free)
	if err != nil || code != 200 || !strings.Contains(body, ": keepalive") || !strings.Contains(body, "queued ok") {
		t.Fatalf("queued for a slot = %d %v %q, want 200, keepalives, then the reply", code, err, body)
	}
	if asked, ok := v.arrived(1); !ok || heard.IsZero() || !heard.Before(asked) {
		t.Fatalf("the gateway first wrote to the agent at %s, the vendor was asked at %s: nothing reached it while it waited", heard.Format("15:04:05.000"), asked.Format("15:04:05.000"))
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
			shortMinute(t, queueMinute, time.Minute)
			gw := queueGateway(t, &streamVendor{fail: 10}, func(p *provider.Provider) { p.MaxRPM = 1 })
			if code, body, _, err := askHeard(gw, a.path, a.body, nil); err != nil || code != 200 {
				t.Fatalf("first: %d %v %s", code, err, body)
			}
			code, body, _, err := askHeard(gw, a.path, a.body, nil)
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
	go askHeard(gw, queueAsks[2].path, queueAsks[2].body, nil)
	waitSeen(t, v, 1) // the first has the slot
	code, body, _, err := askHeard(gw, queueAsks[2].path, queueAsks[2].body, nil)
	if err != nil || code != 200 || !strings.Contains(body, ": keepalive") || !strings.Contains(body, "event: error") || !strings.Contains(body, "requests at once is its limit") {
		t.Fatalf("waited out = %d %v %q, want keepalives, then the limit as the stream's error", code, err, body)
	}
}

// A wait shorter than keepHeldAfter sends nothing ahead: turned away, it
// is still a 429 with its Retry-After.
func TestQueuedBrieflyStill429(t *testing.T) {
	shortMinute(t, 10*time.Second, 50*time.Millisecond)
	gw := queueGateway(t, &streamVendor{}, func(p *provider.Provider) { p.MaxRPM = 1 })
	if code, _, _, err := askHeard(gw, queueAsks[0].path, queueAsks[0].body, nil); err != nil || code != 200 {
		t.Fatalf("first: %d %v", code, err)
	}
	code, body, _, err := askHeard(gw, queueAsks[0].path, queueAsks[0].body, nil)
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
