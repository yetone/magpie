package usage

import (
	"github.com/yetone/magpie/internal/sessions"
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestIndexedExportMatchesExhaustiveOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 100; trial++ {
		var recs []Record
		var logs []sessions.Call
		start := time.Now()
		for i := 0; i < 80; i++ {
			r := Record{Time: start.Add(time.Duration(rng.Intn(15)) * time.Second), Agent: "codex", Session: []string{"s", "t", ""}[rng.Intn(3)], Model: "m", Input: rng.Intn(4), Status: 200, Millis: int64(rng.Intn(4) * 1000)}
			if rng.Intn(3) == 0 {
				r.RequestID = []string{"a", "b", "c"}[rng.Intn(3)]
			}
			if rng.Intn(5) == 0 {
				r.Status = 500
				r.Error = "failed"
			}
			if rng.Intn(7) == 0 {
				r.Rejected = true
			}
			if rng.Intn(2) == 0 {
				r.NativeSession = r.Session
				r.Session = "overridden"
			}
			recs = append(recs, r)
			c := sessions.Call{Time: start.Add(time.Duration(rng.Intn(15)) * time.Second), Agent: "codex", Session: []string{"s", "t", ""}[rng.Intn(3)], Tokens: sessions.Tokens{Input: rng.Intn(4)}}
			if rng.Intn(3) == 0 {
				c.RequestID = []string{"a", "b", "c"}[rng.Intn(3)]
			}
			if rng.Intn(5) == 0 {
				c.Error = "failed"
			}
			logs = append(logs, c)
		}
		got, want := gatewayMatches(recs, logs), exhaustiveGatewayMatches(recs, logs)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d: got %v want %v", trial, got, want)
		}
	}
}

func BenchmarkExportAmbiguousSession(b *testing.B) {
	for _, n := range []int{1000, 4000, 8000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			recs := make([]Record, n)
			logs := make([]sessions.Call, n)
			now := time.Now()
			for i := range recs {
				recs[i] = Record{Time: now, Agent: "codex", Session: "s", Input: 1, Status: 200}
				logs[i] = sessions.Call{Time: now, Agent: "codex", Session: "s", Tokens: sessions.Tokens{Input: 1}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				gatewayMatches(recs, logs)
			}
		})
	}
}
func exhaustiveGatewayMatches(recs []Record, logs []sessions.Call) map[int]bool {
	byID, bySession := map[string][]int{}, map[string][]int{}
	for i, r := range recs {
		if r.IsRejected() {
			continue
		}
		if r.RequestID != "" {
			byID[r.RequestID] = append(byID[r.RequestID], i)
		}
		session := r.NativeSession
		if session == "" {
			session = r.Session
		}
		if session != "" {
			bySession[session] = append(bySession[session], i)
		}
	}
	matched, used := map[int]bool{}, map[int]bool{}
	for j, c := range logs {
		if c.RequestID == "" {
			continue
		}
		for _, i := range byID[c.RequestID] {
			if !used[i] {
				matched[j], used[i] = true, true
				break
			}
		}
	}
	candidates := map[int][]int{}
	counts := map[int]int{}
	for j, c := range logs {
		if matched[j] || c.Session == "" {
			continue
		}
		for _, i := range bySession[c.Session] {
			r := recs[i]
			if used[i] || c.RequestID != "" && r.RequestID != "" {
				continue
			}
			// Empty successes carry too little evidence. Failed calls may have
			// zero tokens, but both sources must agree that the call failed.
			if c.Input+c.Output+c.CacheRead+c.CacheWrite == 0 && (c.Error == "" || !r.Failed()) {
				continue
			}
			if (c.Error != "") != r.Failed() {
				continue
			}

			if AgentOf(r.Agent) != c.Agent || r.Input != c.Input || r.Output != c.Output || r.CacheRead != c.CacheRead || r.CacheWrite != c.CacheWrite {
				continue
			}
			end := r.Time.Add(time.Duration(r.Millis) * time.Millisecond)
			if c.Time.Before(end.Add(-2*time.Second)) || c.Time.After(end.Add(2*time.Second)) {
				continue
			}
			candidates[j] = append(candidates[j], i)
			counts[i]++
		}
	}
	for j, cs := range candidates {
		if len(cs) == 1 && counts[cs[0]] == 1 {
			matched[j] = true
		}
	}
	return matched
}
