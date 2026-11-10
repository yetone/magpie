package gateway

import (
	"sync"
	"time"
)

// tokenRates learns how many output tokens a provider's model writes per
// content byte, from streamed replies whose vendor said their output
// tokens, to estimate a reply in flight before its usage comes (#4951).
// An estimate is never learned from, and under liveRateMin replies there
// is none: the page then shows no tokens rather than a guess.
//
// The rates are kept in memory only: a restart learns them again in
// liveRateMin replies, and nothing is written per request.
const (
	liveRateMin  = 3   // replies learned before a pair estimates
	liveRateKeys = 256 // pairs kept, the least recently learned dropped
)

// rateSample is a pair's sums: estimating a ratio needs no more.
type rateSample struct {
	bytes, output int64
	seen          int
	at            int64
}

type tokenRates struct {
	mu sync.Mutex
	m  map[string]rateSample
}

// liveRates is the gateway's one: estimate runs on every write of a
// stream, a multiplication under a lock nothing blocks under.
var liveRates = &tokenRates{m: map[string]rateSample{}}

func rateKey(provider, model string) string {
	if provider == "" || model == "" {
		return ""
	}
	return provider + "/" + model
}

// estimate is content's output tokens at the pair's learned rate, rounded
// down, or 0 when it has none yet.
func (t *tokenRates) estimate(provider, model string, content int64) int {
	if content <= 0 || provider == "" || model == "" {
		return 0
	}
	t.mu.Lock()
	s, ok := t.m[rateKey(provider, model)]
	t.mu.Unlock()
	if !ok || s.seen < liveRateMin || s.bytes <= 0 || s.output <= 0 {
		return 0
	}
	// in floats: the sums grow with every reply, and their product with a
	// long reply's content would overflow
	return int(float64(content) * float64(s.output) / float64(s.bytes))
}

// learn adds one finished reply: content bytes and the vendor's output.
func (t *tokenRates) learn(provider, model string, content int64, output int) {
	key := rateKey(provider, model)
	if key == "" || content <= 0 || output <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.m[key]
	s.bytes += content
	s.output += int64(output)
	s.seen++
	s.at = time.Now().UnixNano()
	t.m[key] = s
	if len(t.m) > liveRateKeys {
		oldest, at := "", int64(1<<62)
		for k, v := range t.m {
			if v.at < at {
				oldest, at = k, v.at
			}
		}
		delete(t.m, oldest)
	}
}

// resetRates forgets every rate, for a test.
func resetRates() {
	liveRates.mu.Lock()
	liveRates.m = map[string]rateSample{}
	liveRates.mu.Unlock()
}

// rate is the pair's learned output tokens a content byte, false when it
// has none yet (as estimate).
func (t *tokenRates) rate(provider, model string) (float64, bool) {
	if provider == "" || model == "" {
		return 0, false
	}
	t.mu.Lock()
	s, ok := t.m[rateKey(provider, model)]
	t.mu.Unlock()
	if !ok || s.seen < liveRateMin || s.bytes <= 0 || s.output <= 0 {
		return 0, false
	}
	return float64(s.output) / float64(s.bytes), true
}
