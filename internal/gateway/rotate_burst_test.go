package gateway

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// held answers once `want` requests are in at once (or after a while), so
// they are all out together, and counts them by key.
type held struct {
	mu    sync.Mutex
	want  int
	in    int
	all   chan struct{}
	tried map[string]int
}

func (h *held) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	h.mu.Lock()
	h.tried[key]++
	h.in++
	if h.in == h.want {
		close(h.all)
	}
	h.mu.Unlock()
	select {
	case <-h.all:
	case <-time.After(5 * time.Second):
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
}

// #1342: requests that each start a conversation of their own (one image
// each, no session), sent at once to a provider routed in turn, spread over
// its keys or accounts. leastHeld counted only conversations already
// answered, so while none of the burst had been answered every one of them
// went to whichever held the fewest before it — here, every key but the one
// an earlier conversation is on got them all, and that one none.
func TestRotateSpreadsABurstOfNewConversations(t *testing.T) {
	fresh(t)
	h := &held{want: 1, all: make(chan struct{}), tried: map[string]int{}}
	serveOn(t, "ag", "k1", []string{"m1"}, h, "k2", "k3")
	if err := provider.SetRouting("ag", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	routed.Lock()
	delete(routed.turn, "ag")
	routed.Unlock()
	s := New()
	// a conversation from before, answered and held on one key
	if code, out := postAs(t, s, "", `{"model":"ag/m1","messages":[{"role":"user","content":"earlier"}]}`); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	var first string
	for k := range h.tried {
		first = k
	}

	const n = 6
	h.mu.Lock()
	h.want, h.in, h.all, h.tried = n, 0, make(chan struct{}), map[string]int{}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(`{"model":"ag/m1","messages":[{"role":"user","content":"picture %d"}]}`, i)
			if code, out := postAs(t, s, "", body); code != 200 {
				t.Errorf("%d %s", code, out)
			}
		}()
	}
	wg.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range []string{"k1", "k2", "k3"} {
		if h.tried[k] == 0 {
			t.Errorf("%s got none of %d new conversations sent at once (an earlier one is on %s): %v", k, n, first, h.tried)
		}
	}
	// and each ended its part in the count
	sticks.Lock()
	left := len(sticks.opening)
	sticks.Unlock()
	if left != 0 {
		t.Errorf("%d conversations still counted as opening after they were answered", left)
	}
}
