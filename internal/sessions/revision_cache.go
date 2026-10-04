package sessions

import (
	"sort"
	"time"
)

// Each cache retains revision indexes for at most eight recently written files
// and 8192 index entries. Settled files need only their totals/materialized rows;
// if one grows again, its parser already rebuilds from the source.
const revisionIdle = 5 * time.Minute
const revisionFiles = 8
const revisionEntries = 8192

func recentRevision(mod int64, weight int, now time.Time) bool {
	return weight > 0 && weight <= revisionEntries && mod >= now.Add(-revisionIdle).UnixNano()
}

func (s *codexUsageState) weight() int {
	if s == nil {
		return 0
	}
	return len(s.Entries) + len(s.Intervals) + len(s.Responses) + len(s.Models) + len(s.Compactions) + len(s.CounterBases)
}
func (s *state) revisionWeight() int { return s.Codex.weight() }
func (s *state) withoutRevisions() *state {
	if s.Codex == nil {
		return s
	}
	c := *s
	c.Codex = nil
	return &c
}
func (s *callFile) revisionWeight() int {
	if s.CX == nil {
		return 0
	}
	return s.CX.Usage.weight() + len(s.CX.Contexts) + len(s.CX.Turns) + len(s.CX.Timings)
}
func (s *callFile) withoutRevisions() *callFile {
	if s.CX == nil {
		return s
	}
	c := *s
	c.CX = nil
	return &c
}

type revisionCandidate struct {
	path   string
	mod    int64
	weight int
}

func retainedRevisions(candidates []revisionCandidate, now time.Time) map[string]bool {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].mod != candidates[j].mod {
			return candidates[i].mod > candidates[j].mod
		}
		return candidates[i].path < candidates[j].path
	})
	keep := map[string]bool{}
	used := 0
	for _, c := range candidates {
		if !recentRevision(c.mod, c.weight, now) || len(keep) == revisionFiles || used+c.weight > revisionEntries {
			continue
		}
		keep[c.path] = true
		used += c.weight
	}
	return keep
}

// Called with mu held. Replace snapshots instead of mutating them: a parser
// may still be reading the previous snapshot outside the metadata lock.
func trimSummaryRevisions(now time.Time) {
	var candidates []revisionCandidate
	for path, s := range cache {
		if n := s.revisionWeight(); n > 0 {
			candidates = append(candidates, revisionCandidate{path, s.Mod, n})
		}
	}
	keep := retainedRevisions(candidates, now)
	for _, c := range candidates {
		if !keep[c.path] {
			cache[c.path] = cache[c.path].withoutRevisions()
		}
	}
}

// Called with callsMu held. The row cache and compact continuation cache have
// independent fixed bounds; reading a settled shard cannot make it active.
func trimCallRevisions(now time.Time) {
	var candidates []revisionCandidate
	for path, s := range callCache {
		if n := s.revisionWeight(); n > 0 {
			candidates = append(candidates, revisionCandidate{path, s.Mod, n})
		}
	}
	keep := retainedRevisions(candidates, now)
	for _, c := range candidates {
		if !keep[c.path] {
			callCache[c.path] = callCache[c.path].withoutRevisions()
		}
	}
	candidates = candidates[:0]
	for path, entry := range callContinuations {
		candidates = append(candidates, revisionCandidate{path, entry.state.Mod, entry.state.revisionWeight()})
	}
	keep = retainedRevisions(candidates, now)
	order := callContinuationOrder[:0]
	for _, path := range callContinuationOrder {
		if keep[path] {
			order = append(order, path)
		} else {
			delete(callContinuations, path)
		}
	}
	callContinuationOrder = order
}
