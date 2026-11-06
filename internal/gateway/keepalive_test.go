package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keepFast makes the keepalives of a test's streams come at its pace.
func keepFast(t *testing.T, gap, every, longest time.Duration) {
	t.Helper()
	g, e, l := keepaliveGap, keepaliveEvery, keepaliveLongest
	keepaliveGap, keepaliveEvery, keepaliveLongest = gap, every, longest
	t.Cleanup(func() { keepaliveGap, keepaliveEvery, keepaliveLongest = g, e, l })
}

// TestTranslatedStreamKeepsClientAlive: a provider that says "progress",
// then only ": keepalive" every 20ms for 1.5s, then finishes, translated
// for a client whose idle timeout is 500ms (#436, congee949's repro).
// Without magpie's keepalives the client hears nothing for three of its
// timeouts; 500ms, not 150ms, is room for a loaded machine's scheduler
// (#2169, #2711, #2737). The client must hear something it counts all along: Codex's
// timeout counts events, never a comment, so a Responses or Anthropic
// client is kept alive with events, a Chat one with comments.
//
// An Anthropic client's pings are spaced pingEvery apart (here 100ms),
// with comments between: Claude Code counts only 30 pings in a row as
// life, so a ping on every provider keepalive (here each 20ms, ~75 in
// all) spent them early and let its watchdog fire later (youyo.p).
func TestTranslatedStreamKeepsClientAlive(t *testing.T) {
	keepFast(t, 10*time.Millisecond, keepaliveEvery, keepaliveLongest)
	const quietFor, idleFor = 1500 * time.Millisecond, 500 * time.Millisecond
	pe := pingEvery
	pingEvery = 100 * time.Millisecond
	t.Cleanup(func() { pingEvery = pe })
	for _, c := range []struct {
		name, path, body, done, ping string // done: the reply's last event
		upChat                       bool
		comments                     bool // the client counts a comment as life
	}{
		{"chat_to_responses", "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`,
			`"type":"response.completed"`, `"type":"response.in_progress"`, true, false},
		{"chat_to_anthropic", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
			`"type":"message_stop"`, `"type":"ping"`, true, false},
		{"responses_to_chat", "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"data: [DONE]", ": keepalive", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			var upPings atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				f := w.(http.Flusher)
				if c.upChat {
					io.WriteString(w, "data: "+`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"progress"},"finish_reason":null}]}`+"\n\n")
				} else {
					io.WriteString(w, "event: response.created\ndata: "+`{"type":"response.created","response":{"id":"r1","model":"m1"}}`+"\n\n")
					io.WriteString(w, "event: response.output_text.delta\ndata: "+`{"type":"response.output_text.delta","delta":"progress"}`+"\n\n")
				}
				f.Flush()
				tick := time.NewTicker(20 * time.Millisecond)
				defer tick.Stop()
				end := time.NewTimer(quietFor)
				defer end.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-tick.C:
						if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
							return
						}
						upPings.Add(1)
						f.Flush()
					case <-end.C:
						if c.upChat {
							io.WriteString(w, "data: "+`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
						} else {
							io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","output":[]}}`+"\n\n")
						}
						f.Flush()
						return
					}
				}
			}))
			defer up.Close()
			p := provider.Provider{ID: "fake", Name: "Fake", Key: "fixture", Models: []string{"m1"}}
			if c.upChat {
				p.Chat = up.URL + "/v1"
			} else {
				p.Responses = up.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(New().Handler())
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+c.path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			lines := make(chan string, 128)
			go func() {
				defer close(lines)
				sc := bufio.NewScanner(res.Body)
				for sc.Scan() {
					lines <- sc.Text()
				}
			}()
			idle := time.NewTimer(idleFor)
			defer idle.Stop()
			// the client's idle timeout, until it has the whole reply. What
			// the gateway does after that before the stream closes (the
			// usage ledger, affinity, served) is no wait a client's idle
			// timeout counts: Codex hangs up on response.completed, and the
			// clients that read on to the close wait minutes, not idleFor.
			// Then the stream closes within closeAfterLast, or the test
			// fails rather than hangs.
			const closeAfterLast = 5 * time.Second
			wait := idle.C
			timedOut, completed, unclosed, pings := false, false, false, 0
			// openai-go v2 (Crush) reads every blank line as an event and
			// fails on one with no data: "unexpected end of JSON input"
			data, empty := false, 0
		read:
			for {
				select {
				case line, ok := <-lines:
					if !ok {
						break read
					}
					if strings.Contains(line, c.ping) {
						pings++
					}
					if line == "" {
						if !data {
							empty++
						}
						data = false
					} else if strings.HasPrefix(line, "data:") {
						data = true
					}
					if strings.Contains(line, c.done) && !completed {
						completed, wait = true, time.After(closeAfterLast)
					}
					// what the client's idle timeout counts
					if strings.HasPrefix(line, "data:") || (c.comments && strings.HasPrefix(line, ":")) {
						idle.Reset(idleFor)
					}
				case <-wait:
					timedOut, unclosed = !completed, completed
					break read
				}
			}
			cancel()
			if unclosed {
				t.Fatalf("the stream didn't close within %v of its last event", closeAfterLast)
			}
			t.Logf("upstream_keepalives=%d downstream_keepalives=%d idle_timeout=%v completed=%v", upPings.Load(), pings, timedOut, completed)
			if timedOut || !completed || pings < 3 {
				t.Fatal("the client wasn't kept alive while the provider was")
			}
			if empty > 0 {
				t.Fatalf("%d events with no data", empty)
			}
			// one each pingEvery over quietFor, and room for the first
			// and a slow scheduler's catch-up
			if most := int(quietFor/pingEvery) + 5; c.ping == `"type":"ping"` && pings > most {
				t.Fatalf("%d pings in %v, want at most %d (one each %v)", pings, quietFor, most, pingEvery)
			}
		})
	}
}

// TestTranslatedSilenceKeptAliveForABound: a provider that begins its
// reply and then says nothing at all, not even a comment, as a relay does
// that sends the rest of a long reply in one burst at its end (youyo.p on
// Discord: Claude Code on claude-opus-5-5 at effort max, a custom
// provider's /v1/responses, the request canceled some 357s in). The
// client is kept alive in its own protocol, with what its idle timeout
// counts: a ping for an Anthropic client, a response.in_progress for a
// Responses one, a comment for a Chat one. It is kept so only for
// translatedQuietLongest of silence (here 1s), after which the client's
// own idle timeout ends the wait. Before, the client was sent only
// comments, which neither Claude Code's event watchdog nor Codex counts,
// for keepaliveLongest.
func TestTranslatedSilenceKeptAliveForABound(t *testing.T) {
	const every, bound = 20 * time.Millisecond, time.Second
	keepFast(t, 10*time.Millisecond, every, 100*time.Millisecond)
	k, w, q, p, l := keepHeldAfter, watchEvery, agentQuietMost, pingEvery, translatedQuietLongest
	keepHeldAfter, watchEvery, agentQuietMost, pingEvery, translatedQuietLongest = 30*time.Millisecond, 10*time.Millisecond, 200*time.Millisecond, every, bound
	t.Cleanup(func() { keepHeldAfter, watchEvery, agentQuietMost, pingEvery, translatedQuietLongest = k, w, q, p, l })
	for _, c := range []struct {
		name, path, body, alive string
		upChat                  bool // the provider speaks Chat, else Responses
	}{
		{"anthropic_from_responses", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, `data: {"type":"ping"}`, false},
		{"responses_from_chat", "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`, `"type":"response.in_progress"`, true},
		{"chat_from_responses", "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`, ": keepalive", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			release := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				// the reply begins, then nothing comes on the open stream
				if c.upChat {
					io.WriteString(w, "data: "+`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"progress"},"finish_reason":null}]}`+"\n\n")
				} else {
					io.WriteString(w, "event: response.created\ndata: "+`{"type":"response.created","response":{"id":"r1","model":"m1"}}`+"\n\n")
					io.WriteString(w, "event: response.output_text.delta\ndata: "+`{"type":"response.output_text.delta","delta":"progress"}`+"\n\n")
				}
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer up.Close()
			defer close(release)
			p := provider.Provider{ID: "fake", Name: "Fake", Key: "fixture", Models: []string{"m1"}}
			if c.upChat {
				p.Chat = up.URL + "/v1"
			} else {
				p.Responses = up.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(New().Handler())
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+c.path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			lines := make(chan string, 1024)
			go func() {
				defer close(lines)
				sc := bufio.NewScanner(res.Body)
				for sc.Scan() {
					lines <- sc.Text()
				}
			}()
			// Kept alive: the client hears 10 of what it counts, which at
			// one each 20ms takes 200ms of the 1s bound. A loaded machine
			// may take longer, so it is waited for, up to 20s.
			count := func(until <-chan time.Time, enough int) int {
				n := 0
				for {
					select {
					case line, ok := <-lines:
						if !ok {
							return n
						}
						if strings.Contains(line, c.alive) {
							if n++; n == enough {
								return n
							}
						}
					case <-until:
						return n
					}
				}
			}
			if n := count(time.After(20*time.Second), 10); n < 10 {
				t.Fatalf("a silent provider's client heard %d of %q, want it kept alive", n, c.alive)
			}
			// Let go: one each 20ms for the bound is at most ~55 more
			// (the bound, and the held stream's 100ms of comments after
			// it). Unbounded, the 4s after would hold ~200.
			n := count(time.After(4*time.Second), -1)
			t.Logf("%d more of %q after the first 10", n, c.alive)
			if n > 100 {
				t.Fatalf("the client was kept alive %d more times, long past the provider's %v of silence", n, bound)
			}
		})
	}
}

// TestRelayKeepsClientAlive: a relayed reply (the Claude subscription's
// bridge, Kiro, Cursor, …) that goes quiet keeps its client alive every
// keepaliveEvery, but only for keepaliveLongest since its last event.
func TestRelayKeepsClientAlive(t *testing.T) {
	keepFast(t, keepaliveGap, 20*time.Millisecond, 200*time.Millisecond)
	events := make(chan Event)
	go func() {
		events <- Event{Kind: KStart, MsgID: "m1"}
		events <- Event{Kind: KText, Text: "progress"}
		time.Sleep(150 * time.Millisecond) // kept alive
		events <- Event{Kind: KText, Text: " more"}
		time.Sleep(600 * time.Millisecond) // kept alive for 200ms only
		events <- Event{Kind: KStop, Stop: "end_turn"}
		close(events)
	}()
	rec := httptest.NewRecorder()
	req := &Request{Model: "m1", Stream: true}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	code, failed := relay(rec, r, provider.Anthropic, "Bridge", req, events, &Usage{}, func() {}, nil, func(string, string, bool) {})
	if code != 200 || failed != "" {
		t.Fatalf("relay: %d %q", code, failed)
	}
	body := rec.Body.String()
	parts := strings.SplitN(body, "more", 2)
	if len(parts) != 2 || !strings.Contains(body, "message_stop") {
		t.Fatalf("reply incomplete:\n%s", body)
	}
	// a ping or, within pingEvery of the last, a comment
	alive := func(s string) int { return strings.Count(s, `"type":"ping"`) + strings.Count(s, ": keepalive") }
	before, after := alive(parts[0]), alive(parts[1])
	t.Logf("pings in a 150ms gap: %d, in a 600ms gap (200ms kept): %d", before, after)
	if before < 3 {
		t.Fatalf("a quiet reply wasn't kept alive: %d pings in 150ms", before)
	}
	if after < 3 || after > 12 {
		t.Fatalf("a reply quiet past keepaliveLongest was kept alive on: %d pings", after)
	}
}

// TestTraceKeepsInflight: a request still going stays in the trace while
// more than traceKeep come and finish after it (#436).
func TestTraceKeepsInflight(t *testing.T) {
	s := New()
	pending := s.trace.begin(Route{Model: "pending", Time: time.Now()})
	for i := 0; i < traceKeep+1; i++ {
		r := s.trace.begin(Route{Model: "later", Time: time.Now()})
		s.trace.update(r, func(r *Route) { r.Done, r.Status = true, 200 })
	}
	state := s.Trace(context.Background(), 0, 0)
	found := false
	for _, r := range state.Routes {
		found = found || r.ID == pending.ID
	}
	if !found || len(state.Routes) != traceKeep {
		t.Fatalf("pending visible %v, %d routes kept", found, len(state.Routes))
	}
	// more going than twice traceKeep: the oldest go after all
	for i := 0; i < 2*traceKeep; i++ {
		s.trace.begin(Route{Model: "going", Time: time.Now()})
	}
	if n := len(s.Trace(context.Background(), 0, 0).Routes); n != 2*traceKeep {
		t.Fatalf("%d routes kept, want %d", n, 2*traceKeep)
	}
}

// Anthropic's SDKs fail a stream with any event before message_start
// ("Unexpected event order, got ping before message_start"), and Anthropic
// sends none, so a keepalive before it is a comment; after it a ping, at
// most one each pingEvery, comments in between.
func TestAnthropicKeepaliveBeforeStartIsAComment(t *testing.T) {
	was := pingEvery
	pingEvery = time.Hour
	t.Cleanup(func() { pingEvery = was })
	rec := httptest.NewRecorder()
	e := &anthropicEncoder{w: newSSEWriter(rec), model: "m"}
	e.keepalive()
	e.keepalive()
	if body := rec.Body.String(); strings.Contains(body, "event:") || !strings.Contains(body, ": keepalive") {
		t.Fatalf("before message_start: %q", body)
	}
	e.event(Event{Kind: KStart, MsgID: "msg_1", Model: "m"})
	e.keepalive()
	e.keepalive()
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimPrefix(body[strings.Index(body, "event:"):], "event: "), "message_start") {
		t.Fatalf("an event before message_start: %q", body)
	}
	if n := strings.Count(body, `"type":"ping"`); n != 1 {
		t.Fatalf("%d pings within one pingEvery after message_start: %q", n, body)
	}
}

// TestQueuedUpstreamKeepsResponsesClientAlive: a Chat provider that sends
// its 200 and then only SSE comments, no event of its reply, for far
// longer than agentQuietMost, keepaliveLongest and translatedQuietLongest
// before it answers. The Qoder plugin does this while Qoder's queue (code
// 10605) holds a request, for up to the hour Qoder's own client waits
// (plugins#807). A Responses client, whose idle timeout counts events and
// never a comment (Codex, 300s), must hear an event within its timeout
// all along: the held stream flows at agentQuietMost, and each comment
// after is response.in_progress, the provider's comments renewing
// translatedQuietLongest. Times are scaled: Codex's 300s is idleFor,
// agentQuietMost's 4 minutes is 200ms.
func TestQueuedUpstreamKeepsResponsesClientAlive(t *testing.T) {
	keepFast(t, 10*time.Millisecond, 20*time.Millisecond, 100*time.Millisecond)
	k, w, q, l := keepHeldAfter, watchEvery, agentQuietMost, translatedQuietLongest
	keepHeldAfter, watchEvery, agentQuietMost, translatedQuietLongest = 30*time.Millisecond, 10*time.Millisecond, 200*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery, agentQuietMost, translatedQuietLongest = k, w, q, l })
	const idleFor = 600 * time.Millisecond
	fresh(t)
	release := make(chan struct{})
	var comments atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		f.Flush()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if _, err := io.WriteString(w, ": queued in Qoder\n\n"); err != nil {
					return
				}
				comments.Add(1)
				f.Flush()
			case <-release:
				io.WriteString(w, "data: "+`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`+"\n\n")
				io.WriteString(w, "data: "+`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
				f.Flush()
				return
			}
		}
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "fixture", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New().Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"fake/m1","stream":true,"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := make(chan string, 4096)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	// the queue holds the request until the client has heard 40 events of
	// its own (40 x 20ms, several times every bound above); each must come
	// within idleFor of the one before
	idle := time.NewTimer(idleFor)
	defer idle.Stop()
	events, released, completed, text := 0, false, false, false
	for !completed {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the stream ended after %d events, completed=%v", events, completed)
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			idle.Reset(idleFor)
			events++
			if strings.Contains(line, `"delta":"hello"`) {
				text = true
			}
			if strings.Contains(line, `"type":"response.completed"`) {
				completed = true
			}
			if events >= 40 && !released {
				released = true
				close(release)
			}
		case <-idle.C:
			t.Fatalf("the client heard no event for %v after %d (upstream comments %d)", idleFor, events, comments.Load())
		}
	}
	if !released || !text {
		t.Fatalf("released=%v text=%v after %d events", released, text, events)
	}
	t.Logf("events=%d upstream_comments=%d", events, comments.Load())
}
