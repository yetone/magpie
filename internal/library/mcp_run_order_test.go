package library

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type orderedRunInput struct {
	io.WriteCloser
	session  *runSession
	fallback chan []byte
	observed chan bool
}

func (w *orderedRunInput) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	if err != nil || !strings.Contains(string(p), `"name":"ask"`) {
		return n, err
	}
	w.session.mu.Lock()
	var request chan []byte
	if len(w.session.streams) > 1 {
		request = w.session.streams[len(w.session.streams)-1]
	}
	w.session.mu.Unlock()
	select {
	case b := <-w.fallback:
		w.fallback <- b
		w.observed <- false
	case b := <-request:
		request <- b
		w.observed <- true
	case <-time.After(10 * time.Second):
		return n, fmt.Errorf("server request was not delivered")
	}
	return n, nil
}

// The request stream exists before the child can answer: otherwise a fast
// roots/list goes to the session stream and the call never receives it (#5433).
func TestRunServerAsksOnCallStreamBeforeSendReturns(t *testing.T) {
	srv, _, _ := runFixture(t)
	_, sid := call(t, srv, "", fmt.Sprintf(runInit, "thread-a"))
	runs.Lock()
	s := runs.sessions[sid]
	runs.Unlock()
	child := s.running()
	fallback := s.open()
	defer s.close(fallback)
	receipt := make(chan bool, 1)
	original := child.in
	child.in = &orderedRunInput{WriteCloser: original, session: s, fallback: fallback, observed: receipt}
	defer func() { child.in = original }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+RunPath+"fake", strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"ask"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", sid)
	type outcome struct {
		response *http.Response
		err      error
	}
	done := make(chan outcome, 1)
	go func() { r, e := http.DefaultClient.Do(req); done <- outcome{r, e} }()
	select {
	case onCall := <-receipt:
		if !onCall {
			t.Fatal("roots/list delivered to session stream instead of call stream")
		}
	case <-ctx.Done():
		t.Fatal("server request not delivered before send returned")
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.response.Body.Close()
	rd := bufio.NewReader(result.response.Body)
	next := func() string {
		t.Helper()
		for {
			l, e := rd.ReadString('\n')
			if e != nil {
				t.Fatal(e)
			}
			if d, ok := strings.CutPrefix(strings.TrimSpace(l), "data: "); ok {
				return d
			}
		}
	}
	if q := next(); !strings.Contains(q, `"roots/list"`) || !strings.Contains(q, `"id":77`) {
		t.Fatalf("first event: %s", q)
	}
	call(t, srv, sid, `{"jsonrpc":"2.0","id":77,"result":{"roots":[]}}`)
	if q := next(); !strings.Contains(q, `"id":5`) || !strings.Contains(q, `roots {\"roots\":[]}`) {
		t.Fatalf("call reply: %s", q)
	}
	select {
	case b := <-fallback:
		t.Fatalf("event on fallback stream: %s", b)
	default:
	}
}
