package gateway

import (
	"math"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// Speed lately, for Speed first (routes.Fastest): of each account or key
// and model (restID), a decayed average of its streamed answers' time to
// first token and of how fast they wrote. Kept in memory, a few numbers
// each, written once as an answer ends (noteSpeed) and read with a map
// lookup per candidate as a pool routed by speed is weighed: the usage
// history is never read for it. A restart forgets them, and each is
// measured again as Fastest's probes give it a turn.

// speedHalfLife: a sample counts half after this, against those since.
// speedStale: this long after its last answer a seat counts as not
// measured lately, and Fastest gives it a turn now and then to be
// measured again, so one that was slow once isn't kept last for good.
const (
	speedHalfLife = 20 * time.Minute
	speedStale    = time.Hour
)

// speedMost bounds the records: past it, those not measured lately go.
const speedMost = 4096

type speedRec struct {
	ttft, ttftW float64   // the average ms to the first token, and its samples' weight
	tps, tpsW   float64   // the average output tokens a second, and its samples' weight
	at, tpsAt   time.Time // the last sample of each
}

var speeds = struct {
	sync.Mutex
	m map[string]speedRec // candidate restID → its speed lately
}{m: map[string]speedRec{}}

func decayed(w float64, since time.Duration) float64 {
	if since <= 0 {
		return w
	}
	return w * math.Exp2(-since.Seconds()/speedHalfLife.Seconds())
}

// fold is avg of weight w, decayed by since, with x added at weight 1.
func fold(avg, w float64, since time.Duration, x float64) (float64, float64) {
	w = decayed(w, since)
	return (avg*w + x) / (w + 1), w + 1
}

// noteSpeed records an answer of c's: ttft ms from its try's start to its
// first content, and, when its pace was told, tokens written in w ms
// after it (usage.DecodeOf). A reply that wasn't streamed has no ttft and
// tells nothing.
func noteSpeed(c candidate, ttft int64, tokens int, w int64, now time.Time) {
	if ttft <= 0 {
		return
	}
	id := c.restID()
	speeds.Lock()
	defer speeds.Unlock()
	r := speeds.m[id]
	r.ttft, r.ttftW = fold(r.ttft, r.ttftW, now.Sub(r.at), float64(ttft))
	r.at = now
	if tokens > 0 && w > 0 {
		r.tps, r.tpsW = fold(r.tps, r.tpsW, now.Sub(r.tpsAt), float64(tokens)*1000/float64(w))
		r.tpsAt = now
	}
	if _, had := speeds.m[id]; !had && len(speeds.m) >= speedMost {
		for k, o := range speeds.m {
			if now.Sub(o.at) > speedStale {
				delete(speeds.m, k)
			}
		}
	}
	speeds.m[id] = r
}

// noteTrySpeed is noteSpeed of a try that answered, its pace as
// usage.DecodeOf counts it for the Requests and Usage pages.
func noteTrySpeed(c candidate, t Try, out, reasoning int, now time.Time) {
	tokens, w := 0, int64(0)
	if !t.ReasoningHidden || t.FirstText > 0 || t.FirstReasoning > 0 {
		tokens, w = usage.DecodeOf(out, reasoning, t.Millis, t.TTFT, t.FirstText, t.Flow, t.FirstReasoning)
	}
	noteSpeed(c, t.TTFT, tokens, w, now)
}

// speedOf is c's speed lately: its average ms to the first token and
// tokens a second, 0 for what wasn't measured lately.
func speedOf(c candidate, now time.Time) (ttft, tps float64) {
	speeds.Lock()
	r, ok := speeds.m[c.restID()]
	speeds.Unlock()
	if !ok {
		return 0, 0
	}
	if now.Sub(r.at) > speedStale {
		return 0, 0
	}
	if !r.tpsAt.IsZero() && now.Sub(r.tpsAt) <= speedStale {
		tps = r.tps
	}
	return r.ttft, tps
}

// forgetSpeeds drops every speed: a test starts anew.
func forgetSpeeds() {
	speeds.Lock()
	speeds.m = map[string]speedRec{}
	speeds.Unlock()
}
