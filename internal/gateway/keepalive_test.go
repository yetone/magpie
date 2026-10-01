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
// then only ": keepalive" every 20ms for half a second, then finishes,
// translated for a client whose idle timeout is 150ms (#436, congee949's
// repro). The client must hear something it counts all along: Codex's
// timeout counts events, never a comment, so a Responses or Anthropic
// client is kept alive with events, a Chat one with comments.
func TestTranslatedStreamKeepsClientAlive(t *testing.T) {
	keepFast(t, 10*time.Millisecond, keepaliveEvery, keepaliveLongest)
	for _, c := range []struct {
		name, path, body, done, ping string
		upChat                       bool
		comments                     bool // the client counts a comment as life
	}{
		{"chat_to_responses", "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`,
			`"type":"response.completed"`, `"type":"response.in_progress"`, true, false},
		{"chat_to_anthropic", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
			`"type":"message_stop"`, `"type":"ping"`, true, false},
		{"responses_to_chat", "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			`"finish_reason":"stop"`, ": keepalive", false, true},
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
				end := time.NewTimer(500 * time.Millisecond)
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
			idle := time.NewTimer(150 * time.Millisecond)
			defer idle.Stop()
			timedOut, completed, pings := false, false, 0
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
					if strings.Contains(line, c.done) {
						completed = true
					}
					// what the client's idle timeout counts
					if strings.HasPrefix(line, "data:") || (c.comments && strings.HasPrefix(line, ":")) {
						idle.Reset(150 * time.Millisecond)
					}
				case <-idle.C:
					timedOut = true
					break read
				}
			}
			cancel()
			t.Logf("upstream_keepalives=%d downstream_keepalives=%d idle_timeout=%v completed=%v", upPings.Load(), pings, timedOut, completed)
			if timedOut || !completed || pings < 3 {
				t.Fatal("the client wasn't kept alive while the provider was")
			}
		})
	}
}

// TestTranslatedSilenceStillTimesOut: a provider that says nothing at all
// isn't covered for; the client's idle timeout still ends the wait.
func TestTranslatedSilenceStillTimesOut(t *testing.T) {
	keepFast(t, 10*time.Millisecond, keepaliveEvery, keepaliveLongest)
	fresh(t)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"progress"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer up.Close()
	defer close(release)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "fixture", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New().Handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"fake/m1","stream":true,"input":"hi"}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if n := strings.Count(string(b), `"type":"response.in_progress"`); n > 1 {
		t.Fatalf("a silent provider was covered for: %d in_progress events\n%s", n, b)
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
	code, failed := relay(rec, r, provider.Anthropic, "Bridge", req, events, &Usage{}, func() {}, func(string, string, bool) {})
	if code != 200 || failed != "" {
		t.Fatalf("relay: %d %q", code, failed)
	}
	body := rec.Body.String()
	parts := strings.SplitN(body, "more", 2)
	if len(parts) != 2 || !strings.Contains(body, "message_stop") {
		t.Fatalf("reply incomplete:\n%s", body)
	}
	before, after := strings.Count(parts[0], `"type":"ping"`), strings.Count(parts[1], `"type":"ping"`)
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
