package gateway

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A rate limit that comes back as soon as its rest is over rests twice as
// long each time, up to half an hour, until the account answers; a vendor's
// own word of when (a header, or a reset time in its error) is kept; and
// an account whose usage says it has credits left still waits its rest out
// behind the others (01huadalang: a WorkBuddy free model 429 while the
// account had credits was asked again every minute).
func TestRateLimitBacksOffWhenItComesBack(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"a@x.com": {{Used: 5, Span: 30 * 24 * time.Hour}}}
	}
	s := &Server{}
	c := candidate{p: provider.Provider{ID: "workbuddy", Account: &provider.Account{Agent: "workbuddy", User: "a@x.com"}}, model: "free-model", rest: "workbuddy#a"}
	id := c.restKey()
	body := []byte(`{"error":{"message":"Too many requests","type":"rate_limit_error"}}`)
	near := func(d, want time.Duration) bool { return d > want-5*time.Second && d <= want }
	// its rest runs out: the next 429 comes as soon as it is back
	expire := func() {
		restingUntil.Lock()
		restingUntil.m[id] = time.Now().Add(-time.Second)
		restingUntil.Unlock()
	}

	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		r := s.restAfter(c, 429, http.Header{}, body)
		if r.Why != failRate || !near(time.Until(r.Until), want) || r.Failures != i+1 {
			t.Fatalf("429 #%d: rests %v by %s, %d failures; want %v", i+1, time.Until(r.Until), r.By, r.Failures, want)
		}
		if i > 0 && r.By != "backoff" {
			t.Fatalf("429 #%d by %s", i+1, r.By)
		}
		// a usage that says it has credits doesn't put it before the others
		other := candidate{p: provider.Provider{ID: "next"}, model: "m", rest: "next"}
		out, _ := restLast([]candidate{c, other}, planned{order: make([]Weighed, 2)})
		if out[0].rest != "next" {
			t.Fatalf("429 #%d: resting account tried first", i+1)
		}
		expire()
	}

	// tried anyway while resting (the last one left, again and again in one
	// request): it doesn't stretch the rest further
	servedCandidate(c, 1)
	s.restAfter(c, 429, http.Header{}, body)
	expire()
	s.restAfter(c, 429, http.Header{}, body) // 2m
	r := s.restAfter(c, 429, http.Header{}, body)
	if !near(time.Until(r.Until), 2*time.Minute) || r.Failures != 2 {
		t.Fatalf("failed again while resting: %v, %d failures", time.Until(r.Until), r.Failures)
	}

	// an answer starts it over
	servedCandidate(c, 1)
	if r := s.restAfter(c, 429, http.Header{}, body); !near(time.Until(r.Until), time.Minute) || r.By != "cooldown" {
		t.Fatalf("after an answer: %v by %s", time.Until(r.Until), r.By)
	}

	// the vendor's Retry-After, when longer than the backoff, is what it waits
	servedCandidate(c, 1)
	h := http.Header{}
	h.Set("Retry-After", "600")
	if r := s.restAfter(c, 429, h, body); !near(time.Until(r.Until), 10*time.Minute) || r.By != "retry-after" {
		t.Fatalf("Retry-After 600: %v by %s", time.Until(r.Until), r.By)
	}
	// and a reset time in the error, as keepRetry notes it
	servedCandidate(c, 1)
	h = http.Header{}
	h.Set(resetsHeader, strconv.FormatInt(time.Now().Add(20*time.Minute).Unix(), 10))
	if r := s.restAfter(c, 429, h, body); !near(time.Until(r.Until), 20*time.Minute) || r.By != "retry-after" {
		t.Fatalf("reset noted: %v by %s", time.Until(r.Until), r.By)
	}
	// a rate limit long after the last one's rest ended is a new one
	restingUntil.Lock()
	restingUntil.m[id] = time.Now().Add(-rateForget - time.Minute)
	restingUntil.Unlock()
	if r := s.restAfter(c, 429, http.Header{}, body); !near(time.Until(r.Until), time.Minute) || r.Failures != 1 {
		t.Fatalf("long after: %v, %d failures", time.Until(r.Until), r.Failures)
	}
	servedCandidate(c, 1)
}
