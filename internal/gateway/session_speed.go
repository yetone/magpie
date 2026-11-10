package gateway

import (
	"math"
	"sort"
	"sync"
	"time"
)

// curveMeter keeps each session's output speed, a second at a time, for
// the Sessions page's curve: how many tokens a second the model wrote to
// the agent over the last curveSpan seconds, and the seconds nothing of
// the session's was in flight, when the agent ran a tool or waited on
// its user. It counts what liveRow counts, a reply's content as it
// reaches the agent, and reads it as tokens at the rate liveRates learned
// for the model that wrote it. A session whose model has no rate yet has
// the curve's shape and no numbers.
//
// It is kept in memory only. A session is dropped curveForget after its
// last request ended, and a pause longer than curveRound with nothing in
// flight starts a new round, whose average, peak and output are counted
// again from 0.
//
// add runs on every write of a stream, a few array writes under a lock
// nothing blocks under.
const (
	curveSpan   = 120 // seconds of curve kept
	curveForget = 10 * time.Minute
	curveRound  = 2 * time.Minute
	curveMost   = 256 // sessions kept, the least recently heard dropped
	// curveSmooth: seconds a peak and the current speed are averaged over,
	// as bytes come in network-sized bursts
	curveSmooth = 3
)

type curveKey struct{ agent, session string }

type curveTrack struct {
	// bytes and live: the content bytes of each second, and whether
	// anything of the session's was in flight in it, at sec % curveSpan
	bytes [curveSpan]int64
	live  [curveSpan]bool
	// at: the latest second the ring holds; heard: when the session last
	// had something in flight (unix s), for curveForget and curveRound
	at, heard int64
	open      int
	// provider and model: the last pair that wrote, whose rate reads the
	// bytes as tokens
	provider, model string
	// the round: its content bytes, the seconds it wrote in, its best
	// smoothed second, and when it began (unix s)
	roundBytes, roundSecs, roundPeak, roundAt int64
}

type curveMeter struct {
	mu sync.Mutex
	m  map[curveKey]*curveTrack
	// now is the clock, time.Now outside tests
	now func() time.Time
}

func (m *curveMeter) clock() int64 {
	if m.now != nil {
		return m.now().Unix()
	}
	return time.Now().Unix()
}

// track is k's, made when it isn't there yet; under mu.
func (m *curveMeter) track(k curveKey, now int64) *curveTrack {
	if m.m == nil {
		m.m = map[curveKey]*curveTrack{}
	}
	t := m.m[k]
	if t == nil {
		if len(m.m) >= curveMost {
			m.dropOldest()
		}
		t = &curveTrack{at: now, heard: now, roundAt: now}
		m.m[k] = t
	}
	t.advance(now)
	return t
}

func (m *curveMeter) dropOldest() {
	var oldest curveKey
	at := int64(math.MaxInt64)
	for k, t := range m.m {
		if t.open == 0 && t.heard < at {
			oldest, at = k, t.heard
		}
	}
	if at != math.MaxInt64 {
		delete(m.m, oldest)
	}
}

// advance moves the ring on to now: each second after the last it holds
// is empty, live if something was in flight all along. A pause longer
// than curveRound with nothing in flight begins a new round.
func (t *curveTrack) advance(now int64) {
	if now <= t.at {
		return
	}
	if t.open == 0 && now-t.heard > int64(curveRound/time.Second) {
		t.roundBytes, t.roundSecs, t.roundPeak, t.roundAt = 0, 0, 0, now
	}
	// the last second held is whole now, and so are the few after it
	// whose smoothed speed still reaches back to it: each may be a peak
	t.peak(t.at)
	last := t.at
	for s := max(t.at+1, now-curveSpan+1); s <= now; s++ {
		i := s % curveSpan
		t.bytes[i], t.live[i] = 0, t.open > 0
	}
	t.at = now
	for s := last + 1; s < now && s < last+curveSmooth; s++ {
		t.peak(s)
	}
}

// peak counts the whole second s toward the round's peak.
func (t *curveTrack) peak(s int64) {
	if s < t.roundAt {
		return
	}
	if v := t.smoothed(s); v > t.roundPeak {
		t.roundPeak = v
	}
}

// smoothed is the bytes a second over the curveSmooth seconds up to s,
// the seconds nothing was in flight left out: 0 when none was.
func (t *curveTrack) smoothed(s int64) int64 {
	var sum, n int64
	for d := int64(0); d < curveSmooth; d++ {
		at := s - d
		if at <= t.at-curveSpan || at < t.roundAt {
			break
		}
		if i := at % curveSpan; t.live[i] {
			sum += t.bytes[i]
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// open counts a request of the session's in flight until the func it
// answers is called. A request with no session is not a session's.
func (m *curveMeter) open(agent, session string) func() {
	if m == nil || session == "" {
		return func() {}
	}
	k := curveKey{agent, session}
	m.mu.Lock()
	now := m.clock()
	t := m.track(k, now)
	t.open++
	t.heard = now
	t.live[now%curveSpan] = true
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		now := m.clock()
		t := m.m[k]
		if t == nil {
			return
		}
		t.advance(now)
		t.open = max(t.open-1, 0)
		t.heard = now
	}
}

// add counts n content bytes written now by provider's model.
func (m *curveMeter) add(agent, session, provider, model string, n int64) {
	if m == nil || session == "" || n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	t := m.track(curveKey{agent, session}, now)
	i := now % curveSpan
	if t.bytes[i] == 0 {
		t.roundSecs++
	}
	t.bytes[i] += n
	t.live[i] = true
	t.roundBytes += n
	if provider != "" && model != "" {
		t.provider, t.model = provider, model
	}
}

// Speed is a session's output speed for the Sessions page. Tokens a
// second are estimates (liveRates); Rated is false while the session's
// model has no rate yet, and then every number is 0 and Curve holds
// content bytes a second instead, for its shape alone.
type Speed struct {
	Agent   string `json:"agent"`
	Session string `json:"session"`
	// Live: a request of the session's is in flight now
	Live  bool `json:"live"`
	Rated bool `json:"rated"`
	// Now: tokens a second over the last few whole seconds, 0 when
	// nothing is in flight
	Now float64 `json:"now"`
	// Avg, Peak and Output are the round's: tokens a second over the
	// seconds it wrote in, its best few seconds, and its output tokens
	Avg    float64 `json:"avg"`
	Peak   float64 `json:"peak"`
	Output int64   `json:"output"`
	// Curve: the last curveSpan whole seconds, oldest first, nil for a
	// second with nothing of the session's in flight
	Curve []*float64 `json:"curve"`
	// Idle: seconds since its last request ended, when nothing is in
	// flight
	Idle int64 `json:"idle,omitempty"`
}

// Speeds are the sessions heard in the last curveForget, the most
// recently heard first.
func (s *Server) Speeds() []Speed { return s.curves.speeds() }

func (m *curveMeter) speeds() []Speed {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	now := m.clock()
	type got struct {
		k     curveKey
		t     curveTrack
		heard int64
	}
	var gs []got
	for k, t := range m.m {
		t.advance(now)
		if t.open == 0 && now-t.heard > int64(curveForget/time.Second) {
			delete(m.m, k)
			continue
		}
		gs = append(gs, got{k, *t, t.heard})
	}
	m.mu.Unlock()
	sort.Slice(gs, func(i, j int) bool {
		if gs[i].heard != gs[j].heard {
			return gs[i].heard > gs[j].heard
		}
		return gs[i].k.agent+gs[i].k.session < gs[j].k.agent+gs[j].k.session
	})
	out := make([]Speed, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.t.speed(g.k, now))
	}
	return out
}

// speed reads t at now, the second now itself not whole yet.
func (t *curveTrack) speed(k curveKey, now int64) Speed {
	sp := Speed{Agent: k.agent, Session: k.session, Live: t.open > 0}
	rate, rated := liveRates.rate(t.provider, t.model)
	sp.Rated = rated
	if !rated {
		rate = 1
	}
	tok := func(b float64) float64 { return math.Round(b*rate*10) / 10 }
	sp.Curve = make([]*float64, curveSpan)
	for d := int64(0); d < curveSpan; d++ {
		s := now - curveSpan + d
		if s <= t.at-curveSpan || s < 0 {
			continue
		}
		if i := s % curveSpan; t.live[i] {
			v := tok(float64(t.bytes[i]))
			sp.Curve[d] = &v
		}
	}
	if !rated {
		return sp
	}
	if sp.Live {
		sp.Now = tok(float64(t.smoothed(now - 1)))
	} else {
		sp.Idle = now - t.heard
	}
	if t.roundSecs > 0 {
		sp.Avg = tok(float64(t.roundBytes) / float64(t.roundSecs))
	}
	sp.Peak = tok(float64(max(t.roundPeak, t.smoothed(now-1))))
	sp.Output = int64(math.Round(float64(t.roundBytes) * rate))
	return sp
}
