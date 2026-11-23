package gateway

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	return rateLeftGroupOf(t, rest, patience, a, b)
}

// rateLeftGroupOf is rateLeftGroup with any handler for a.
func rateLeftGroupOf(t *testing.T, rest Rest, patience int, a http.Handler, b *rateUp) *Server {
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
// out: a 400 and a conversation too long reach the agent from the one
// asked, as before.
func TestGroupRequestRefusedDoesNotAskTheMemberLeftOut(t *testing.T) {
	for name, body := range map[string]string{
		"invalid":  `{"error":{"message":"invalid request: messages must not be empty","type":"invalid_request_error"}}`,
		"too long": `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`,
	} {
		for rn, rest := range map[string]Rest{"rate": momentsRate, "outage": {Why: failOther, Status: http.StatusBadGateway, By: "backoff"}} {
			t.Run(name+"/"+rn, func(t *testing.T) {
				a, b := &rateUp{status: http.StatusBadRequest, body: body}, &rateUp{}
				s := rateLeftGroup(t, rest, 0, a, b)
				code, _ := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
				if code != http.StatusBadRequest || b.asked() != 0 {
					t.Fatalf("a refused the request (%s): %d, b asked %d", name, code, b.asked())
				}
			})
		}
	}
}

// A member left out resting for its quota, its credit, a refused key, or
// till a time its vendor named is still left out (#1799): only a rest
// magpie guessed the length of is asked past.
func TestGroupMemberRestingLongerStaysOut(t *testing.T) {
	for name, rest := range map[string]Rest{
		"quota":       {Why: failQuota, Status: http.StatusTooManyRequests, By: "quota"},
		"credit":      {Why: failCredit, Status: http.StatusPaymentRequired, By: "credit"},
		"key":         {Why: failAuth, Status: http.StatusUnauthorized, By: "backoff"},
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

// hangUp counts the requests it took and closes each connection without
// a word, as a vendor gone away does.
type hangUp struct{ n atomic.Int32 }

func (h *hangUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.n.Add(1)
	if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
		c.Close()
	}
}

// justforlxz on X after the 429 was handled: "其他的错误码也要处理". The
// one asked failing for anything about itself (a 5xx, overloaded, no
// connection, its key, its allowance, its model) asks a member left out as
// resting for a while magpie guessed (a moment's rate limit, an outage, a
// slow start), as a member after it in the order would be. Before, each
// of these reached the agent, the member left out never asked.
func TestGroupMemberFailingForItselfAsksTheMemberLeftOut(t *testing.T) {
	// brief: a failure for a moment, handed on to a member resting after
	// an outage too; the others only to one resting for a rate limit, so
	// a group down whole isn't asked each member for one's credit (#1799)
	fails := map[string]struct {
		status int
		body   string
		brief  bool
	}{
		"500":       {500, `{"error":{"message":"internal error"}}`, true},
		"502":       {502, `{"error":{"message":"bad gateway"}}`, true},
		"503":       {503, `{"error":{"message":"service unavailable"}}`, true},
		"504":       {504, `{"error":{"message":"gateway timeout"}}`, true},
		"529":       {529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, true},
		"401":       {401, `{"error":{"message":"invalid api key"}}`, false},
		"403":       {403, `{"error":{"message":"forbidden"}}`, false},
		"404":       {404, `{"error":{"message":"model not found"}}`, false},
		"429 quota": {429, `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota"}}`, false},
		"402":       {402, `{"error":{"message":"insufficient balance"}}`, false},
		"429":       {429, tooMany, true},
		"hung up":   {0, "", true},
	}
	rests := map[string]Rest{
		"rate":   momentsRate,
		"outage": {Why: failOther, Status: http.StatusBadGateway, By: "backoff"},
		"slow":   {Why: failSlow, Status: http.StatusGatewayTimeout, By: "slow"},
	}
	for rn, rest := range rests {
		for fn, f := range fails {
			t.Run(rn+"/"+fn, func(t *testing.T) {
				b := &rateUp{}
				var a http.Handler = &rateUp{status: f.status, body: f.body}
				if f.status == 0 {
					a = &hangUp{}
				}
				s := rateLeftGroupOf(t, rest, provider.PatienceOff, a, b)
				code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
				if !f.brief && rn != "rate" {
					if code < 400 || b.asked() != 0 {
						t.Fatalf("a failed %s, b resting after %s: %d %s, b asked %d", fn, rn, code, body, b.asked())
					}
					return
				}
				if code != 200 || !strings.Contains(body, "answered") || b.asked() != 1 {
					t.Fatalf("a failed %s, b resting for %s: %d %s, b asked %d", fn, rn, code, body, b.asked())
				}
			})
		}
	}
}

// A member handed a request while it rested isn't handed another for a
// while (handOnEvery): a group whose members all fail for a moment is
// asked the one back soonest a request, and the other once, not every
// member every request (#1799).
func TestGroupHandsOnOncePerWhile(t *testing.T) {
	a := &rateUp{status: 503, body: `{"error":{"message":"service unavailable"}}`}
	b := &rateUp{status: 503, body: `{"error":{"message":"service unavailable"}}`}
	s := rateLeftGroup(t, Rest{Why: failOther, Status: 503, By: "backoff"}, provider.PatienceOff, a, b)
	var per [3][2]int
	for i := range per {
		was := [2]int{a.asked(), b.asked()}
		if code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code < 500 {
			t.Fatalf("request %d: every member fails, answered %d %s", i, code, body)
		}
		per[i] = [2]int{a.asked() - was[0], b.asked() - was[1]}
	}
	t.Logf("asked per request (a, b): %v", per)
	if per[0][0] == 0 || per[0][1] == 0 {
		t.Fatalf("the first request didn't hand on: %v", per)
	}
	for _, p := range per[1:] {
		if p[0] > 0 && p[1] > 0 {
			t.Fatalf("a later request asked both members again: %v", per)
		}
	}
}

// lateFirst answers a streamed request only after a while, or when it is
// let go.
type lateFirst struct{ n atomic.Int32 }

func (h *lateFirst) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.n.Add(1)
	select {
	case <-time.After(5 * time.Second):
	case <-r.Context().Done():
	}
}

// The one asked with no first token in the group's FirstToken, a member
// left out as resting for a while is asked next, as after a failure.
func TestGroupSlowMemberAsksTheMemberLeftOut(t *testing.T) {
	a, b := &lateFirst{}, &rateUp{}
	s := rateLeftGroupOf(t, momentsRate, provider.PatienceOff, a, b)
	g, _, _ := provider.FindGroup("group/g")
	g.FirstToken = 1
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "answered") || b.asked() != 1 || a.n.Load() != 1 {
		t.Fatalf("a slow, b resting for a moment: %d %s (a %d, b %d)", code, body, a.n.Load(), b.asked())
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
