package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// failsNext is an Anthropic Messages upstream that answers its next fails
// requests with status (an overload's 503 or a rate limit's 429), then as
// cacheWriter does, writing the whole prompt to its cache.
type failsNext struct {
	cacheWriter
	mu     sync.Mutex
	fails  int
	status int
	asked  int
}

func (f *failsNext) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.asked++
	fail := f.fails > 0
	if fail {
		f.fails--
	}
	status := f.status
	f.mu.Unlock()
	if !fail {
		f.cacheWriter.ServeHTTP(w, r)
		return
	}
	io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusTooManyRequests {
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
}

func (f *failsNext) failNext(n, status int) {
	f.mu.Lock()
	f.fails, f.status, f.asked = n, status, 0
	f.mu.Unlock()
}

func (f *failsNext) askedTimes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked
}

// A conversation kept on a member for what its vendor cached of it, whose
// member fails once in a way that passes (an overloaded 503), is asked of
// that member again after a moment rather than moved at once to the next,
// which would be sent it all uncached (jiabubu on Discord: a channel that
// errs once and is back a moment later, and every rule moved the
// conversation off it at the first error). Asked again once only: failing
// again, it moves. A rate limit moves at once as before, and so does a
// conversation nobody has answered yet, which has no cache to keep.
func TestKeptConversationAskedAgainBeforeItMoves(t *testing.T) {
	const (
		first = `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`
		again = `{"model":"group/g","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
	)
	for _, c := range []struct {
		name     string
		routing  string
		affinity string
		fails    int
		status   int
		fresh    bool // the failing request opens a conversation of its own
		want     string
		aAsked   int
	}{
		{"overloaded once, Auto", "", "", 1, 503, false, "a", 2},
		{"overloaded once, Use before expiry", provider.Pace, "", 1, 503, false, "a", 2},
		{"overloaded once, Session", "", provider.AffinitySession, 1, 503, false, "a", 2},
		{"overloaded twice", "", "", 2, 503, false, "b", 2},
		{"rate limited", "", "", 1, 429, false, "b", 1},
		{"a new conversation", "", "", 1, 503, true, "b", 1},
		{"Stays Off", "", provider.AffinityOff, 1, 503, false, "b", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			pause := retryPause
			retryPause = time.Millisecond
			t.Cleanup(func() { retryPause = pause })
			a := &failsNext{cacheWriter: cacheWriter{wrote: 3000}}
			b := &failsNext{cacheWriter: cacheWriter{wrote: 3000}}
			au, bu := httptest.NewServer(a), httptest.NewServer(b)
			t.Cleanup(au.Close)
			t.Cleanup(bu.Close)
			for _, p := range []provider.Provider{
				{ID: "pa", Name: "A", Key: "a-key", Models: []string{"m"}, Anthropic: au.URL + "/v1"},
				{ID: "pb", Name: "B", Key: "b-key", Models: []string{"m"}, Anthropic: bu.URL + "/v1"},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"pa/m", "pb/m"}, Routing: c.routing, Affinity: c.affinity}); err != nil {
				t.Fatal(err)
			}
			s := New()
			if code, body := postAs(t, s, "s1", first); code != 200 || !strings.Contains(body, "from a-key") {
				t.Fatalf("first turn %d %s, want a's answer", code, body)
			}
			a.failNext(c.fails, c.status)
			session, body := "s1", again
			if c.fresh {
				session, body = "s2", first
			}
			code, got := postAs(t, s, session, body)
			if code != 200 || !strings.Contains(got, "from "+c.want+"-key") {
				t.Fatalf("next request %d %s, want %s's answer", code, got, c.want)
			}
			if n := a.askedTimes(); n != c.aAsked {
				t.Fatalf("a asked %d times, want %d", n, c.aAsked)
			}
			if c.want == "a" && b.askedTimes() != 0 {
				t.Fatalf("b asked %d times, want none", b.askedTimes())
			}
		})
	}
}
