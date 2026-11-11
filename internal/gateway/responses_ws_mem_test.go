package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/yetone/magpie/internal/provider"
)

// A socket turn's body is freed once the turn is over (casyubaji on
// Discord: ~2.8 GB after 6 hours of Codex). Each turn is served from a
// body built in memory, the whole conversation, and the trace keeps its
// last 60 routes: a route that kept the turn's request context (its tool
// scan, for the turn's end) kept the request on it (the inflight's
// unrouted watch), and with it the body, so 60 of Codex's 20 MB
// conversations stayed after every socket had closed.
func TestSocketTurnBodiesFreed(t *testing.T) {
	fresh(t)
	var served atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		n := served.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		id := fmt.Sprintf("resp_up%d", n)
		item := fmt.Sprintf(`{"type":"message","id":"msg_%d","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer %d"}]}`, n, n)
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"`+id+`","object":"response","status":"in_progress","output":[]}}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
			`data: {"type":"response.completed","response":{"id":"`+id+`","object":"response","status":"completed","output":[`+item+`],"usage":{"input_tokens":300,"output_tokens":50,"total_tokens":350}}}`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Responses: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var bodies []weak.Pointer[byte]
	socketTurnBody = func(body []byte) {
		mu.Lock()
		bodies = append(bodies, weak.Make(&body[0]))
		mu.Unlock()
	}
	t.Cleanup(func() { socketTurnBody = nil })

	s, ts := sockServer(t)
	c, _, err := dialSock(t, ts, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	big := `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("x", 256<<10) + `"}]}`
	const turns = 12
	id := ""
	for i := range turns {
		in := userMore
		if i == 0 {
			in = big
		}
		got := c.turn(create(id, in))
		end := last(got)
		if end.Get("type").String() != "response.completed" {
			t.Fatalf("turn %d: %v", i, got)
		}
		id = end.Get("response.id").String()
	}
	c.c.CloseNow()
	// the socket's handler ends after its client goes (drain waits for
	// it); nothing of a turn is kept after, but the trace's routes. A
	// held body is let go by a context.AfterFunc (holdBody, body room),
	// which runs in a goroutine of its own once the turn's context ends,
	// so under load a collection can come before it: collect until each
	// is gone, within a bar far under what a real holder keeps one for
	// (#5250: a stopped timer kept the last turn's request for up to
	// unroutedMost, 2m; unroutedShow is 15s).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.sockets.drain(ctx)
	if ctx.Err() != nil {
		t.Fatal("the socket's handler did not end after its client went")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != turns {
		t.Fatalf("%d turn bodies for %d turns", len(bodies), turns)
	}
	var kept []int
	for bar := time.Now().Add(10 * time.Second); ; {
		runtime.GC()
		kept = kept[:0]
		for i, b := range bodies {
			if b.Value() != nil {
				kept = append(kept, i)
			}
		}
		if len(kept) == 0 || time.Now().After(bar) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(kept) > 0 {
		t.Fatalf("turn bodies %v of %d still kept after the socket's handler ended", kept, turns)
	}
}
