package gateway

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// rateUp counts the requests an upstream took, and rateUp each with
// status and body, or a reply when status is 0.
type rateUp struct {
	sync.Mutex
	n      int
	status int
	body   string
}

func (a *rateUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	a.Lock()
	a.n++
	status, body := a.status, a.body
	a.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		io.WriteString(w, body)
		return
	}
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"answered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
}

func (a *rateUp) asked() int {
	a.Lock()
	defer a.Unlock()
	return a.n
}

// restMembersAs rests each named member of group's candidates until the time
// given, as rest says, as a failure before would have.
func restMembersAs(t *testing.T, s *Server, group string, rest Rest, until map[string]time.Time) {
	t.Helper()
	g, ms, ok := provider.FindGroup("group/" + group)
	if !ok {
		t.Fatalf("no group %s", group)
	}
	cs, _ := s.planGroup(g.Live(), ms, provider.Chat)
	restingUntil.Lock()
	defer restingUntil.Unlock()
	for _, c := range cs {
		if u, ok := until[c.p.ID]; ok {
			r := rest
			r.Key, r.Until = c.restKey(), u
			restingUntil.m[c.restKey()] = u
			restingUntil.note[c.restKey()] = r
		}
	}
}

var momentsRate = Rest{Why: failRate, Status: http.StatusTooManyRequests, By: "backoff"}

const tooMany = `{"error":{"message":"Too many requests"}}`

// rateLeftGroup saves a group "g" of a/x and b/x, served by a and b, both
// resting, a back first, and shortens the pauses. patience is the group's
// own (0 for the default).
func rateLeftGroup(t *testing.T, rest Rest, patience int, a, b *rateUp) *Server {
	t.Helper()
	fresh(t)
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	serveOn(t, "a", "ka", []string{"x"}, a)
	serveOn(t, "b", "kb", []string{"x"}, b)
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"a/x", "b/x"}, Routing: provider.Ordered, Patience: patience}); err != nil {
		t.Fatal(err)
	}
	s := New()
	now := time.Now()
	restMembersAs(t, s, "g", rest, map[string]time.Time{"a": now.Add(time.Minute), "b": now.Add(2 * time.Minute)})
	return s
}

// justforlxz on X: a group's member answering 429 again and again ended
// the request with its 429, the same model on the group's other provider
// never asked. Both members rested from a 429 each a while before, so the
// request asked only the one back soonest (#1799), and that one again on
// its retries and the group's patience. Now a member resting only for a
// moment's rate limit is asked once the one asked has failed for a moment,
// before that one is asked again.
func TestGroupRateLimitedAsksTheMemberRestingForAMoment(t *testing.T) {
	a, b := &rateUp{status: http.StatusTooManyRequests, body: tooMany}, &rateUp{}
	s := rateLeftGroup(t, momentsRate, 0, a, b)
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "answered") {
		t.Fatalf("a rate limited, b resting from a rate limit of its own: %d %s (a %d, b %d)", code, body, a.asked(), b.asked())
	}
	if a.asked() != 1 || b.asked() != 1 {
		t.Fatalf("asked a %d, b %d; want each once, a first", a.asked(), b.asked())
	}

	// streamed the same
	a.Lock()
	a.n = 0
	a.Unlock()
	b.Lock()
	b.n = 0
	b.Unlock()
	restMembersAs(t, s, "g", momentsRate, map[string]time.Time{"a": time.Now().Add(time.Minute), "b": time.Now().Add(2 * time.Minute)})
	code, body = postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "answered") || b.asked() != 1 {
		t.Fatalf("streamed: %d %s (a %d, b %d)", code, body, a.asked(), b.asked())
	}
}

// Both rate limited, each is asked, and the agent has the 429 once the
// group's patience is spent, as before: the left-out member is asked in
// the rounds too, not only the one back first.
func TestGroupRateLimitedBothStillEndsInTheLimit(t *testing.T) {
	a, b := &rateUp{status: http.StatusTooManyRequests, body: tooMany}, &rateUp{status: http.StatusTooManyRequests, body: tooMany}
	s := rateLeftGroup(t, momentsRate, 0, a, b)
	code, _ := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusTooManyRequests {
		t.Fatalf("both rate limited: %d", code)
	}
	if a.asked() == 0 || b.asked() == 0 {
		t.Fatalf("asked a %d, b %d", a.asked(), b.asked())
	}
}

// What the request itself is refused for isn't asked of the member left
// out: a 400 reaches the agent from the one asked, as before.
func TestGroupRequestRefusedDoesNotAskTheMemberLeftOut(t *testing.T) {
	a, b := &rateUp{status: http.StatusBadRequest, body: `{"error":{"message":"invalid request: messages must not be empty","type":"invalid_request_error"}}`}, &rateUp{}
	s := rateLeftGroup(t, momentsRate, 0, a, b)
	code, _ := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusBadRequest || b.asked() != 0 {
		t.Fatalf("a refused the request: %d, b asked %d", code, b.asked())
	}
}

// A member left out resting for its quota, its credit, or till a time its
// vendor named is still left out (#1799): only a moment's rate limit, a
// guess, is asked past.
func TestGroupMemberRestingLongerStaysOut(t *testing.T) {
	for name, rest := range map[string]Rest{
		"quota":       {Why: failQuota, Status: http.StatusTooManyRequests, By: "quota"},
		"credit":      {Why: failCredit, Status: http.StatusPaymentRequired, By: "credit"},
		"outage":      {Why: failOther, Status: http.StatusBadGateway, By: "backoff"},
		"retry-after": {Why: failRate, Status: http.StatusTooManyRequests, By: "retry-after"},
	} {
		t.Run(name, func(t *testing.T) {
			a, b := &rateUp{status: http.StatusTooManyRequests, body: tooMany}, &rateUp{}
			// not waited out: b isn't asked in the wait either
			s := rateLeftGroup(t, rest, provider.PatienceOff, a, b)
			code, _ := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
			if code != http.StatusTooManyRequests || b.asked() != 0 {
				t.Fatalf("b resting for %s: %d, b asked %d", name, code, b.asked())
			}
		})
	}
}

// A provider asked by name, with one key, has nobody left out: its 429
// reaches the agent once its own retries are spent, as before.
func TestSingleProviderRateLimitStillReachesTheAgent(t *testing.T) {
	fresh(t)
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	a := &rateUp{status: http.StatusTooManyRequests, body: tooMany}
	serveOn(t, "a", "ka", []string{"x"}, a)
	b := &rateUp{}
	serveOn(t, "b", "kb", []string{"x"}, b)
	code, body := postAs(t, New(), "", `{"model":"a/x","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "Too many requests") || b.asked() != 0 {
		t.Fatalf("a/x rate limited: %d %s, b asked %d", code, body, b.asked())
	}
}
