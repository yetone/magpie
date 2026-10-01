package usage

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

func TestRequestMatchIndexEquivalent(t *testing.T) {
	// Repeated token totals, named/unnamed retries, zero-token failures,
	// ambiguous timestamps, native sessions and local rejections.
	for seed := int64(0); seed < 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		gateway, local := &rowChunk{}, &rowChunk{Source: sessions.CallSource{Path: "/local"}}
		now := time.Now().Truncate(time.Second)
		for i := 0; i < 180; i++ {
			r := Record{Time: now.Add(time.Duration(rng.Intn(20)) * time.Second), Agent: "codex", Provider: "relay", Session: fmt.Sprint(rng.Intn(4)), Input: rng.Intn(3), Output: rng.Intn(2), Millis: 1000}
			if i%4 == 0 {
				r.RequestID = fmt.Sprint(rng.Intn(10))
			}
			if i%5 == 0 {
				r.Status = 500
			}
			if i%9 == 0 {
				r.Provider = ""
			}
			if i%7 == 0 {
				r.NativeSession = r.Session
				r.Session = "routing override"
			}
			gateway.add(Row{Record: r}, "", int64(i), false)
			r.Time = r.Time.Add(time.Duration(rng.Intn(9)-4) * time.Second)
			if i%3 == 0 {
				r.RequestID = ""
			}
			if r.NativeSession != "" {
				r.Session = r.NativeSession
			}
			local.add(Row{Record: r, Source: "log"}, "", int64(i), r.Failed())
		}
		for _, since := range []time.Time{{}, now.Add(5 * time.Second)} {
			got := matchedLocal(gateway, []*rowChunk{local}, nil, since)
			want := matchedLocalLegacy(gateway, []*rowChunk{local}, nil, since)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d: %d matches, want %d", seed, len(got), len(want))
			}
		}
	}
}

func TestQueryPageIOOutsideCacheLock(t *testing.T) {
	pageHome(t)
	path := filepath.Join(sessions.ClaudeDir(), "projects", "p", "yesterday.jsonl")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("{}\n"), 0600)
	at := time.Now().Add(-48 * time.Hour)
	os.Chtimes(path, at, at)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		queryPage(All, Filter{}, 0, 10, func(s sessions.CallSource) []sessions.Call {
			close(entered)
			<-release
			return []sessions.Call{{Time: at, Agent: "claude", Model: "m", Session: "yesterday", Tokens: sessions.Tokens{Input: 1}}}
		})
	}()
	defer func() { close(release); <-done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("all-period read did not start")
	}
	today := make(chan RequestPage, 1)
	go func() { today <- QueryPage(Today, Filter{}, 0, 1) }()
	select {
	case p := <-today:
		if p.Total != 0 {
			t.Fatal(p.Total)
		}
	case <-time.After(2 * time.Second):
		t.Error("Today waited on All's blocked file IO")
	}
}

func TestPackedGatewayRetainsOversizedSnapshot(t *testing.T) {
	c := &rowChunk{}
	now := time.Now().Round(0)
	for i := 0; i < 170000; i++ {
		c.add(Row{Record: Record{Time: now.Add(time.Duration(i) * time.Second), Agent: "codex", Provider: "relay", Model: "m", Input: 10, RequestID: fmt.Sprint(i)}}, "", int64(i), false)
	}
	if c.Bytes <= 32<<20 {
		t.Fatal("fixture below old gateway eviction threshold")
	}
	packed := c.pack()
	if len(packed.Archive) == 0 || len(packed.Rows) != 0 || packed.dict != nil {
		t.Fatal("gateway snapshot was not compressed")
	}
	got := packed.unpack()
	if got == nil || len(got.Rows) != len(c.Rows) || !reflect.DeepEqual(got.row(160000), c.row(160000)) {
		t.Fatal("archive lost request metadata")
	}
	if len(packed.Rows) != 0 {
		t.Fatal("decoding mutated published snapshot")
	}
}

func TestRequestModelFilterExact(t *testing.T) {
	for _, m := range []string{"gpt-5", "gpt-5-mini", "gpt-5.1"} {
		r := Record{Model: m}
		if got := (Filter{Model: "gpt-5"}).keeps(r); got != (m == "gpt-5") {
			t.Fatal(m, got)
		}
		if !(Filter{Query: "gpt-5"}).keeps(r) {
			t.Fatal("free-text search changed")
		}
	}
}

func BenchmarkMatchedLocal(b *testing.B) {
	for _, n := range []int{1000, 4000, 8000} {
		for _, dense := range []bool{false, true} {
			g, c := &rowChunk{}, &rowChunk{}
			now := time.Now().Round(0)
			for i := 0; i < n; i++ {
				at := now.Add(time.Duration(i) * 5 * time.Second)
				if dense {
					at = now
				}
				r := Record{Time: at, Agent: "codex", Provider: "relay", Session: "same", Input: 10, Output: 1}
				g.add(Row{Record: r}, "", int64(i), false)
				c.add(Row{Record: r, Source: "log"}, "", int64(i), false)
			}
			for _, impl := range []struct {
				name string
				f    func(*rowChunk, []*rowChunk, map[rowRef]bool, time.Time) map[rowRef]bool
			}{{"indexed", matchedLocal}, {"without_index", matchedLocalLegacy}} {
				b.Run(fmt.Sprintf("%d/dense=%v/%s", n, dense, impl.name), func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						got := impl.f(g, []*rowChunk{c}, nil, time.Time{})
						if (!dense && len(got) != n) || (dense && len(got) != 0) {
							b.Fatal(len(got))
						}
					}
				})
			}
		}
	}
}

// The reviewed matcher is retained only as an independent equivalence oracle
// and the without-index arm of the opt-in benchmark.
func matchedLocalLegacy(gateway *rowChunk, chunks []*rowChunk, skip map[rowRef]bool, since time.Time) map[rowRef]bool {
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	byID, bySession := map[string][]int{}, map[string][]int{}
	for i := range gateway.Rows {
		r := gateway.row(i)
		if r.IsRejected() || r.Time.Before(gatewaySince) {
			continue
		}
		if r.RequestID != "" {
			byID[r.RequestID] = append(byID[r.RequestID], i)
		}
		s := r.NativeSession
		if s == "" {
			s = r.Session
		}
		if s != "" {
			bySession[s] = append(bySession[s], i)
		}
	}
	byRequest := map[string][]rowRef{}
	for _, c := range chunks {
		for i, p := range c.Rows {
			ref := rowRef{c, i}
			if skip[ref] || p.Time.Before(since) {
				continue
			}
			id := c.Strings[p.Text[11]]
			if len(byID[id]) > 0 {
				byRequest[id] = append(byRequest[id], ref)
			}
		}
	}
	matched, used := map[rowRef]bool{}, map[int]bool{}
	for id, refs := range byRequest {
		slices.SortFunc(refs, func(a, b rowRef) int {
			if refNewer(a, b) {
				return -1
			}
			if refNewer(b, a) {
				return 1
			}
			return 0
		})
		for j, ref := range refs {
			if j >= len(byID[id]) {
				break
			}
			matched[ref] = true
			used[byID[id][j]] = true
		}
	}
	candidates := map[rowRef][]int{}
	counts := map[int]int{}
	for _, c := range chunks {
		for j, p := range c.Rows {
			ref := rowRef{c, j}
			if skip[ref] || matched[ref] || p.Time.Before(since) {
				continue
			}
			s := c.Strings[p.Text[13]]
			if len(bySession[s]) == 0 {
				continue
			}
			r := c.row(j)
			failed := p.Flags&16 != 0
			for _, i := range bySession[s] {
				g := gateway.row(i)
				if used[i] || r.RequestID != "" && g.RequestID != "" {
					continue
				}
				if r.Input+r.Output+r.CacheRead+r.CacheWrite == 0 && (!failed || !g.Failed()) {
					continue
				}
				if failed != g.Failed() || g.Agent != r.Agent || g.Input != r.Input || g.Output != r.Output || g.CacheRead != r.CacheRead || g.CacheWrite != r.CacheWrite {
					continue
				}
				end := g.Time.Add(time.Duration(g.Millis) * time.Millisecond)
				if r.Time.Before(end.Add(-2*time.Second)) || r.Time.After(end.Add(2*time.Second)) {
					continue
				}
				candidates[ref] = append(candidates[ref], i)
				counts[i]++
			}
		}
	}
	for ref, cs := range candidates {
		if len(cs) == 1 && counts[cs[0]] == 1 {
			matched[ref] = true
		}
	}
	return matched
}

func BenchmarkRequestSnapshot(b *testing.B) {
	c := &rowChunk{}
	now := time.Now().Round(0)
	for i := 0; i < 8000; i++ {
		c.add(Row{Record: Record{Time: now.Add(time.Duration(i) * time.Second), Agent: "codex", Provider: "relay", Model: "gpt-5", Session: "same", RequestID: fmt.Sprint(i), Input: 12000, Output: 100}}, "", int64(i), false)
	}
	c.Bytes -= int64(32 * len(c.Strings))
	c.dict = nil
	compressed := c.pack()
	b.ReportMetric(float64(c.Bytes), "raw-bytes")
	b.ReportMetric(float64(compressed.Bytes), "compressed-bytes")
	b.Run("encode", func(b *testing.B) {
		b.ReportMetric(float64(c.Bytes), "raw-bytes")
		b.ReportMetric(float64(compressed.Bytes), "compressed-bytes")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = c.pack()
		}
	})
	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := compressed.unpack(); len(got.Rows) != 8000 {
				b.Fatal("rows lost")
			}
		}
	})
}
