package usage

import (
	"math"
	"slices"
	"strings"
	"unique"
)

// MinRankTokens is the fewest output tokens a request has to write to count
// in the model speed ranking: a short reply is nearly all wait, and its
// speed and first token say little about the model. The ranking's tooltip
// says the same number.
const MinRankTokens = 100

// ModelSpeed is one model's place in the speed ranking: the median of its
// requests' output tokens a second after the first, and of the wait for the
// first token, over the requests that count. Median, so one stalled or one
// burst reply doesn't move it. Both are read off a histogram of 2% bins, so
// a median is within 1% of the exact one.
type ModelSpeed struct {
	Model string `json:"model"`
	// Requests are the requests that counted: answered, streamed and timed,
	// of MinRankTokens or more
	Requests int `json:"requests"`
	// Speed is the median tokens a second (Row.Decode's), over SpeedN
	// requests whose speed was measured; 0 for none
	Speed  float64 `json:"tps,omitempty"`
	SpeedN int     `json:"tps_n,omitempty"`
	// TTFT is the median ms to the first token, over Requests
	TTFT int64 `json:"ttft_ms,omitempty"`
}

// speedHist counts values by the 2% bin they fall in, so a chunk of
// hundreds of thousands of requests is a few hundred numbers, and a page
// adds the chunks' up instead of sorting every request's.
type speedHist map[uint16]uint32

const (
	histStep = 1.02
	// histZero is the bin of 1: values from ~0.0003 to ~10^8 have one
	histZero = 400
)

var histLog = math.Log(histStep)

func histBin(v float64) uint16 {
	b := math.Floor(math.Log(v)/histLog) + histZero
	return uint16(min(max(b, 0), math.MaxUint16))
}

// histValue is the middle of bin b.
func histValue(b uint16) float64 {
	return math.Exp((float64(b) - histZero + 0.5) * histLog)
}

func (h speedHist) total() (n int) {
	for _, c := range h {
		n += int(c)
	}
	return n
}

// median is the middle of the values counted, the mean of the two middle
// ones for an even count; 0 for none.
func (h speedHist) median() float64 {
	n := h.total()
	if n == 0 {
		return 0
	}
	bins := make([]uint16, 0, len(h))
	for b := range h {
		bins = append(bins, b)
	}
	slices.Sort(bins)
	lo, hi := (n-1)/2, n/2
	var got, seen int
	var sum float64
	for _, b := range bins {
		next := seen + int(h[b])
		if lo >= seen && lo < next {
			sum += histValue(b)
			got++
		}
		if hi >= seen && hi < next {
			sum += histValue(b)
			got++
		}
		seen = next
	}
	return sum / float64(got)
}

// modelHist is what one model's counted requests measured.
type modelHist struct {
	Requests int       `json:"n"`
	Speed    speedHist `json:"s,omitempty"`
	TTFT     speedHist `json:"t,omitempty"`
}

// speedRank collects the histograms of every model; a chunk keeps its own
// (chunkPart.speeds) and a page merges those of the chunks it reads.
type speedRank map[string]*modelHist

func (s speedRank) of(model string) *modelHist {
	m := s[model]
	if m == nil {
		m = &modelHist{Speed: speedHist{}, TTFT: speedHist{}}
		// unique's copy, so a chunk's part holds none of the chunk's strings
		s[unique.Make(model).Value()] = m
	}
	return m
}

// add counts r when it is fair to rank by: not rejected, not a try handed on
// to another, not failed or canceled, its first token timed, and it wrote
// MinRankTokens or more.
func (s speedRank) add(r *Row) {
	if r.IsRejected() || r.HandedOn || r.Failed() || r.Canceled() || r.TTFT <= 0 || r.Output < MinRankTokens {
		return
	}
	m := s.of(r.Model)
	m.Requests++
	m.TTFT[histBin(float64(r.TTFT))]++
	if n, w := r.Decode(); w > 0 && n >= MinRankTokens {
		m.Speed[histBin(float64(n)*1000/float64(w))]++
	}
}

// merge adds o's counts to s's.
func (s speedRank) merge(o speedRank) {
	for model, x := range o {
		m := s.of(model)
		m.Requests += x.Requests
		for b, c := range x.Speed {
			m.Speed[b] += c
		}
		for b, c := range x.TTFT {
			m.TTFT[b] += c
		}
	}
}

// result is the ranking, the fastest first; models whose speed wasn't
// measured follow, the one with more requests first.
func (s speedRank) result() []ModelSpeed {
	out := make([]ModelSpeed, 0, len(s))
	for model, m := range s {
		out = append(out, ModelSpeed{
			Model: model, Requests: m.Requests, SpeedN: m.Speed.total(),
			TTFT: int64(m.TTFT.median() + 0.5), Speed: m.Speed.median(),
		})
	}
	slices.SortFunc(out, func(a, b ModelSpeed) int {
		switch {
		case a.Speed != b.Speed:
			if a.Speed > b.Speed {
				return -1
			}
			return 1
		case a.Requests != b.Requests:
			return b.Requests - a.Requests
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out
}
