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

// ProviderSpeed is one model's place on one provider: its speed as
// ModelSpeed's, over that provider's requests, and how its tries there
// went. Facts only: a failed try may be the agent's request as much as
// the provider, and a reply naming another model may be a name the
// provider's list doesn't give.
type ProviderSpeed struct {
	ModelSpeed
	Provider string `json:"provider"`
	// Tries are the tries sent to the provider for the model, not
	// rejected before sending, a probe or canceled by the agent
	Tries int `json:"tries"`
	// Failed are those of Tries that failed, handed on to another
	// provider or not (Row.Failed, Row.HandedOn)
	Failed int `json:"failed,omitempty"`
	// Swapped are those of Tries whose reply named another model than
	// the one asked for (Row.Swapped)
	Swapped int `json:"swapped,omitempty"`
}

// modelHist is what one model's counted requests measured on one
// provider, and how its tries there went.
type modelHist struct {
	Requests int       `json:"n"`
	Speed    speedHist `json:"s,omitempty"`
	TTFT     speedHist `json:"t,omitempty"`
	Tries    int       `json:"r,omitempty"`
	Failed   int       `json:"f,omitempty"`
	Swapped  int       `json:"w,omitempty"`
}

// speedRank collects the histograms of every model on every provider,
// keyed rankKey; a chunk keeps its own (chunkPart.speeds) and a page
// merges those of the chunks it reads.
type speedRank map[string]*modelHist

// rankKey is model and provider as one key of a speedRank.
func rankKey(model, provider string) string { return model + "\x00" + provider }

// rankOf is the model and provider of a rankKey.
func rankOf(k string) (model, provider string) {
	model, provider, _ = strings.Cut(k, "\x00")
	return model, provider
}

func (s speedRank) of(key string) *modelHist {
	m := s[key]
	if m == nil {
		m = &modelHist{Speed: speedHist{}, TTFT: speedHist{}}
		// unique's copy, so a chunk's part holds none of the chunk's strings
		s[unique.Make(key).Value()] = m
	}
	return m
}

// add counts r's try on its provider, when it is not rejected or canceled;
// and its speed when it is fair to rank by: not a try handed on to
// another, not failed, its first token timed, and it wrote MinRankTokens
// or more.
func (s speedRank) add(r *Row) {
	if r.IsRejected() || r.Canceled() {
		return
	}
	m := s.of(rankKey(r.Model, r.Provider))
	m.Tries++
	switch {
	case r.HandedOn || r.Failed():
		m.Failed++
		return
	case r.Swapped:
		m.Swapped++
	}
	if r.TTFT <= 0 || r.Output < MinRankTokens {
		return
	}
	m.Requests++
	m.TTFT[histBin(float64(r.TTFT))]++
	if n, w := r.Decode(); w > 0 && n >= MinRankTokens {
		m.Speed[histBin(float64(n)*1000/float64(w))]++
	}
}

// merge adds o's counts to s's.
func (s speedRank) merge(o speedRank) {
	for k, x := range o {
		m := s.of(k)
		m.Requests += x.Requests
		m.Tries += x.Tries
		m.Failed += x.Failed
		m.Swapped += x.Swapped
		for b, c := range x.Speed {
			m.Speed[b] += c
		}
		for b, c := range x.TTFT {
			m.TTFT[b] += c
		}
	}
}

// speedOf is m's medians as model's ModelSpeed.
func (m *modelHist) speedOf(model string) ModelSpeed {
	return ModelSpeed{
		Model: model, Requests: m.Requests, SpeedN: m.Speed.total(),
		TTFT: int64(m.TTFT.median() + 0.5), Speed: m.Speed.median(),
	}
}

// result is the ranking of models, every provider's requests together,
// the fastest first; models whose speed wasn't measured follow, the one
// with more requests first. A model with no request that counted isn't
// in it.
func (s speedRank) result() []ModelSpeed {
	byModel := speedRank{}
	for k, x := range s {
		if x.Requests == 0 {
			continue
		}
		model, _ := rankOf(k)
		byModel.merge(speedRank{model: x})
	}
	out := make([]ModelSpeed, 0, len(byModel))
	for model, m := range byModel {
		out = append(out, m.speedOf(model))
	}
	slices.SortFunc(out, fasterFirst)
	return out
}

// providers is each model on each provider it was tried on, a model's
// fastest provider first, and the models in the order of result.
func (s speedRank) providers() []ProviderSpeed {
	out := make([]ProviderSpeed, 0, len(s))
	for k, m := range s {
		model, prov := rankOf(k)
		out = append(out, ProviderSpeed{ModelSpeed: m.speedOf(model), Provider: prov, Tries: m.Tries, Failed: m.Failed, Swapped: m.Swapped})
	}
	place := map[string]int{}
	for i, m := range s.result() {
		place[m.Model] = i + 1
	}
	slices.SortFunc(out, func(a, b ProviderSpeed) int {
		pa, pb := place[a.Model], place[b.Model]
		switch {
		case pa != pb:
			// models without a counted request last
			if pa == 0 || pb != 0 && pa > pb {
				return 1
			}
			return -1
		case a.Model != b.Model:
			return strings.Compare(a.Model, b.Model)
		}
		if c := fasterFirst(a.ModelSpeed, b.ModelSpeed); c != 0 {
			return c
		}
		if a.Tries != b.Tries {
			return b.Tries - a.Tries
		}
		return strings.Compare(a.Provider, b.Provider)
	})
	return out
}

// fasterFirst orders a ranking: the fastest first, then the one with
// more requests, then by name.
func fasterFirst(a, b ModelSpeed) int {
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
}
