// Package guard runs AstrLink Guard (QuantumNous/astrlink-guard,
// Apache-2.0), a token classifier for secrets and personal data in Chinese
// and English text: its int8 ONNX model (37 MB) read by onnx.go and run
// by bert.go on the CPU in Go, or on a Mac by CoreML (coreml.go),
// its tokenizer by tokenizer.go. Nothing is started.
package guard

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/pii/guard/coreml"
)

// labels are the model's BIO tags, by output index (its labels.json).
var labels = []string{"O",
	"B-email", "I-email", "B-phone", "I-phone", "B-account", "I-account",
	"B-payment_card", "I-payment_card", "B-ip_address", "I-ip_address",
	"B-url", "I-url", "B-common_secret", "I-common_secret",
	"B-private_address", "I-private_address", "B-private_date", "I-private_date",
	"B-private_person", "I-private_person"}

// Files are the model's files Load reads, in its folder.
var Files = []string{"model_int8.onnx", "model_int8.onnx_data", "tokenizer.json"}

// Model is the model, loaded.
type Model struct {
	tok *tokenizer
	net *net
	ml  *coreml.Model // nil where CoreML isn't, or couldn't be made
	// MLErr is why CoreML isn't used, on a Mac.
	MLErr error
	// mlFail is why CoreML's last read failed and bert.go read the window
	// instead; nil once one doesn't.
	mlFail atomic.Pointer[string]
}

// Backend is what reads the model now, as Settings › Privacy names it:
// via "CoreML" on the device its compute plan puts most of the program
// on ("Neural Engine", "GPU" or "CPU"; "" where the plan can't be read),
// or bert.go on the CPU (via ""), and then on a Mac why not CoreML: it
// couldn't be made, or its last read failed. Settings said "on the CPU"
// while CoreML read every window (yyhhyyyyyy on Discord, 10-27).
func (m *Model) Backend() (via, device string, err error) {
	if m.ml == nil {
		if !coreml.Available {
			return "", "CPU", nil
		}
		return "", "CPU", m.MLErr
	}
	if e := m.mlFail.Load(); e != nil {
		return "", "CPU", errors.New(*e)
	}
	return "CoreML", m.ml.Device, nil
}

// Span is what the model found: text[Start:End], a Label of its (email,
// phone, account, payment_card, ip_address, url, common_secret,
// private_address, private_date, private_person), and the least of its
// tokens' probabilities.
type Span struct {
	Start, End int
	Label      string
	Score      float64
}

// MinScore is the publisher's default threshold, under which masking
// drops a span unless the user set another (internal/masking).
const MinScore = 0.8

// Floor is the probability under which Find drops a span: the lowest
// threshold a user can set.
const Floor = 0.5

// window is how many tokens the model reads at once, besides <s> and
// </s>; a longer text is read in windows overlapping by overlap, each
// token labelled by the window it is furthest inside.
const (
	window  = maxPos - 2
	overlap = 128
)

// Load reads the model in dir.
func Load(dir string) (*Model, error) {
	tok, err := loadTokenizer(filepath.Join(dir, "tokenizer.json"), vocabSize)
	if err != nil {
		return nil, fmt.Errorf("tokenizer: %w", err)
	}
	nt, err := loadNet(filepath.Join(dir, "model_int8.onnx"))
	if err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	if nt.labels != len(labels) {
		return nil, fmt.Errorf("model: %d labels, not %d", nt.labels, len(labels))
	}
	m := &Model{tok: tok, net: nt}
	m.ml, m.MLErr = openCoreML(dir, nt)
	return m, nil
}

// logits are the network's scores for ids, from CoreML when it is there
// and the window is long enough to gain by it, else over procs goroutines
// (0: as many of cores as are free, holding them while it reads).
func (m *Model) logits(ids []int, procs int) []float32 {
	if m.ml != nil && len(ids) >= coremlMin {
		lg, err := coremlRead(m.ml, m.net, ids)
		if err == nil {
			m.mlFail.Store(nil)
			return lg
		}
		e := err.Error()
		m.mlFail.Store(&e)
	}
	if procs == 0 { // CoreML's reader: a core of cores, and all that are free
		procs = take(Cores())
		defer give(procs)
	}
	return m.net.logits(ids, procs)
}

// Cores is how many cores the model reads on at most: half of them, and
// never more than 4. It reads beside the agents and everything else on
// the computer, and on all of them a long history unread kept every core
// busy for as long as requests came (xinvxueyuan and shensmart on Discord,
// 10-28: a 32-core Linux box spent 45 CPU seconds on each request).
func Cores() int { return max(1, min(4, runtime.GOMAXPROCS(0)/2)) }

// cores are the Cores() the model reads on, shared by every Find at once:
// a window read on the CPU holds one or more, so requests read side by
// side never take more than Cores() between them.
var cores = make(chan struct{}, Cores())

// take holds a core of cores, waiting for one, and up to want-1 more of
// those free now: how many it holds, to give back.
func take(want int) int {
	cores <- struct{}{}
	n := 1
	for n < want {
		select {
		case cores <- struct{}{}:
			n++
		default:
			return n
		}
	}
	return n
}

func give(n int) {
	for range n {
		<-cores
	}
}

// Find is what the model finds in text, at Floor, each span with its score.
// A text longer than a window is read a window at a time, overlapping.
// On the CPU those windows are read at once, one a core (of Cores()): a
// window's own matmuls split over the cores leave most of each idle in
// the steps between them (a 4-core server read a long prompt for minutes).
// Finds called at once share those cores (cores): a window takes one,
// waiting while others have them all, and splits its matmuls over as many
// more as are free, up to its share of them, so a lone short text is read
// on every core and several texts at once a core each.
func (m *Model) Find(text string) []Span {
	toks := m.tok.encode(text)
	n := len(toks)
	if n == 0 {
		return nil
	}
	var starts []int
	for start := 0; ; start = start + window - overlap {
		starts = append(starts, start)
		if start+window >= n {
			break
		}
	}
	lgs := make([][]float32, len(starts))
	ids := func(w int) []int {
		start := starts[w]
		end := min(start+window, n)
		ids := make([]int, 0, end-start+2)
		ids = append(ids, 0)
		for _, t := range toks[start:end] {
			ids = append(ids, t.id)
		}
		return append(ids, 2)
	}
	if m.ml != nil {
		for w := range starts {
			lgs[w] = m.logits(ids(w), 0)
		}
	} else {
		at := min(Cores(), len(starts))
		var next atomic.Int32
		var wg sync.WaitGroup
		var c carried
		for range at {
			wg.Go(func() {
				defer c.catch()
				for w := int(next.Add(1)) - 1; w < len(starts); w = int(next.Add(1)) - 1 {
					func() {
						procs := take(max(1, Cores()/at))
						defer give(procs)
						lgs[w] = m.net.logits(ids(w), procs)
					}()
				}
			})
		}
		wg.Wait()
		c.rethrow()
	}
	lab := make([]int, n)
	prob := make([]float64, n)
	margin := make([]int, n)
	for i := range margin {
		margin[i] = -1
	}
	k := len(labels)
	for w, start := range starts {
		end := min(start+window, n)
		lg := lgs[w]
		for i := 0; i < end-start; i++ {
			j := start + i
			mg := min(i, end-start-1-i)
			if mg <= margin[j] {
				continue
			}
			row := lg[(i+1)*k : (i+2)*k]
			best, mx := 0, row[0]
			for c, v := range row {
				if v > mx {
					best, mx = c, v
				}
			}
			var sum float64
			for _, v := range row {
				sum += math.Exp(float64(v - mx))
			}
			margin[j], lab[j], prob[j] = mg, best, 1/sum
		}
	}
	return decode(toks, lab, prob)
}

// carried is the first panic in the goroutines a read starts, panicked
// again by rethrow on the goroutine that waited for them, where Find's
// caller can recover it: a panic left on a goroutine of its own ends the
// whole process (#4553 ended magpie on Windows).
type carried struct{ p atomic.Pointer[any] }

// catch is deferred by each goroutine.
func (c *carried) catch() {
	if r := recover(); r != nil {
		c.p.CompareAndSwap(nil, &r)
	}
}

// rethrow is called once they have all ended.
func (c *carried) rethrow() {
	if p := c.p.Load(); p != nil {
		panic(*p)
	}
}

// decode reads the tokens' BIO tags (lab, by index into labels) into
// spans: a B tag, or an I tag of another kind than the span before it,
// starts one; an I tag of the same kind extends it; O ends it. A span's
// score is its least token's, and those under Floor are dropped.
// Tokens of no text (a lone "▁") neither start nor end a span.
func decode(toks []token, lab []int, prob []float64) []Span {
	var spans []Span
	cur := -1
	for j, t := range toks {
		if t.start == t.end {
			continue
		}
		tag := labels[lab[j]]
		if tag == "O" {
			cur = -1
			continue
		}
		kind := tag[2:]
		if tag[0] == 'B' || cur < 0 || spans[cur].Label != kind {
			spans = append(spans, Span{t.start, t.end, kind, prob[j]})
			cur = len(spans) - 1
			continue
		}
		spans[cur].End = t.end
		spans[cur].Score = min(spans[cur].Score, prob[j])
	}
	out := spans[:0]
	for _, s := range spans {
		if s.Score >= Floor {
			out = append(out, s)
		}
	}
	return out
}
