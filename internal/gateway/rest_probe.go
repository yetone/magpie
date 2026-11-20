package gateway

// A key resting after an outage that names no time — a 5xx, its vendor
// unreachable — rests a minute, longer each time it fails again, while
// the keys ready beside it take the requests, so nothing asks it whether
// it is back before its rest ends (heatonz on Discord: a key back up
// waited out its rest, or a click on Try now). The gateway asks it in the
// background instead: its vendor's model list, with the key and the proxy
// it goes through, a minute into its rest, then two minutes on, then
// five, then every ten, and lifts the rest as soon as the list answers.
// A list costs no allowance, and none is asked of a subscription's
// account, a plugin's, or a rest whose end the vendor or the user named
// (out of quota or credit, a Retry-After, a window full), which ends on
// time — but an Azure key that answered before and is refused 401, its
// subscription disabled until its credit renews (azure_suspended.go), is
// asked; nor of a rate limit, which a list answers while the chat endpoint
// still refuses, or of one model alone. A list answers for the vendor,
// not for every model behind it, so a rest the list lifted that fails
// again is not asked again until the key answers a request.

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restProbeSteps is how long after a rest begins its key is first asked
// again, and how long after each list that failed the next is asked; the
// last is kept from then on.
var restProbeSteps = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// restProbeWait is how long one list is waited for.
var restProbeWait = 20 * time.Second

// restProbeMiss is how soon after a probe lifted a key's rest a failure
// counts against the probe: the list answered, the requests didn't.
const restProbeMiss = 30 * time.Minute

// listForProbe asks p's vendor its model list; swapped in tests.
var listForProbe = func(ctx context.Context, p provider.Provider) error {
	_, err := p.List(ctx)
	return err
}

type restProbe struct {
	by    *Server           // the gateway whose loop asks it
	p     provider.Provider // the key, as the request had it
	until time.Time         // the rest it is for: another since ends it
	next  time.Time         // when it is asked next
	step  int               // which of restProbeSteps comes next
	busy  bool              // being asked now
}

var restProbes = struct {
	sync.Mutex
	m      map[string]restProbe // rest key → its probe
	lifted map[string]time.Time // rest key → when a probe lifted its rest
	missed map[string]bool      // rest key → failed again after a probe lifted it
	// asking: the gateways whose loop runs; another's rests (a test's
	// Server) are not asked
	asking map[*Server]bool
	wake   chan struct{}
}{m: map[string]restProbe{}, lifted: map[string]time.Time{}, missed: map[string]bool{}, asking: map[*Server]bool{}, wake: make(chan struct{}, 1)}

// probeable says r, the rest c just took, is one asked again in the
// background: a key's own (not one model's), after a failure that says
// nothing of when it is back and isn't about the key itself — a server
// error, a timeout or no connection — at a vendor whose list is asked over
// the network, and long enough for the first ask to come before it ends.
// A Retry-After the vendor sent with it is its word on when to ask again,
// which a list asked sooner would go around.
func probeable(c candidate, r Rest, header http.Header, now time.Time) bool {
	if c.p.Account != nil || r.Model != "" || r.Key != c.restKey() || !c.p.ListAsks() {
		return false
	}
	if r.Why == failSuspended {
		// an Azure subscription disabled, its key refused till Azure
		// enables it again, which nothing says the time of: its list is
		// refused the same till then (azure_suspended.go)
		return r.By == "suspended" && probeBefore(now.Add(restProbeSteps[0]), r.Until)
	}
	if retryAfter(header, now) > 0 || r.Why != failOther {
		return false
	}
	if r.By != "backoff" {
		return false
	}
	if r.Status < 500 && r.Status != http.StatusRequestTimeout && r.Status != 0 {
		return false
	}
	return probeBefore(now.Add(restProbeSteps[0]), r.Until)
}

// probeBefore says an ask at is worth making before a rest ending at
// until: half the first step at least before it ends on its own.
func probeBefore(at, until time.Time) bool {
	return at.Before(until.Add(-restProbeSteps[0] / 2))
}

// probeMissed says key isn't to be asked again in the background: a probe
// lifted its rest a moment ago and it failed again all the same, or did
// so before and hasn't answered a request since.
func probeMissed(key string, now time.Time) bool {
	restProbes.Lock()
	defer restProbes.Unlock()
	if at, ok := restProbes.lifted[key]; ok {
		delete(restProbes.lifted, key)
		if now.Sub(at) < restProbeMiss {
			restProbes.missed[key] = true
		}
	}
	return restProbes.missed[key]
}

// probeAnswered forgets what the probes knew of key once it answers a
// request: a later outage is asked again.
func probeAnswered(key string) {
	restProbes.Lock()
	delete(restProbes.lifted, key)
	delete(restProbes.missed, key)
	restProbes.Unlock()
}

// probeLater has s's loop (keepProbingRests) ask p again during r.
func (s *Server) probeLater(p provider.Provider, r Rest, now time.Time) {
	restProbes.Lock()
	if !restProbes.asking[s] {
		restProbes.Unlock()
		return
	}
	restProbes.m[r.Key] = restProbe{by: s, p: p, until: r.Until, next: now.Add(restProbeSteps[0])}
	restProbes.Unlock()
	select {
	case restProbes.wake <- struct{}{}:
	default:
	}
}

// keepProbingRests asks the keys resting after an outage again as their
// turns come (probeLater), until ctx ends, and returns once the asks under
// way have.
func (s *Server) keepProbingRests(ctx context.Context) {
	restProbes.Lock()
	restProbes.asking[s] = true
	restProbes.Unlock()
	var asks sync.WaitGroup
	defer func() {
		asks.Wait()
		restProbes.Lock()
		delete(restProbes.asking, s)
		for key, pr := range restProbes.m {
			if pr.by == s {
				delete(restProbes.m, key)
			}
		}
		restProbes.Unlock()
	}()
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	for {
		next := s.probeDue(ctx, &asks, time.Now())
		wait := time.Hour
		if !next.IsZero() {
			wait = max(time.Until(next), 0)
		}
		t.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-restProbes.wake:
		case <-t.C:
		}
	}
}

// probeDue starts asking each of s's keys whose turn has come, lets go of
// those whose rest ended or changed, and says when the next turn is.
func (s *Server) probeDue(ctx context.Context, asks *sync.WaitGroup, now time.Time) time.Time {
	var next time.Time
	restProbes.Lock()
	defer restProbes.Unlock()
	for key, pr := range restProbes.m {
		if pr.by != s {
			continue
		}
		if !restingAt(key, pr.until) {
			delete(restProbes.m, key)
			continue
		}
		if pr.busy {
			continue
		}
		if now.Before(pr.next) {
			if next.IsZero() || pr.next.Before(next) {
				next = pr.next
			}
			continue
		}
		pr.busy = true
		restProbes.m[key] = pr
		asks.Add(1)
		go func() {
			defer asks.Done()
			c, done := context.WithTimeout(ctx, restProbeWait)
			err := listForProbe(c, pr.p)
			done()
			if ctx.Err() != nil {
				return
			}
			s.probed(key, pr.until, err, time.Now())
		}()
	}
	return next
}

// restingAt says key still rests the rest that ends at until.
func restingAt(key string, until time.Time) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	u, ok := restingUntil.m[key]
	return ok && u.Equal(until) && time.Now().Before(u)
}

// probed takes in what asking key's vendor came back with: answered, its
// rest is lifted, as Try now would; else it is asked again at the next
// step, unless its rest ends first.
func (s *Server) probed(key string, until time.Time, err error, now time.Time) {
	restProbes.Lock()
	pr, ok := restProbes.m[key]
	if !ok || !pr.until.Equal(until) {
		restProbes.Unlock()
		return // another rest, or none, since it was asked
	}
	if err != nil {
		pr.busy = false
		pr.next = now.Add(restProbeSteps[min(pr.step+1, len(restProbeSteps)-1)])
		pr.step++
		if probeBefore(pr.next, until) {
			restProbes.m[key] = pr
		} else {
			delete(restProbes.m, key) // its rest ends before then
		}
		restProbes.Unlock()
		select {
		case restProbes.wake <- struct{}{}:
		default:
		}
		return
	}
	delete(restProbes.m, key)
	restingUntil.Lock()
	lifted := restingUntil.m[key].Equal(until)
	ladder := lifted && ladderRest(restingUntil.note[key])
	if lifted {
		delete(restingUntil.m, key)
		delete(restingUntil.note, key)
		liftedLocked(key, now)
		restProbes.lifted[key] = now
	}
	restingUntil.Unlock()
	restProbes.Unlock()
	saveDropped()
	if ladder {
		saveRests()
	}
	if lifted {
		log.Printf("gateway: %s answered again; its rest is lifted", key)
		unsink(key)
		s.trace.lifted()
	}
}
