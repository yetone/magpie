package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	var made, freed atomic.Int64
	socketTurnBody = func(body []byte) {
		made.Add(1)
		runtime.AddCleanup(&body[0], func(struct{}) { freed.Add(1) }, struct{}{})
	}
	t.Cleanup(func() { socketTurnBody = nil })

	_, ts := sockServer(t)
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
	if made.Load() != turns {
		t.Fatalf("%d turn bodies for %d turns", made.Load(), turns)
	}
	// the socket's handler ends after its client goes; nothing of a
	// turn is kept after, but the trace's routes
	deadline := time.Now().Add(10 * time.Second)
	for freed.Load() < turns && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	if n := freed.Load(); n < turns {
		t.Fatalf("%d of %d socket turn bodies still kept after the socket closed", turns-n, turns)
	}
}
