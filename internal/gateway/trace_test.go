package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// The trace tells what routing did as it did it: who was to answer in what
// order, each try and what it answered, and how long one that failed rests.
func TestTraceTellsTheRoute(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &byKey{limited: map[string]bool{"k-personal": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := provider.Provider{ID: "plan", Name: "Plan", Chat: srv.URL + "/v1", Models: []string{"m1"},
		Key: "k-personal", KeyName: "Personal", Keys: []provider.KeyAccount{{Name: "Team", Key: "k-team"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		req.Header.Set("session_id", "native-session")
		req.Header.Set(SessionHeader, "chosen-session")
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	// one waiting for a route hears of it
	got := make(chan []Route, 1)
	go func() {
		got <- s.Trace(context.Background(), 0, 5*time.Second).Routes
	}()
	time.Sleep(50 * time.Millisecond)
	send()
	if rs := <-got; len(rs) != 1 {
		t.Fatalf("waiting: %d routes", len(rs))
	}

	st := s.Trace(context.Background(), 0, 0)
	r := st.Routes[len(st.Routes)-1]
	if r.Session != "chosen-session" {
		t.Fatalf("session %q", r.Session)
	}
	if !r.Done || r.Status != 200 || r.Provider != "plan" || len(r.Order) != 2 || r.Order[0].Who != "Personal" || r.Order[1].Kind != "key" {
		t.Fatalf("route %+v", r)
	}
	if len(r.Tries) != 2 || r.Tries[0].Status != 429 || r.Tries[0].Fail != failRate || r.Tries[0].Rest == nil ||
		r.Tries[0].Rest.By != "cooldown" || r.Tries[1].Status != 200 || r.Tries[1].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}

	if recs := usage.Load(time.Time{}); len(recs) != 1 || recs[0].RouteID != r.ID || r.ID == 0 {
		t.Fatalf("usage: %+v, route %d", recs, r.ID)
	}

	// the next finds the limited key resting, and says why
	send()
	st = s.Trace(context.Background(), st.Seq, 0)
	r = st.Routes[len(st.Routes)-1]
	if r.Order[0].Who != "Team" || r.Order[1].Rest == nil || r.Order[1].Rest.Status != 429 || len(r.Tries) != 1 {
		t.Fatalf("resting %+v", r)
	}
	if st.Totals != (Totals{Requests: 2, Rerouted: 1}) {
		t.Fatalf("totals %+v", st.Totals)
	}
}

// A vendor's Retry-After reaches routing through a failed request: the
// account rests as long as it says, and the trace says so.
func TestRetryAfterRests(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer k-slow" {
			w.Header().Set("Retry-After", "300")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"Too many requests"}}`)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[]}`)
	}))
	defer srv.Close()
	p := provider.Provider{ID: "ra", Name: "RA", Chat: srv.URL + "/v1", Models: []string{"m1"},
		Key: "k-slow", KeyName: "Slow", Keys: []provider.KeyAccount{{Name: "Fast", Key: "k-fast"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"ra/m1","messages":[{"role":"user","content":"hi"}]}`)))
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) != 1 || len(rs[0].Tries) != 2 || rs[0].Tries[0].Rest == nil {
		t.Fatalf("%+v", rs)
	}
	r := rs[0].Tries[0].Rest
	if r.By != "retry-after" || time.Until(r.Until).Round(time.Minute) != 5*time.Minute {
		t.Fatalf("rest %+v", r)
	}
}

// A route weighed with no seats tells its order as [], not null: the
// Routing page reads it as a list, and a null one left a refused request
// that couldn't be opened (Discord, mythfish on v0.1.810).
func TestTraceOrderIsAList(t *testing.T) {
	s := New()
	r := s.trace.begin(Route{Model: "workbuddy-ai/deepseek-v4.1-flash"})
	s.trace.update(r, func(r *Route) {
		r.Tries = append(r.Tries, Try{ID: "workbuddy-ai", Status: 400, Error: "WorkBuddy AI: Invalid request parameters"})
		r.Status, r.Error, r.Done = 400, "WorkBuddy AI: Invalid request parameters", true
	})
	st := s.Trace(context.Background(), 0, 0)
	b, err := json.Marshal(st.Routes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"order":[]`) || strings.Contains(string(b), `"order":null`) {
		t.Fatalf("trace routes = %s, want order []", b)
	}
	if _, c := s.trace.sessionLatest(context.Background(), "", 0, 0); c == nil || c.Order == nil {
		t.Fatalf("session's latest route = %+v, want a non-nil order", c)
	}
}
