package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// quietFast makes watch keep a quiet stream's agent alive within
// milliseconds rather than 15s.
func quietFast(t *testing.T) {
	t.Helper()
	keepFast(t, keepaliveGap, 40*time.Millisecond, keepaliveLongest)
	k, w := keepHeldAfter, watchEvery
	keepHeldAfter, watchEvery = 40*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery = k, w })
}

const (
	quietCreated = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-test\",\"status\":\"in_progress\"}}\n\n"
	quietDone    = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-test\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n"
)

func quietDelta(s string) string {
	return "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + s + "\"}\n\n"
}

// quietOn serves a Responses-only provider that sends before, then says
// nothing at all — no event, no comment — until heard is closed (or 3s,
// telling timedOut), then sends after.
func quietOn(t *testing.T, id, before, after string, heard <-chan struct{}) *atomic.Bool {
	t.Helper()
	timedOut := new(atomic.Bool)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, before)
		w.(http.Flusher).Flush()
		select {
		case <-heard:
		case <-time.After(3 * time.Second):
			timedOut.Store(true)
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, after)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: "Fixture", Key: "k", Models: []string{"gpt-test"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	return timedOut
}

// askChat streams a chat completion of model from the gateway over HTTP,
// closing heard at the first keepalive, and tells how long its headers
// took.
func askChat(t *testing.T, s *Server, model string, heard chan struct{}) (int, string, time.Duration) {
	t.Helper()
	return askChatAfter(t, s, model, heard, "")
}

// askChatAfter is askChat closing heard at the first keepalive after after
// (readQuiet).
func askChatAfter(t *testing.T, s *Server, model string, heard chan struct{}, after string) (int, string, time.Duration) {
	t.Helper()
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	began := time.Now()
	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	headers := time.Since(began)
	defer resp.Body.Close()
	return resp.StatusCode, readQuiet(resp.Body, heard, after), headers
}

// readQuiet reads a stream to its end, closing heard at its first
// keepalive once after has come in it. A keepalive may come before the
// vendor's first words as well, the vendor slow to send them, or the try
// slow to read them, with the stream held and nothing of it sent: that is
// the held stream's (TestSilentHeldStreamKeptAlive), and says nothing of a
// quiet after them.
func readQuiet(r io.Reader, heard chan struct{}, after string) string {
	var once sync.Once
	var body strings.Builder
	rd := bufio.NewReader(r)
	for {
		line, err := rd.ReadString('\n')
		body.WriteString(line)
		if strings.HasPrefix(line, ": keepalive") && strings.Contains(body.String(), after) {
			once.Do(func() { close(heard) })
		}
		if err != nil {
			return body.String()
		}
	}
}

// aliveAfter is where body's first keepalive after after is, or -1.
func aliveAfter(body, after string) int {
	i := strings.Index(body, after)
	if i < 0 {
		return -1
	}
	j := strings.Index(body[i:], ": keepalive")
	if j < 0 {
		return -1
	}
	return i + j
}

// A vendor that sent its 200 and response.created, then nothing while it
// reasoned: the stream was held for its first content with nothing sent,
// and with no event to scan, the agent heard nothing — no headers — until
// the vendor spoke again; Cloudflare in front of a remote magpie ended it
// at 125s (#947, jorben's repro). The agent now gets the stream's 200 and
// comments meanwhile, and the reply after them.
func TestSilentHeldStreamKeptAlive(t *testing.T) {
	fresh(t)
	quietFast(t)
	heard := make(chan struct{})
	timedOut := quietOn(t, "fixture", quietCreated, quietDelta("hello")+quietDone, heard)
	code, body, headers := askChat(t, New(), "fixture/gpt-test", heard)
	if timedOut.Load() || headers > 2*time.Second {
		t.Fatalf("the agent heard nothing while the vendor was quiet: headers after %v, body %q", headers, body)
	}
	alive := strings.Index(body, ": keepalive")
	if code != 200 || alive < 0 || !strings.Contains(body[alive:], "hello") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("%d %q", code, body)
	}
}

// One quiet mid-reply, its first words sent: a translated reply's client
// was kept alive only as its vendor sent something, so a vendor that said
// nothing at all left it to the proxy's timeout, which reset the stream
// (#947, "INTERNAL_ERROR; received from peer"). The keepalive this is
// about is the one after them (readQuiet).
func TestQuietStreamMidReplyKeptAlive(t *testing.T) {
	fresh(t)
	quietFast(t)
	heard := make(chan struct{})
	timedOut := quietOn(t, "fixture", quietCreated+quietDelta("hel"), quietDelta("lo")+quietDone, heard)
	code, body, _ := askChatAfter(t, New(), "fixture/gpt-test", heard, "hel")
	alive := aliveAfter(body, "hel")
	if timedOut.Load() || code != 200 || alive < 0 || !strings.Contains(body[alive:], `"lo"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("timed out %v: %d %q", timedOut.Load(), code, body)
	}
}

// Kept alive while held, the try is still one another member may answer:
// the comments say nothing of it, and a failure before its first content
// goes to the next, whose reply follows in the same stream.
func TestSilentHeldStreamStillFailsOver(t *testing.T) {
	fresh(t)
	quietFast(t)
	heard := make(chan struct{})
	quietOn(t, "a", quietCreated, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"overloaded\"}}\n\n", heard)
	b := &scripted{replies: []reply{{200, "text/event-stream", quietCreated + quietDelta("from b") + quietDone}}}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"gpt-test"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	refusalGroup(t, "a/gpt-test", "b/gpt-test")
	code, body, _ := askChat(t, New(), "G", heard)
	alive := strings.Index(body, ": keepalive")
	if code != 200 || alive < 0 || !strings.Contains(body[alive:], "from b") || strings.Contains(body, "overloaded") || b.n != 1 {
		t.Fatalf("%d %q (b %d)", code, body, b.n)
	}
	if strings.Count(body, `"role":"assistant"`) > 1 {
		t.Fatalf("one reply in the stream: %q", body)
	}
}

// A vendor quiet for longer than keepaliveLongest is no longer covered
// for: the comments stop, for a stuck stream to end at a timeout.
func TestQuietKeptAliveOnlySoLong(t *testing.T) {
	fresh(t)
	quietFast(t)
	keepaliveLongest = 200 * time.Millisecond
	never := make(chan struct{})
	quietOn(t, "fixture", quietCreated+quietDelta("hel"), quietDelta("lo")+quietDone, never)
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"fixture/gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body) // the vendor goes on after 3s
	// counted from its first words, as readQuiet: the held stream's before
	// them are kept for 200ms of their own
	body := string(b)
	i, j := strings.Index(body, "hel"), strings.Index(body, `"lo"`)
	if i < 0 || j < i {
		t.Fatalf("%q", body)
	}
	if n := strings.Count(body[i:j], ": keepalive"); n == 0 || n > 8 {
		t.Fatalf("%d keepalives in 3s, kept for 200ms: %q", n, body)
	}
}

// Every protocol's stream, relayed as it is or translated, is kept alive
// the same way while its vendor is quiet: held before its first content,
// and between two lines once it has passed (#947).
func TestQuietStreamsKeptAliveEveryProtocol(t *testing.T) {
	anthropicStart := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"
	anthropicText := func(s string) string {
		return "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + s + "\"}}\n\n"
	}
	anthropicEnd := "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	anthropicBlock := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	chatText := func(s string) string {
		return "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + s + "\"},\"finish_reason\":null}]}\n\n"
	}
	chatRole := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n"
	chatEnd := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	for _, c := range []struct {
		name, path, body string
		up               provider.Protocol
		start, text, end func(string) string
	}{
		{"responses", "/v1/responses", `{"model":"q/m","stream":true,"input":"hi"}`, provider.Responses,
			func(string) string { return quietCreated }, quietDelta, func(string) string { return quietDone }},
		{"anthropic", "/v1/messages", `{"model":"q/m","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, provider.Anthropic,
			func(string) string { return anthropicStart + anthropicBlock }, anthropicText, func(string) string { return anthropicEnd }},
		{"chat", "/v1/chat/completions", `{"model":"q/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, provider.Chat,
			func(string) string { return chatRole }, chatText, func(string) string { return chatEnd }},
		{"anthropic from chat", "/v1/messages", `{"model":"q/m","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, provider.Chat,
			func(string) string { return chatRole }, chatText, func(string) string { return chatEnd }},
	} {
		for _, mid := range []bool{false, true} {
			t.Run(c.name+map[bool]string{false: " held", true: " mid-reply"}[mid], func(t *testing.T) {
				fresh(t)
				quietFast(t)
				heard := make(chan struct{})
				before, after := c.start(""), c.text("lo")+c.end("")
				said := "" // what the agent has before the quiet
				if mid {
					before += c.text("hel")
					said = "hel"
				}
				var timedOut atomic.Bool
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, before)
					w.(http.Flusher).Flush()
					select {
					case <-heard:
					case <-time.After(3 * time.Second):
						timedOut.Store(true)
					case <-r.Context().Done():
						return
					}
					io.WriteString(w, after)
				}))
				t.Cleanup(up.Close)
				p := provider.Provider{ID: "q", Name: "Q", Key: "k", Models: []string{"m"}}
				switch c.up {
				case provider.Responses:
					p.Responses = up.URL + "/v1"
				case provider.Anthropic:
					p.Anthropic = up.URL
				case provider.Chat:
					p.Chat = up.URL + "/v1"
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				gw := httptest.NewServer(New().Handler())
				t.Cleanup(gw.Close)
				resp, err := http.Post(gw.URL+c.path, "application/json", strings.NewReader(c.body))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				b := readQuiet(resp.Body, heard, said)
				alive := aliveAfter(b, said)
				if timedOut.Load() || resp.StatusCode != 200 || alive < 0 || !strings.Contains(b[alive:], `"lo"`) {
					t.Fatalf("timed out %v: %d %q", timedOut.Load(), resp.StatusCode, b)
				}
			})
		}
	}
}

// A vendor slow to answer at all gets its stream's agent kept alive from
// watch's goroutine while the try, once the vendor answers, copies the
// vendor's headers in: keepAlive reads only magpie's notes until the try
// has its status, so the two never touch one map (a race, and in a build
// without -race "concurrent map iteration and map write").
func TestKeepAliveBeforeTheVendorsHeaders(t *testing.T) {
	quietFast(t)
	rec := httptest.NewRecorder()
	h := newHoldWriter(rec, true)
	h.streams, h.alive = true, &keptAlive{proto: provider.Responses}
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	noteMember(h, r, provider.Provider{ID: "q"}, "m")
	end := h.watch()
	// the vendor's headers come in as passthrough copies them, the first
	// comment going out meanwhile
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; ; i++ {
		h.Header().Set("X-Vendor", strconv.Itoa(i))
		h.mu.Lock()
		sent := h.alive.sent
		h.mu.Unlock()
		if sent && i > 100 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Microsecond)
	}
	end()
	if !h.alive.sent {
		t.Fatal("no comment went out")
	}
	if got := rec.Header().Get(providerHeader); got != "q" {
		t.Fatalf("the stream's headers before its reply lack magpie's notes: %q", got)
	}
	if !strings.Contains(rec.Body.String(), ": keepalive") {
		t.Fatalf("body: %q", rec.Body.String())
	}
}
