package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// slowStart streams the role chunk at once and its text only after wait,
// or never when the request goes first.
type slowStart struct {
	wait time.Duration
	n    int
}

func (s *slowStart) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.n++
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	io.WriteString(w, `data: {"id":"s","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n")
	w.(http.Flusher).Flush()
	select {
	case <-r.Context().Done():
		return
	case <-time.After(s.wait):
	}
	io.WriteString(w, `data: {"id":"s","choices":[{"index":0,"delta":{"content":"slow answer"},"finish_reason":"stop"}]}`+"\n\n"+"data: [DONE]\n\n")
}

const chatStreamOK = `data: {"id":"b","choices":[{"index":0,"delta":{"role":"assistant","content":"from b"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"

func slowOn(t *testing.T, id string, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// A member that hasn't begun its answer within the group's first-token
// time is let go before any of it reaches the agent, and the next one
// answers; the slow one doesn't rest. The last one is always waited for.
func TestGroupFirstTokenFailsOver(t *testing.T) {
	fresh(t)
	a := &slowStart{wait: 10 * time.Second}
	b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, FirstToken: 1}); err != nil {
		t.Fatal(err)
	}
	s := New()
	began := time.Now()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "slow answer") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Fail != failSlow || r.Tries[0].Rest != nil || r.Tries[1].Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}
	recs := usage.Load(time.Time{})
	if len(recs) == 0 {
		t.Fatal("no record")
	}
	last := recs[len(recs)-1]
	if last.Sent <= 0 || last.TTFT <= 0 || last.Sent > last.TTFT {
		t.Fatalf("sent %d, ttft %d", last.Sent, last.TTFT)
	}

	// the last one left is waited for
	fresh(t)
	a = &slowStart{wait: 2 * time.Second}
	slowOn(t, "a", a)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m"}, Routing: provider.Ordered, FirstToken: 1})
	s = New()
	if code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "slow answer") {
		t.Fatalf("last: %d %s", code, body)
	}

	// a member that answers in time is kept
	fresh(t)
	a = &slowStart{wait: 100 * time.Millisecond}
	b = &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, FirstToken: 1})
	s = New()
	if code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "slow answer") || b.n != 0 {
		t.Fatalf("in time: %d %s (b %d)", code, body, b.n)
	}
}
