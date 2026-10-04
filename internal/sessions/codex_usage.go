package sessions

import (
	"encoding/json"
	"maps"
	"time"
)

// codexUsageState is shared by the summary and request readers. It is a
// transient index: caches store aggregates and calls, not these observations.
// A response's thread counter includes compaction requests that the legacy
// counter omits. Only token_count observations advance High/Epoch. A paired
// response retains both checkpoints so replays never compare different domains.
type codexUsageState struct {
	// Pending is the last unpaired usage observation (index + 1). Input or
	// turn boundaries clear it, so equal usage from another call is not paired.
	Pending        int
	Entries        []codexContribution
	Intervals      map[codexInterval][]int
	Responses      map[string]int
	High           *cxUsage
	HighAt         time.Time
	Epoch          int
	Session        string
	Turn           string
	Models         map[string]string
	Conflicts      int
	Compactions    map[string]codexContribution
	CounterBases   []codexCounterBase
	SnapshotAt     time.Time
	SnapshotThread string
	SnapshotTotal  *cxUsage
	SnapshotOffset *cxUsage
}

// A paginated subagent starts with a restored compaction and a zero-last
// legacy checkpoint at the same timestamp. They describe the same boundary,
// including history no longer present in the file, without adding that history.
type cxHistorySnapshot struct {
	HistoryMode  string          `json:"history_mode"`
	HistoryBase  json.RawMessage `json:"history_base"`
	HistoryStart int64           `json:"subagent_history_start_ordinal"`
}

func (s *codexUsageState) beginSnapshot(at time.Time, thread string, h cxHistorySnapshot) {
	if h.HistoryMode == "paginated" && (len(h.HistoryBase) == 0 || string(h.HistoryBase) == "null") && h.HistoryStart > 0 && thread != "" {
		s.SnapshotAt, s.SnapshotThread = at, thread
	}
}

func (s *codexUsageState) alignSnapshot(at time.Time, total, last *cxUsage) {
	defer func() { s.SnapshotThread = "" }()
	if s.SnapshotThread == "" || s.SnapshotTotal == nil || s.High != nil || !at.Equal(s.SnapshotAt) ||
		last == nil || last.Known != 31 || !last.zero() || total.Known != 31 || s.SnapshotTotal.Known != 31 || !s.SnapshotTotal.covers(*total) {
		return
	}
	for _, v := range s.Entries {
		if !v.Compaction || !v.At.Equal(s.SnapshotAt) {
			return
		}
	}
	offset := s.SnapshotTotal.less(*total)
	for _, c := range s.Compactions {
		if c.Session != s.Session || c.Usage.Known != 31 || !c.At.Equal(s.SnapshotAt) || !offset.covers(c.Usage) {
			return
		}
		offset = offset.less(c.Usage)
	}
	s.SnapshotOffset = &offset
}

type codexCounterBase struct {
	Session     string
	Epoch       int
	At          time.Time
	PreviousAt  time.Time
	Base, Start cxUsage
	Confirmed   bool
}

type cxRecordedUsage struct {
	ThreadID    string   `json:"thread_id"`
	SessionID   string   `json:"session_id"`
	TurnID      string   `json:"turn_id"`
	ResponseID  string   `json:"response_id"`
	Usage       *cxUsage `json:"usage"`
	ThreadUsage *cxUsage `json:"thread_token_usage"`
}

type cxCompaction struct {
	ResponseID string           `json:"compaction_response_id"`
	Record     *cxRecordedUsage `json:"latest_token_usage_record"`
}

type codexContribution struct {
	ResponseID, Session, Turn, Model string
	Epoch                            int
	At, Updated                      time.Time
	Usage                            cxUsage
	Total                            *cxUsage
	CountTotal                       *cxUsage
	CountEpoch                       int
	ExpectedCount                    *cxUsage
	Compaction                       bool
}

type codexChange struct {
	Index int
	Old   *codexContribution
	Value codexContribution
}

// Explicit compaction identity adds the compaction call once, even when its
// usage appears in both an embedded snapshot and a top-level RECORD.
func (s *codexUsageState) compacted(at time.Time, model string, p cxCompaction) *codexChange {
	r := p.Record
	if r == nil || p.ResponseID == "" || r.ResponseID != p.ResponseID || r.Usage == nil || !r.Usage.valid() || r.Usage.Known&3 != 3 {
		return nil
	}
	session := r.SessionID
	if session == "" {
		session = s.Session
	}
	key := session + "\x00" + p.ResponseID
	if _, ok := s.Compactions[key]; ok {
		return nil
	}
	var change *codexChange
	if i, ok := s.Responses[key]; ok {
		old := s.Entries[i]
		if !old.Usage.equal(*r.Usage) {
			s.Conflicts++
			return nil
		}
		v := old
		v.Compaction = true
		s.Entries[i] = v
		change = &codexChange{Index: i, Old: &old, Value: v}
	} else {
		change = s.record(codexContribution{ResponseID: r.ResponseID, Session: session, Turn: r.TurnID, Model: model, At: at, Usage: *r.Usage, Total: r.ThreadUsage, Compaction: true})
		if change == nil {
			return nil
		}
	}
	if s.Compactions == nil {
		s.Compactions = map[string]codexContribution{}
	}
	s.Compactions[key] = change.Value
	if s.SnapshotThread != "" && r.ThreadID == s.SnapshotThread && at.Equal(s.SnapshotAt) && r.ThreadUsage != nil && r.ThreadUsage.valid() && len(s.Compactions) == 1 {
		v := *r.ThreadUsage
		s.SnapshotTotal = &v
	}
	return change
}

func (s *codexUsageState) projectCount(v codexContribution) (*cxUsage, int) {
	if v.Total == nil || v.Compaction {
		return nil, v.Epoch
	}
	offset := cxUsage{Known: 31}
	if s.SnapshotOffset != nil && s.SnapshotTotal != nil && !v.At.Before(s.SnapshotAt) && v.Total.covers(*s.SnapshotTotal) {
		offset = *s.SnapshotOffset
	}
	for _, c := range s.Compactions {
		// A late response from before compaction belongs to the earlier domain.
		if c.Session != v.Session || v.At.Before(c.At) || c.Total != nil && !v.Total.covers(*c.Total) {
			continue
		}
		offset.Input += c.Usage.Input
		offset.Output += c.Usage.Output
		offset.Cached += c.Usage.Cached
		offset.CacheWrite += c.Usage.CacheWrite
		offset.Reasoning += c.Usage.Reasoning
		offset.Known &= c.Usage.Known
	}
	if !v.Total.covers(offset) {
		return nil, v.Epoch
	}
	total := v.Total.less(offset)
	epoch := v.Epoch
	var base *cxUsage
	for _, b := range s.CounterBases {
		if b.Session != v.Session {
			continue
		}
		first := b.Start.less(b.Base)
		provenFirst := total.matches(b.Start) && v.Usage.matches(first) && !v.At.After(b.At)
		if total.covers(b.Start) && (b.Confirmed || provenFirst) {
			copy := b.Base
			base, epoch = &copy, b.Epoch
		} else if !v.At.After(b.PreviousAt) {
			epoch = min(epoch, b.Epoch-1)
		}
	}
	if base != nil {
		total = total.less(*base)
	}
	if offset.zero() && base == nil {
		return nil, epoch
	}
	return &total, epoch
}

func (s *codexUsageState) confirmBase(v codexContribution) {
	if v.CountTotal == nil || v.ExpectedCount == nil || !v.CountTotal.matches(*v.ExpectedCount) {
		return
	}
	for i, b := range s.CounterBases {
		if b.Session == v.Session && b.Epoch == v.CountEpoch {
			s.CounterBases[i].Confirmed = true
		}
	}
}

func (s *codexUsageState) clone() *codexUsageState {
	if s == nil {
		return nil
	}
	c := *s
	c.Entries = append([]codexContribution(nil), s.Entries...)
	c.Responses, c.Models = maps.Clone(s.Responses), maps.Clone(s.Models)
	c.Compactions = maps.Clone(s.Compactions)
	c.CounterBases = append([]codexCounterBase(nil), s.CounterBases...)
	c.Intervals = maps.Clone(s.Intervals)
	return &c // Usage pointers are immutable after publication.
}

func (u *cxUsage) UnmarshalJSON(b []byte) error {
	// Pointer fields distinguish an absent/null counter from an explicit zero
	// in one decode, without a second map of copied RawMessages per event.
	var wire struct {
		Input      *int   `json:"input_tokens"`
		Output     *int   `json:"output_tokens"`
		Cached     *int   `json:"cached_input_tokens"`
		CacheWrite *int   `json:"cache_write_input_tokens"`
		Reasoning  *int   `json:"reasoning_output_tokens"`
		Known      *uint8 `json:"known"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	*u = cxUsage{}
	values := [5]*int{wire.Input, wire.Output, wire.Cached, wire.CacheWrite, wire.Reasoning}
	fields := [5]*int{&u.Input, &u.Output, &u.Cached, &u.CacheWrite, &u.Reasoning}
	for i, value := range values {
		if value != nil {
			*fields[i] = *value
			u.Known |= 1 << i
		}
	}
	if wire.Known != nil {
		u.Known = *wire.Known
	}
	return nil
}

func (u cxUsage) valid() bool {
	return u.Input >= 0 && u.Output >= 0 && u.Cached >= 0 && u.CacheWrite >= 0 && u.Reasoning >= 0 && u.Cached+u.CacheWrite <= u.Input
}

func (u cxUsage) equal(v cxUsage) bool {
	return u.Input == v.Input && u.Output == v.Output && u.Cached == v.Cached && u.CacheWrite == v.CacheWrite && u.Reasoning == v.Reasoning
}

func (u cxUsage) covers(v cxUsage) bool {
	return u.Input >= v.Input && u.Output >= v.Output && u.Cached >= v.Cached && u.CacheWrite >= v.CacheWrite && u.Reasoning >= v.Reasoning
}

func (u cxUsage) less(v cxUsage) cxUsage {
	return cxUsage{Input: u.Input - v.Input, Output: u.Output - v.Output, Cached: u.Cached - v.Cached, CacheWrite: u.CacheWrite - v.CacheWrite, Reasoning: u.Reasoning - v.Reasoning, Known: u.Known & v.Known}
}

func (u cxUsage) plus(v cxUsage) cxUsage {
	return cxUsage{Input: u.Input + v.Input, Output: u.Output + v.Output, Cached: u.Cached + v.Cached, CacheWrite: u.CacheWrite + v.CacheWrite, Reasoning: u.Reasoning + v.Reasoning, Known: u.Known & v.Known}
}

func (u cxUsage) zero() bool { return u.equal(cxUsage{}) }

// Matching needs the prompt and completion counts. Optional missing fields do
// not prove zero, and any common, explicitly supplied field must agree.
func (u cxUsage) matches(v cxUsage) bool {
	if u.Known&3 != 3 || v.Known&3 != 3 {
		return false
	}
	a := [5]int{u.Input, u.Output, u.Cached, u.CacheWrite, u.Reasoning}
	b := [5]int{v.Input, v.Output, v.Cached, v.CacheWrite, v.Reasoning}
	for i := range a {
		if u.Known&v.Known&(1<<i) != 0 && a[i] != b[i] {
			return false
		}
	}
	return true
}

func codexCheckpoint(v codexContribution) (*cxUsage, int) {
	if v.CountTotal != nil {
		return v.CountTotal, v.CountEpoch
	}
	if v.ExpectedCount != nil {
		return v.ExpectedCount, v.Epoch
	}
	return v.Total, v.Epoch
}

func sameCodexInterval(a, b codexContribution) bool {
	at, ae := codexCheckpoint(a)
	bt, be := codexCheckpoint(b)
	return !a.Compaction && !b.Compaction && ae == be && a.Session == b.Session && (a.Turn == "" || b.Turn == "" || a.Turn == b.Turn) &&
		at != nil && bt != nil && at.matches(*bt) && a.Usage.matches(b.Usage)
}

func (s *codexUsageState) model(turn, fallback string) string {
	if m := s.Models[turn]; turn != "" && m != "" {
		return m
	}
	return fallback
}

func (s *codexUsageState) context(session, turn, model string) {
	if turn != "" && turn != s.Turn {
		s.Pending = 0
	}
	if session != "" {
		s.Session = session
	}
	if turn != "" {
		s.Turn = turn
	}
	if model != "" && s.Turn != "" {
		if s.Models == nil {
			s.Models = map[string]string{}
		}
		s.Models[s.Turn] = model
	}
}

func (s *codexUsageState) advance(at time.Time, total *cxUsage) {
	if total != nil && total.valid() && (s.High == nil || total.covers(*s.High)) {
		v := *total
		if s.High == nil || at.After(s.HighAt) {
			s.HighAt = at
		}
		s.High = &v
	}
}

type codexInterval struct {
	Session                                       string
	Epoch, TotalInput, TotalOutput, Input, Output int
	Valid                                         bool
}

func codexIntervalKey(v codexContribution) codexInterval {
	total, epoch := codexCheckpoint(v)
	if total == nil {
		return codexInterval{}
	}
	return codexInterval{v.Session, epoch, total.Input, total.Output, v.Usage.Input, v.Usage.Output, true}
}

func (s *codexUsageState) index(i int, v codexContribution) {
	key := codexIntervalKey(v)
	if !key.Valid {
		return
	}
	if s.Intervals == nil {
		s.Intervals = map[codexInterval][]int{}
	}
	// The old immutable snapshot may still reference the previous slice.
	s.Intervals[key] = append(append([]int(nil), s.Intervals[key]...), i)
}

func (s *codexUsageState) record(v codexContribution) *codexChange {
	pending := s.Pending
	s.Pending = 0
	if v.ResponseID == "" || v.At.IsZero() || !v.Usage.valid() || v.Usage.Known&3 != 3 {
		return nil
	}
	if v.Session == "" {
		v.Session = s.Session
	} else if s.Session == "" {
		s.Session = v.Session
	}
	if v.Turn == "" {
		v.Turn = s.Turn
	}
	v.Epoch = s.Epoch
	v.Model = s.model(v.Turn, v.Model)
	v.Updated = v.At
	v.ExpectedCount, v.Epoch = s.projectCount(v)
	key := v.Session + "\x00" + v.ResponseID
	if s.Responses == nil {
		s.Responses = map[string]int{}
	}
	if i, ok := s.Responses[key]; ok {
		old := s.Entries[i]
		v.Epoch = old.Epoch
		v.CountTotal, v.CountEpoch = old.CountTotal, old.CountEpoch
		v.Compaction = old.Compaction
		if old.Turn != "" && v.Turn != "" && old.Turn != v.Turn {
			s.Conflicts++
			return nil
		}
		if old.Usage.equal(v.Usage) && old.Usage.Known == v.Usage.Known && old.Model == v.Model && !(old.Total == nil && v.Total != nil) {
			if !v.Updated.After(old.Updated) {
				return nil
			}
			// A repeated current snapshot advances revision order, but not the
			// completion time or counting date. Keep the watermark in the live
			// index; a changed cold source reconstructs it from the original events.
			updated := old
			updated.Updated = v.Updated
			s.Entries[i] = updated
			return &codexChange{Index: i, Old: &old, Value: updated}
		}
		// Event time is the source's revision order. A replay of an older
		// snapshot cannot undo an accepted update; a tied conflict is ambiguous.
		if !v.Updated.After(old.Updated) {
			s.Conflicts++
			return nil
		}
		v.At = old.At
		s.Entries[i] = v
		if codexIntervalKey(old) != codexIntervalKey(v) {
			s.index(i, v)
		}
		return &codexChange{Index: i, Old: &old, Value: v}
	}
	match := -1
	for _, i := range s.Intervals[codexIntervalKey(v)] {
		old := s.Entries[i]
		if old.ResponseID == "" && sameCodexInterval(old, v) {
			if match >= 0 && match != i {
				match = -2
				break
			}
			match = i
		}
	}
	// A page can start after an unavailable parent or a runtime reset. The
	// paired local observations still describe one call even when their two
	// cumulative counters have different bases. Consume each observation once.
	if match == -1 && !v.Compaction && pending > 0 {
		old := s.Entries[pending-1]
		if old.ResponseID == "" && old.Turn == v.Turn && old.Session == v.Session && old.Usage.matches(v.Usage) {
			match = pending - 1
		}
	}
	if match >= 0 {
		old := s.Entries[match]
		v.CountTotal, v.CountEpoch = old.Total, old.Epoch
		s.confirmBase(v)
		s.Entries[match], s.Responses[key] = v, match
		s.index(match, v)
		return &codexChange{Index: match, Old: &old, Value: v}
	}
	if match == -2 {
		s.Conflicts++
	}
	i := len(s.Entries)
	s.Entries = append(s.Entries, v)
	s.index(i, v)
	s.Responses[key] = i
	if !v.Compaction {
		s.Pending = i + 1
	}
	return &codexChange{Index: i, Value: v}
}

func (s *codexUsageState) count(at time.Time, model string, total, last *cxUsage) *codexChange {
	if total == nil || !total.valid() || total.Known&3 != 3 || at.IsZero() {
		return nil
	}
	s.alignSnapshot(at, total, last)
	if last != nil && last.Known&3 != 3 {
		s.Conflicts++
		last = nil
	}
	// A delayed count can arrive after a later response. Its observation time
	// cannot turn a proven old interval into a fresh epoch. Legacy-only files
	// retain their existing counter-reset interpretation.
	if last != nil && len(s.Responses) > 0 {
		candidate := codexContribution{Epoch: s.Epoch, Session: s.Session, Turn: s.Turn, Usage: *last, Total: total}
		for _, i := range s.Intervals[codexIntervalKey(candidate)] {
			if sameCodexInterval(s.Entries[i], candidate) {
				old := s.Entries[i]
				if old.ResponseID == "" || old.CountTotal != nil {
					if s.High != nil && !total.covers(*s.High) && at.After(s.HighAt) && total.matches(*last) {
						continue // the same numbers can recur after a later runtime reset
					}
					return nil
				}
				v := old
				// Both counters can restart together after legacy-only history.
				// This response and its checkpoint explicitly agree on the new
				// first request; do not leave High in the previous runtime.
				if s.High != nil && !s.High.zero() && last.valid() && total.matches(*last) && !total.matches(*s.High) &&
					at.After(s.HighAt) && old.At.After(s.HighAt) && old.ExpectedCount == nil && old.Total != nil && old.Total.matches(*total) {
					s.Epoch++
					s.High = nil
					v.Epoch = s.Epoch
				}
				v.CountTotal, v.CountEpoch = total, s.Epoch
				s.confirmBase(v)
				s.Entries[i] = v
				s.index(i, v)
				s.advance(at, total)
				return &codexChange{Index: i, Old: &old, Value: v}
			}
		}
	}
	if last != nil && s.Pending > 0 {
		i := s.Pending - 1
		old := s.Entries[i]
		if old.ResponseID != "" && old.CountTotal == nil && !old.Compaction && old.Turn == s.Turn && old.Session == s.Session && old.Usage.matches(*last) {
			v := old
			v.CountTotal, v.CountEpoch = total, s.Epoch
			s.Entries[i] = v
			s.index(i, v)
			s.Pending = 0
			// This is a local counter boundary, including resets and missing history.
			copy := *total
			s.High, s.HighAt = &copy, at
			return &codexChange{Index: i, Old: &old, Value: v}
		}
	}
	s.Pending = 0
	prev := s.High
	reset := false
	// A larger first request can exceed the previous runtime's entire total.
	// total == last still proves a fresh counter, even without a decrease.
	firstAfterReset := prev != nil && !prev.zero() && last != nil && last.valid() && !last.zero() && total.matches(*last) && !total.matches(*prev)
	if prev != nil && (!total.covers(*prev) || firstAfterReset) {
		// Source order can cross a clock adjustment or several events can share
		// a timestamp. Only a proven interval above is a replay; time alone
		// cannot discard a decreasing legacy counter.
		s.Epoch++
		if len(s.Responses) > 0 && last != nil && last.valid() && !last.zero() && total.matches(*last) {
			// After a runtime resume legacy usage restarts at one request, while
			// the persisted thread counter continues. Retain the previous domain's
			// full high-water mark, independently of explicit compaction usage.
			base := *prev
			for i := len(s.CounterBases) - 1; i >= 0; i-- {
				if s.CounterBases[i].Session == s.Session && s.CounterBases[i].Confirmed {
					base = base.plus(s.CounterBases[i].Base)
					break
				}
			}
			s.CounterBases = append(s.CounterBases, codexCounterBase{Session: s.Session, Epoch: s.Epoch, At: at, PreviousAt: s.HighAt, Base: base, Start: base.plus(*total)})
			reset = true
		}
		s.High = nil
		prev = nil
	}
	v := codexContribution{Epoch: s.Epoch, Session: s.Session, Turn: s.Turn, Model: s.model(s.Turn, model), At: at, Updated: at, Total: total}
	if last != nil && last.valid() {
		v.Usage = *last
		if reset {
			// A response may precede the checkpoint that reveals the reset.
			// Reconcile only a unique exact checkpoint in the newly proven domain.
			match := -1
			var projected codexContribution
			for i, old := range s.Entries {
				if old.ResponseID == "" || old.CountTotal != nil || old.At.After(at) {
					continue
				}
				candidate := old
				candidate.ExpectedCount, candidate.Epoch = s.projectCount(candidate)
				if sameCodexInterval(candidate, v) {
					if match >= 0 {
						match = -2
						break
					}
					match, projected = i, candidate
				}
			}
			if match >= 0 {
				old := s.Entries[match]
				projected.CountTotal, projected.CountEpoch = total, s.Epoch
				s.confirmBase(projected)
				s.Entries[match] = projected
				s.index(match, projected)
				s.advance(at, total)
				return &codexChange{Index: match, Old: &old, Value: projected}
			}
		}
		for _, i := range s.Intervals[codexIntervalKey(v)] {
			if sameCodexInterval(s.Entries[i], v) {
				s.advance(at, total)
				return nil
			}
		}
	}
	if prev != nil && total.equal(*prev) {
		return nil
	}
	s.advance(at, total)
	// Preserve the legacy delta when there is no paired RECORD. A missing
	// last value or an unexplained gap must not silently discard token usage.
	if prev != nil {
		v.Usage = total.less(*prev)
	} else if last == nil || !last.valid() {
		v.Usage = *total
	}
	if v.Usage.zero() || !v.Usage.valid() {
		return nil
	}
	i := len(s.Entries)
	s.Entries = append(s.Entries, v)
	s.index(i, v)
	s.Pending = i + 1
	return &codexChange{Index: i, Value: v}
}

func codexState(s *state) *codexUsageState {
	if s.Codex == nil {
		s.Codex = &codexUsageState{}
	}
	return s.Codex
}

func codexApply(s *state, change *codexChange) {
	if change == nil {
		return
	}
	if old := change.Old; old != nil {
		s.unuse(dateOf(old.At), old.Model, spent(old.Usage.raw()))
	}
	v := change.Value
	s.use(dateOf(v.At), v.Model, spent(v.Usage.raw()))
}
