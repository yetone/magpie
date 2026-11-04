package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// routeMoment reads what a request's route says now: the bytes received
// and when content last came, under the trace's lock the gateway writes
// them under.
func routeMoment(s *Server, model string) (bytes, contentAt int64, found bool) {
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	for _, rt := range s.trace.routes {
		if rt.Model == model {
			return rt.Bytes, rt.ContentAt, true
		}
	}
	return 0, 0, false
}

// A real request through the whole gateway, its vendor streaming slowly:
// while it is out its route has the bytes the client has had, once it
// ends exactly the bytes the client received, and the provider and model
// that answered have learned from it.
func TestLiveCountsARealStream(t *testing.T) {
	freshRates(t)
	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5,\"total_tokens\":8}}\n\n",
		"data: [DONE]\n\n",
	}
	// the vendor stops after the first event until the test has looked
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i, c := range chunks {
			if _, err := io.WriteString(w, c); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if i == 0 {
				<-release
			}
		}
	}))
	defer up.Close()
	defer func() {
		// let the vendor go on a failure half way, or httptest can't close
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	if err := provider.Save(provider.Provider{ID: "live", Name: "Live", Chat: up.URL + "/v1", Key: "k", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}

	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"live/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("content-type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, req)
	}()

	// wait for the first bytes to reach the client's route
	var midBytes, midContentAt int64
	deadline := time.Now().Add(5 * time.Second)
	for {
		if bytes, contentAt, ok := routeMoment(s, "live/m"); ok && bytes > 0 {
			midBytes, midContentAt = bytes, contentAt
			break
		}
		if time.Now().After(deadline) {
			close(release)
			_, _, found := routeMoment(s, "live/m")
			t.Fatalf("a request whose first bytes arrived has no progress on its route (found=%v)", found)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if midBytes != int64(len(chunks[0])) {
		t.Fatalf("mid-stream bytes are %d, the client got %d", midBytes, len(chunks[0]))
	}
	if midContentAt == 0 {
		t.Fatal("content arrived but the route has no content time")
	}

	close(release)
	<-done

	bytes, _, ok := routeMoment(s, "live/m")
	if !ok {
		t.Fatal("the route is gone")
	}
	if want := int64(rec.Body.Len()); bytes != want {
		t.Fatalf("counted %d bytes, the client received %d", bytes, want)
	}
	if want := int64(len(strings.Join(chunks, ""))); bytes != want {
		t.Fatalf("counted %d bytes, the upstream wrote %d", bytes, want)
	}

	s.trace.mu.Lock()
	rt := s.trace.routes[len(s.trace.routes)-1]
	done2, output := rt.Done, rt.Output
	s.trace.mu.Unlock()
	if !done2 {
		t.Fatal("the route is still open after the reply ended")
	}
	// the vendor's 5, not an estimate
	if output != 5 {
		t.Fatalf("the route says %d output tokens, the vendor said 5", output)
	}

	liveRates.mu.Lock()
	learned := liveRates.m["live/m"]
	liveRates.mu.Unlock()
	if learned.seen != 1 || learned.bytes != 6 || learned.output != 5 {
		t.Fatalf("learned %+v from the reply, want 6 content bytes for 5 tokens", learned)
	}
}
