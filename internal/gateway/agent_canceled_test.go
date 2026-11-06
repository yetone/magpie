package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// TestAgentCanceledIsNoError: an agent that cancels its request while the
// vendor is still answering — a person stopping the reply, or Claude
// Desktop asking again with the next message sent mid-reply — saw no
// error. The usage log keeps it as 499 "the agent canceled the request",
// counted as canceled, not failed; the Routing page's errors leave it out;
// and its request and its Recent call say canceled, for the pages to show
// it as no failure.
func TestAgentCanceledIsNoError(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "", chatOK}}, hang: make(chan struct{}), hanging: make(chan struct{})}
	scriptedOn(t, "a", provider.Chat, a)
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	// the agent leaves while a is asked, and a answers only once the
	// gateway is done with the request
	go func() {
		select {
		case <-a.hanging:
			cancel()
		case <-served:
		}
		select {
		case <-served:
		case <-time.After(10 * time.Second):
		}
		close(a.hang)
	}()
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx))
	close(served)

	s.trace.mu.Lock()
	r := *s.trace.routes[len(s.trace.routes)-1]
	totals := s.trace.totals
	s.trace.mu.Unlock()
	if !r.Done || r.Status != usage.StatusCanceled || r.Error != usage.AgentCanceled || !r.Canceled {
		t.Fatalf("route done %v status %d error %q canceled %v, want a done 499 the agent canceled", r.Done, r.Status, r.Error, r.Canceled)
	}
	if totals.Requests != 1 || totals.Errors != 0 {
		t.Errorf("Routing totals %+v, want 1 request and no error", totals)
	}
	s.mu.Lock()
	var call Call
	if n := len(s.recent); n > 0 {
		call = s.recent[n-1]
	}
	s.mu.Unlock()
	if call.Status != usage.StatusCanceled || !call.Canceled {
		t.Errorf("Recent call status %d canceled %v, want 499 canceled", call.Status, call.Canceled)
	}
	var recs []usage.Record
	usage.Visit(time.Time{}, func(rec usage.Record) { recs = append(recs, rec) })
	if len(recs) != 1 || !recs[0].Canceled() || recs[0].Failed() {
		t.Fatalf("usage log %+v, want the one call canceled, not failed", recs)
	}
	if recs[0].ErrType != usage.CanceledErrType {
		t.Errorf("the usage log's error type %q, want %q: the record keeps that it was the agent's cancel", recs[0].ErrType, usage.CanceledErrType)
	}
}

// TestCodexGoneMidReplyIsNoFailure: Codex signed in to ChatGPT, its turn
// relayed to the backend as it came, gone mid-reply (Esc, or its window
// closed) is logged 499 as a routed request is (#4866), and like one is the
// agent's cancel: on its route, in the Routing totals and on its usage
// record, canceled and no failure.
func TestCodexGoneMidReplyIsNoFailure(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header()["Content-Type"] = nil // as the ChatGPT backend sends it
		io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"wor"}`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	})
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	// Codex leaves as the reply's first bytes reach it
	s.Handler().ServeHTTP(&cancelOnWrite{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}, req)

	r := lastRoute(s)
	s.trace.mu.Lock()
	totals := s.trace.totals
	s.trace.mu.Unlock()
	if r.Status != usage.StatusCanceled || !r.Canceled || totals.Errors != 0 {
		t.Fatalf("route %d %q canceled %v, Routing errors %d: want the agent's cancel, no error", r.Status, r.Error, r.Canceled, totals.Errors)
	}
	var recs []usage.Record
	usage.Visit(time.Time{}, func(rec usage.Record) { recs = append(recs, rec) })
	if len(recs) != 1 {
		t.Fatalf("%d usage records, want the one call", len(recs))
	}
	if rec := recs[0]; !rec.Canceled() || rec.Failed() || rec.ErrType != usage.CanceledErrType {
		t.Fatalf("usage record %d %q type %q: canceled %v failed %v, want the agent's cancel (%s), not failed",
			rec.Status, rec.Error, rec.ErrType, rec.Canceled(), rec.Failed(), usage.CanceledErrType)
	}
}

// TestCanceledTellsTheAgentFromMagpiesLimit: magpie's own limit running out
// on a request it asked of itself is logged 499 as well, and stays a
// failure (#2318); only the agent's own cancel is told as canceled.
func TestCanceledTellsTheAgentFromMagpiesLimit(t *testing.T) {
	limited, stop := sideDeadline(context.Background(), "search", time.Hour, time.Millisecond)
	defer stop()
	<-limited.Done()
	if status, msg := canceled(limited); status != usage.StatusCanceled || usage.CanceledBy(status, msg) {
		t.Errorf("magpie's own limit logged %d %q as the agent's cancel", status, msg)
	}
	left, leave := context.WithCancel(context.Background())
	leave()
	if status, msg := canceled(left); !usage.CanceledBy(status, msg) {
		t.Errorf("the agent's cancel logged %d %q, not as canceled", status, msg)
	}
}

// TestHistoryTellsCanceledRoutes: a day's history kept before routes said
// canceled reads a route its agent canceled as canceled, and one magpie's
// own limit ended as a failure, so past days count as the live trace does.
func TestHistoryTellsCanceledRoutes(t *testing.T) {
	routes := appendRoute(nil, []byte(`{"id":1,"time":"2026-10-27T17:26:24+08:00","agent":"claude-desktop","model":"copilot/claude-opus-5.5","provider":"copilot","order":[],"tries":[],"done":true,"status":499,"error":"the agent canceled the request","ms":27000}`))
	routes = appendRoute(routes, []byte(`{"id":2,"time":"2026-10-27T17:30:00+08:00","agent":"claude-desktop","model":"copilot/claude-opus-5.5","provider":"copilot","order":[],"tries":[],"done":true,"status":499,"error":"magpie stopped waiting: its search had no answer in 2m0s","ms":120000}`))
	if len(routes) != 2 || !routes[0].Canceled || routes[1].Canceled {
		var said []bool
		for _, r := range routes {
			said = append(said, r.Canceled)
		}
		t.Fatalf("history read canceled %v, want [true false]: the agent's cancel, then magpie's own limit", said)
	}
}

// The route-events feed (#3387) tells a request its agent canceled, and
// its last try, as no failure, as the Routing page and the usage log
// count it; magpie's own limit on a request it asked of itself, logged 499
// too, stays one (#2318).
func TestRouteEventsCanceledIsNoFailure(t *testing.T) {
	try := Try{Status: usage.StatusCanceled, Error: usage.AgentCanceled, Done: true}
	r := &Route{Done: true, Status: usage.StatusCanceled, Error: usage.AgentCanceled, Tries: []Try{try}}
	if a := activityOf(r); a.Failed {
		t.Errorf("a done route its agent canceled is told failed: %+v", a)
	}
	r.Done = false
	if a := activityOf(r); a.Failed {
		t.Errorf("its last try, canceled by the agent, is told failed: %+v", a)
	}
	own := &Route{Done: true, Status: usage.StatusCanceled, Error: "magpie stopped waiting: its search had no answer in 2m0s"}
	if a := activityOf(own); !a.Failed {
		t.Errorf("magpie's own limit is told as no failure: %+v", a)
	}
}
