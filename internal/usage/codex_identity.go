package usage

import (
	"hash/maphash"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

func codexIdentityRow(row Row, actor string, identities map[string]sessions.CodexIdentity) Row {
	if !isCodexRow(row) && actor == "" {
		return row
	}
	row.codexActor = actor
	row.SessionRole = "unknown"
	row.Subagent, row.ParentAgent = "", ""
	identity, ok := identities[actor]
	if actor == "" || !ok || identity.ID != actor || identity.Role != "main" && identity.Role != "subagent" {
		return row
	}
	row.SessionRole = identity.Role
	row.Session = identity.RootSession
	if identity.Role == "subagent" {
		row.Subagent = identity.ID
		if parent := identities[identity.ParentSession]; parent.ID == identity.ParentSession && parent.Role == "subagent" {
			row.ParentAgent = parent.ID
		}
	}
	return row
}

func codexActor(call sessions.Call) string {
	if call.Agent == "codex" {
		return call.ThreadID
	}
	return ""
}

func codexResponseMatches(records []Record, calls []sessions.Call, duplicateCalls map[int]bool) map[int]int {
	recordIDs := map[string][]int{}
	for i, record := range records {
		if record.ResponseID != "" {
			recordIDs[record.ResponseID] = append(recordIDs[record.ResponseID], i)
		}
	}
	callIDs := map[string][]int{}
	for i, call := range calls {
		if call.ResponseID != "" && !duplicateCalls[i] {
			callIDs[call.ResponseID] = append(callIDs[call.ResponseID], i)
		}
	}
	pairs := map[int]int{}
	for id, recordRefs := range recordIDs {
		callRefs := callIDs[id]
		if len(recordRefs) != 1 || len(callRefs) != 1 {
			continue
		}
		record, call := recordRefs[0], callRefs[0]
		if records[record].Agent == "codex" && !records[record].IsRejected() && records[record].Source != SourceReported && calls[call].Agent == "codex" {
			pairs[record] = call
		}
	}
	return pairs
}

func sameCodexCall(a, b Row) bool {
	return isCodexRow(a) && isCodexRow(b) && a.ResponseID == b.ResponseID && a.Time.Equal(b.Time) &&
		a.Model == b.Model && a.Requested == b.Requested && a.Served == b.Served && a.Effort == b.Effort &&
		a.Input == b.Input && a.Output == b.Output && a.CacheRead == b.CacheRead && a.CacheWrite == b.CacheWrite &&
		a.CacheWrite1h == b.CacheWrite1h && a.Reasoning == b.Reasoning && a.Millis == b.Millis && a.TTFT == b.TTFT &&
		a.Error == b.Error && a.ErrType == b.ErrType && a.RequestID == b.RequestID
}

// codexCompactionOf is what a native Codex call says about compaction: only
// a call read from a token_usage_record (it has a response_id) can say, as
// Codex writes that record and a compaction's compaction_response_id together
// (openai/codex#41912). An older rollout's call, told from token_count alone,
// never carries the marker, so it is unknown, not "no compaction".
func codexCompactionOf(call sessions.Call) *bool {
	if call.Agent != "codex" || call.ResponseID == "" {
		return nil
	}
	compacted := call.Compaction
	return &compacted
}

func mergeCodexCompactions(values []*bool) *bool {
	allKnown, confirmed := len(values) > 0, false
	for _, value := range values {
		if value == nil {
			allKnown = false
			continue
		}
		confirmed = confirmed || *value
	}
	if confirmed {
		yes := true
		return &yes
	}
	if allKnown {
		no := false
		return &no
	}
	return nil
}

func isCodexRow(row Row) bool {
	if row.Client != "" {
		return row.Client == "codex"
	}
	return row.Agent == "codex"
}

func duplicateCodexCalls(calls []sessions.Call) (map[int]bool, map[int]string, map[int]*bool) {
	local := map[string][]int{}
	for i, call := range calls {
		if call.Agent == "codex" && call.ResponseID != "" {
			local[call.ResponseID] = append(local[call.ResponseID], i)
		}
	}
	skip, actors, compactions := map[int]bool{}, map[int]string{}, map[int]*bool{}
	for index, call := range calls {
		if call.Agent == "codex" {
			compactions[index] = codexCompactionOf(call)
		}
	}
	for _, indexes := range local {
		if len(indexes) < 2 {
			continue
		}
		rows := make([]Row, len(indexes))
		actor := calls[indexes[0]].ThreadID
		actorConflict := actor == ""
		identical := true
		for i, index := range indexes {
			rows[i] = Row{Record: logRecord(calls[index])}
			if calls[index].ThreadID != actor {
				actorConflict = true
			}
			if i > 0 && !sameCodexCall(rows[0], rows[i]) {
				identical = false
			}
		}
		if !identical {
			for _, index := range indexes {
				actors[index] = ""
			}
			continue
		}
		for _, index := range indexes[1:] {
			skip[index] = true
		}
		merged := make([]*bool, len(indexes))
		for i, index := range indexes {
			merged[i] = codexCompactionOf(calls[index])
		}
		compactions[indexes[0]] = mergeCodexCompactions(merged)
		if actorConflict {
			actors[indexes[0]] = ""
		} else {
			actors[indexes[0]] = actor
		}
	}
	return skip, actors, compactions
}

func cloneCodexChunks(chunks []*rowChunk) []*rowChunk {
	out := make([]*rowChunk, len(chunks))
	for i, chunk := range chunks {
		copy := *chunk
		copy.codexActors = chunk.codexActors.clone()
		copy.codexCompactions = nil
		copy.codexIdentities = nil
		out[i] = &copy
	}
	return out
}

// rowResponseID is rowText's slot of Record.ResponseID.
const rowResponseID = 25

// responsePick is what codexChunkResponsePairs knows of one response id
type responsePick struct {
	gateways, locals   int
	gateway, local     rowRef
	gatewayOK, localOK bool
	compaction         *bool
}

// pickScratch is what one codexChunkResponsePairs call works in, kept for
// the next as matchScratch is: made afresh on every poll, its maps and
// slab were ~775 MB of what 80 s of polls allocated while calls came in
// (#2278).
type pickScratch struct {
	made   int // the gateway's rows of the largest call it served
	picks  map[string]int32
	slab   []responsePick
	picked map[uint64]bool
}

var keptPicks atomic.Pointer[pickScratch]

// release empties s, so it holds no chunk of this call, and keeps it
func (s *pickScratch) release() {
	clear(s.picks)
	clear(s.picked)
	clear(s.slab)
	s.slab = s.slab[:0]
	keptPicks.Store(s)
	lingerScratch()
}

// codexChunkResponsePairs are the Codex calls the gateway and a session file
// both name by one response id, gateway to local, and each local call's
// compaction. Only the ids the gateway names are kept of the locals': a map
// of every Codex call's id held each decoded chunk's strings.
func codexChunkResponsePairs(gateways, locals []*rowChunk, duplicateLocal refSet) (map[rowRef]rowRef, map[rowRef]*bool) {
	// the picks are one slice the map points into: a pick each was ~300 MB
	// of what two minutes of polls allocated while an agent worked (#2278).
	// The slab holds the gateway's ids, and a gateway call is
	// judged by the few strings that decide it, not made a Row: grown
	// from nothing, and a Row each, they were ~2.4 GB of what 80 s of
	// polls allocated while calls came in, on a year's history.
	var rows chunkRows
	defer rows.release()
	withID := 0
	for _, chunk := range gateways {
		open := rows.of(chunk)
		for i := range open.Rows {
			if open.text(&open.Rows[i], rowResponseID) != 0 {
				withID++
			}
		}
	}
	need := rowsOfAll(gateways)
	scratch := keptPicks.Swap(nil)
	if scratch == nil || !scratchFits(scratch.made, need) {
		scratch = &pickScratch{}
	}
	scratch.made = max(scratch.made, need)
	defer scratch.release()
	scratch.picks = mapFor(scratch.picks, withID)
	picks := scratch.picks
	slab := slices.Grow(scratch.slab, withID)
	defer func() { scratch.slab = slab }()
	for _, chunk := range gateways {
		open := rows.of(chunk)
		for i := range open.Rows {
			p := &open.Rows[i]
			id := open.Strings[open.text(p, rowResponseID)]
			if id == "" {
				continue
			}
			k, ok := picks[id]
			if !ok {
				k = int32(len(slab))
				picks[id] = k
				slab = append(slab, responsePick{})
			}
			pk := &slab[k]
			pk.gateways++
			pk.gateway = rowRef{chunk, i}
			pk.gatewayOK = open.codexGatewayCall(p)
		}
	}
	lent := lentRows() // picks are looked up by a local's id, not keyed
	defer lent.release()
	// a local chunk is read only when one of its ids may be a pick's: on a
	// page's poll every session file of the period was decoded again for
	// the few the gateway named
	scratch.picked = mapFor(scratch.picked, len(picks))
	picked := scratch.picked
	for id := range picks {
		picked[foldHash(id)] = true
	}
	known := chunkIDs(locals, &lent, false)
	for _, chunk := range locals {
		if !slices.ContainsFunc(known[chunk].calls, func(call uint64) bool { return picked[call>>foldShift] }) {
			continue
		}
		if pairsOpening != nil {
			pairsOpening(chunk)
		}
		open := lent.of(chunk)
		for i := range open.Rows {
			ref := rowRef{chunk, i}
			if duplicateLocal.has(ref) {
				continue
			}
			k, ok := picks[open.Strings[open.text(&open.Rows[i], rowResponseID)]]
			if !ok {
				continue
			}
			p := &slab[k]
			row := open.row(i)
			p.locals++
			p.local = ref
			p.localOK = isCodexRow(row) && row.Source == "log"
			p.compaction = row.Compaction
		}
	}
	pairs, compactions := map[rowRef]rowRef{}, map[rowRef]*bool{}
	for i := range slab {
		if p := &slab[i]; p.gateways == 1 && p.locals == 1 && p.gatewayOK && p.localOK {
			pairs[p.gateway] = p.local
			compactions[p.local] = p.compaction
		}
	}
	return pairs, compactions
}

// codexGatewayCall is whether p, a row of c, is a Codex call the gateway
// served and logged itself: isCodexRow, not IsRejected and not
// SourceReported of c.row(p's index), read from p's strings alone.
func (c *rowChunk) codexGatewayCall(p *packedRow) bool {
	str := func(slot int) string { return c.Strings[c.text(p, slot)] }
	if client := str(rowClient); client != "" {
		if client != "codex" {
			return false
		}
	} else if str(0) != "codex" {
		return false
	}
	if p.Flags&4 != 0 || str(1) == "" && p.Status >= 400 {
		return false
	}
	return str(16) != SourceReported
}

// foldIDs are each chunk's calls by a hash of their response id, kept by
// the chunk's serial (chunkIDs). A page polled while one session was live
// decoded every session file of the period again to hash the same ids:
// ~40 MB a poll of today's page, and as much again for Codex's effort.
var foldIDs struct {
	sync.Mutex
	by   map[uint64]*foldedIDs
	turn uint64
}

type foldedIDs struct {
	// each call with a response id: the id's hash shifted left by
	// foldShift, foldCodexLog when it is a Codex log call, and its index
	// in the low foldIndexBits. Calls whose hash is the same are told
	// apart by their ids after, so a shortened hash only reads a few more
	// rows.
	calls []uint64
	turn  uint64 // the fold that used it last
}

const (
	foldIndexBits = 24
	foldCodexLog  = 1 << foldIndexBits
	foldShift     = foldIndexBits + 1
)

var foldSeed = maphash.MakeSeed()

// foldHash is the part of an id's hash a foldedIDs call keeps.
func foldHash(id string) uint64 { return maphash.String(foldSeed, id) << foldShift >> foldShift }

// pairsOpening, in tests, is told each local chunk
// codexChunkResponsePairs reads rows of.
var pairsOpening func(*rowChunk)

// foldKept is how many folds a chunk's ids outlive the last that read
// them: the page's and Codex effort's are of different chunks.
const foldKept = 4

// foldHashing, in tests, is told each chunk chunkIDs reads ids from.
var foldHashing func(*rowChunk)

// chunkIDs are locals' calls as foldIDs keeps them, read from a chunk
// only when none are kept for its serial. next starts a fold: one that
// is not is for the same chunks as the fold before it.
func chunkIDs(locals []*rowChunk, rows *chunkRows, next bool) map[*rowChunk]*foldedIDs {
	foldIDs.Lock()
	if next {
		foldIDs.turn++
	}
	turn := foldIDs.turn
	known := make(map[*rowChunk]*foldedIDs, len(locals))
	for _, chunk := range locals {
		if f := foldIDs.by[chunk.serial]; chunk.serial != 0 && f != nil {
			f.turn = turn
			known[chunk] = f
		}
	}
	foldIDs.Unlock()
	var made map[uint64]*foldedIDs
	for _, chunk := range locals {
		if known[chunk] != nil {
			continue
		}
		if foldHashing != nil {
			foldHashing(chunk)
		}
		f := &foldedIDs{turn: turn}
		open := rows.of(chunk)
		for i := range open.Rows {
			id := open.Strings[open.text(&open.Rows[i], rowResponseID)]
			if id == "" {
				continue
			}
			call := foldHash(id)<<foldShift | uint64(i)
			if row := open.row(i); isCodexRow(row) && row.Source == "log" {
				call |= foldCodexLog
			}
			f.calls = append(f.calls, call)
		}
		known[chunk] = f
		if chunk.serial != 0 && len(open.Rows) < 1<<foldIndexBits {
			if made == nil {
				made = map[uint64]*foldedIDs{}
			}
			f.calls = slices.Clip(f.calls)
			made[chunk.serial] = f
		}
	}
	foldIDs.Lock()
	if foldIDs.by == nil {
		foldIDs.by = map[uint64]*foldedIDs{}
	}
	for serial, f := range made {
		foldIDs.by[serial] = f
	}
	if next {
		for serial, f := range foldIDs.by {
			if f.turn+foldKept < foldIDs.turn {
				delete(foldIDs.by, serial)
			}
		}
	}
	foldIDs.Unlock()
	return known
}

func foldCodexRows(locals []*rowChunk) refSet {
	// The calls are found by a hash of their response id, not the id: a map
	// keyed by every Codex call's id held each decoded chunk's strings. Calls
	// whose hash is the same are told apart by their ids after.
	first := map[uint64]rowRef{}
	again := map[uint64][]rowRef{}
	rows := lentRows() // an id is kept as its hash
	defer rows.release()
	known := chunkIDs(locals, &rows, true)
	for _, chunk := range locals {
		for _, call := range known[chunk].calls {
			if call&foldCodexLog == 0 {
				continue
			}
			ref, h := rowRef{chunk, int(call & (1<<foldIndexBits - 1))}, call>>foldShift
			if f, ok := first[h]; !ok {
				first[h] = ref
			} else {
				if again[h] == nil {
					again[h] = []rowRef{f}
				}
				again[h] = append(again[h], ref)
			}
		}
	}
	var refs []rowRef
	for _, hashed := range again {
		refs = append(refs, hashed...)
	}
	read := rowsOf(locals, refs)
	skip := refSet{}
	for _, hashed := range again {
		byID := map[string][]rowRef{}
		var ids []string
		for _, ref := range hashed {
			id := read[ref].ResponseID
			if byID[id] == nil {
				ids = append(ids, id)
			}
			byID[id] = append(byID[id], ref)
		}
		for _, id := range ids {
			foldCodexCall(byID[id], read, skip)
		}
	}
	return skip
}

// foldCodexCall keeps the first of refs, the calls a session file names by
// one response id, and skips the rest when they are the same call.
func foldCodexCall(refs []rowRef, read map[rowRef]Row, skip refSet) {
	if len(refs) < 2 {
		return
	}
	actors := make([]string, len(refs))
	rows := make([]Row, len(refs))
	identical := true
	actor := ""
	actorConflict := false
	for i, ref := range refs {
		rows[i] = read[ref]
		actors[i] = ref.Chunk.codexActors.at(ref.Index)
		if i == 0 {
			actor = actors[i]
		} else {
			if actors[i] != actor {
				actorConflict = true
			}
			if !sameCodexCall(rows[0], rows[i]) {
				identical = false
			}
		}
	}
	if !identical {
		for _, ref := range refs {
			setCodexActor(ref.Chunk, ref.Index, "")
		}
		return
	}
	if actorConflict {
		setCodexActor(refs[0].Chunk, refs[0].Index, "")
	}
	for _, ref := range refs[1:] {
		skip.add(ref)
	}
	compactions := make([]*bool, len(refs))
	for i := range refs {
		compactions[i] = rows[i].Compaction
	}
	refs[0].Chunk.setCodexCompaction(refs[0].Index, mergeCodexCompactions(compactions))
}

func setCodexActor(chunk *rowChunk, index int, actor string) {
	chunk.codexActors.set(rowsIn(chunk), index, actor)
}

// codexEffort keeps CodexModelEffort's last answer for each period: the
// Sessions page asks for all history every 15s, and the ledger under it is
// read again only when a call, a price or an identity changed.
//
// A gateway in use writes a call every few seconds, so the answer is often
// out of date when it is asked for. Only one ledger is built at a time, and
// while one is built for a period that has an answer already, that answer is
// what is said: each request used to build its own ledger of all history
// (#2278, with 68k session files and 75k calls a week), and the Sessions
// page's requests arriving together grew the app to 85 GB.
var codexEffort = struct {
	sync.Mutex
	periods map[Period]*effortEntry
	built   int64 // a clock for which period was asked for last
}{periods: map[Period]*effortEntry{}}

// codexEffortBuilding holds one ledger build at a time.
var codexEffortBuilding sync.Mutex

// effortPeriodsKept is how many periods' answers are kept: a picked range is
// a period of its own, so they are not kept without end.
const effortPeriodsKept = 8

type effortEntry struct {
	key      string
	root     string // the logs it was read from: another home's is no answer
	value    map[string][]sessions.ModelEffort
	have     bool
	building chan struct{} // closed when the build under way ends
	used     int64
	at       time.Time // when the kept answer's build began
}

// effortFresh is how long an answer stands before a change is built into
// it. Codex and Claude Code write to their session files all the time, so
// nearly every 15 s Sessions poll found the history changed and built the
// ledger of all of it again: over a real history that was ~2.5 GB
// allocated and ~1 s of CPU every 3.5 minutes for this answer alone. A
// thread's consumption a minute behind is still the thread's; the page's
// own rows and totals are read as they change.
var effortFresh = time.Minute

// effortBehind: once an answer is past effortFresh and the history
// changed, the ask that finds it is told it while the next is built behind
// it. The build is the ledger of all history, and made inside the
// Sessions page's request it held that poll 0.8 to 1.3 s every minute a
// Codex or Claude Code was working; the next poll has the change. Only a
// period's first answer, or one from another home, is waited for.
var effortBehind = true

// effortBuilt, in tests, is told each time CodexModelEffort reads the ledger.
var effortBuilt func()

// CodexModelEffort reports each Codex thread's own priced ledger consumption.
// The answer is the kept one, shared by every ask until the next build
// replaces it: callers read it and copy a thread's slice before keeping it,
// never write it. A copy of every thread's for each ask was ~230 MB every
// 80 s of a Sessions page open over a large history (#2278).
func CodexModelEffort(p Period) map[string][]sessions.ModelEffort {
	if LogCalls != nil { // synthetic logs carry no state to key on
		return codexModelEffort(codexEffortRows(p, Clock(), LogCalls))
	}
	codexEffort.Lock()
	if e := codexEffort.periods[p]; e != nil && e.have && e.root == requestRoot() && time.Since(e.at) < effortFresh {
		codexEffort.built++
		e.used = codexEffort.built
		value := e.value
		codexEffort.Unlock()
		return value // read before the state is: that is most of an unchanged ask's cost
	}
	codexEffort.Unlock()
	now := Clock()
	st := readRequestState(now)
	codexEffort.Lock()
	e := codexEffort.periods[p]
	if e == nil {
		e = &effortEntry{}
		codexEffort.periods[p] = e
		forgetEffortPeriods(p)
	}
	codexEffort.built++
	e.used = codexEffort.built
	if e.have && (e.key == st.key || e.building != nil && e.root == st.root) {
		value := e.value // current, or the last one while the next is built
		codexEffort.Unlock()
		return value
	}
	if wait := e.building; wait != nil { // the first answer is on its way
		codexEffort.Unlock()
		<-wait
		codexEffort.Lock()
		value := e.value
		codexEffort.Unlock()
		return value
	}
	done := make(chan struct{})
	e.building = done
	if effortBehind && e.have && e.root == st.root {
		value := e.value
		codexEffort.Unlock()
		go buildEffort(e, st, p, now, done)
		return value
	}
	codexEffort.Unlock()
	return buildEffort(e, st, p, now, done)
}

// buildEffort builds p's answer under st into e, and closes done when it
// is kept.
func buildEffort(e *effortEntry, st requestState, p Period, now time.Time, done chan struct{}) (value map[string][]sessions.ModelEffort) {
	began := time.Now()
	defer func() {
		codexEffort.Lock()
		e.building = nil
		if value != nil {
			// kept under the state read before the build: a call written
			// while it ran changes the state, and the next ask builds again
			e.key, e.root, e.value, e.have, e.at = st.key, st.root, value, true, began
		}
		codexEffort.Unlock()
		close(done)
	}()
	codexEffortBuilding.Lock()
	defer codexEffortBuilding.Unlock()
	if effortBuilt != nil {
		effortBuilt()
	}
	return codexEffortOfIndex(st, p, now)
}

// codexEffortRows are the rows of ledgerOfAt(p, Filter{}, now, reader) that
// carry a Codex thread, in the same order, without the rest of the ledger.
// A gateway call is told its thread only by the session file's call it is
// paired with, and a session file's call is matched (and so not a row of
// its own) only by a gateway call with its request id, its session or its
// response id: only those gateway calls are kept from the log, each read
// and let go as the log is visited. Over ~720k gateway calls a build held
// every one of them three times over (a record, a packed row and a priced
// row) at the start's peak (#2776).
func codexEffortRows(p Period, now time.Time, reader func(time.Time) []sessions.Call) []Row {
	since := p.Since(now)
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	logs := reader(since)
	if len(logs) == 0 {
		return nil
	}
	recs := matchingRecords(gatewaySince, logs)
	matched, pairs, actors, compactions := gatewayMatchesDetails(recs, logs)
	// only a gateway call paired with a session file's call has a thread
	paired := make([]Record, 0, len(pairs))
	byRecord := make(map[int]int, len(pairs))
	for i, r := range recs {
		if call, ok := pairs[i]; ok {
			byRecord[len(paired)] = call
			paired = append(paired, r)
		}
	}
	rows, _, _, _ := ledgerMatched(since, p.Until(now), Filter{}, paired, logs, nil, matched, byRecord, actors, compactions)
	return slices.DeleteFunc(rows, func(r Row) bool { return r.codexActor == "" })
}

// matchingRecords are the gateway's calls from since on that can be matched
// to one of logs, in the log's order: the ones with a response id, a request
// id or a session one of them has (an agy call by its agent, agy's or the
// app's whose gateway it follows: agy names no session). Any other gateway
// call leaves gatewayMatchesDetails' answer for logs as it is.
func matchingRecords(since time.Time, logs []sessions.Call) []Record {
	responses, requests, sessionIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	agy := false
	for _, c := range logs {
		responses[c.ResponseID] = true
		requests[c.RequestID] = true
		sessionIDs[c.Session] = true
		agy = agy || agyLocal(c.Agent)
	}
	delete(responses, "")
	delete(requests, "")
	delete(sessionIDs, "")
	var out []Record
	Visit(since, func(r Record) {
		if responses[r.ResponseID] || requests[r.RequestID] || sessionIDs[r.NativeSession] || sessionIDs[r.Session] ||
			agy && agyGateway(ReportedAs(r.Agent)) {
			out = append(out, r)
		}
	})
	return out
}

func codexCalls(since time.Time) []sessions.Call { return sessions.AgentCalls(since, "codex") }

// forgetEffortPeriods drops the answers asked for longest ago, keeping p's
// entry; codexEffort is held.
func forgetEffortPeriods(p Period) {
	for len(codexEffort.periods) > effortPeriodsKept {
		var oldest Period
		var at int64 = -1
		for q, e := range codexEffort.periods {
			if q != p && e.building == nil && (at < 0 || e.used < at) {
				oldest, at = q, e.used
			}
		}
		if at < 0 {
			return
		}
		delete(codexEffort.periods, oldest)
	}
}

func codexModelEffort(rows []Row) map[string][]sessions.ModelEffort {
	sums := effortSums{}
	for i := range rows {
		sums.add(&rows[i])
	}
	return sums.values()
}

type modelEffortKey struct{ model, effort string }

type effortTotal struct {
	value  sessions.ModelEffort
	priced bool
}

// effortSums adds rows up by Codex thread, model and effort as they are
// visited, so that no build holds the rows themselves.
type effortSums map[string]map[modelEffortKey]*effortTotal

func (groups effortSums) add(row *Row) {
	if row.codexActor == "" || row.IsRejected() ||
		row.Input+row.Output+row.CacheRead+row.CacheWrite == 0 {
		return
	}
	byModel := groups[row.codexActor]
	if byModel == nil {
		byModel = map[modelEffortKey]*effortTotal{}
		groups[row.codexActor] = byModel
	}
	key := modelEffortKey{row.Model, row.Effort}
	entry := byModel[key]
	if entry == nil {
		// the row's strings can be lent (visitActors): what is kept is a copy
		key = modelEffortKey{strings.Clone(row.Model), strings.Clone(row.Effort)}
		entry = &effortTotal{value: sessions.ModelEffort{Model: key.model, Effort: key.effort}, priced: true}
		byModel[key] = entry
	}
	entry.priced = entry.priced && row.Priced
	entry.value.Input += row.Input
	entry.value.Output += row.Output
	entry.value.CacheRead += row.CacheRead
	entry.value.CacheWrite += row.CacheWrite
	entry.value.CacheWrite1h += row.CacheWrite1h
	entry.value.Reasoning += row.Reasoning
	entry.value.Cost += row.Cost
}

// addValue adds a thread's kept total for one model and effort (effortSplit).
func (groups effortSums) addValue(actor string, v sessions.ModelEffort) {
	byModel := groups[actor]
	if byModel == nil {
		byModel = map[modelEffortKey]*effortTotal{}
		groups[actor] = byModel
	}
	key := modelEffortKey{v.Model, v.Effort}
	entry := byModel[key]
	if entry == nil {
		entry = &effortTotal{value: sessions.ModelEffort{Model: v.Model, Effort: v.Effort}, priced: true}
		byModel[key] = entry
	}
	entry.priced = entry.priced && v.Priced
	entry.value.Input += v.Input
	entry.value.Output += v.Output
	entry.value.CacheRead += v.CacheRead
	entry.value.CacheWrite += v.CacheWrite
	entry.value.CacheWrite1h += v.CacheWrite1h
	entry.value.Reasoning += v.Reasoning
	entry.value.Cost += v.Cost
}

func (groups effortSums) values() map[string][]sessions.ModelEffort {
	out := make(map[string][]sessions.ModelEffort, len(groups))
	for actor, byModel := range groups {
		values := make([]sessions.ModelEffort, 0, len(byModel))
		for _, entry := range byModel {
			entry.value.Priced = entry.priced
			values = append(values, entry.value)
		}
		slices.SortFunc(values, func(a, b sessions.ModelEffort) int {
			if a.Model != b.Model {
				return strings.Compare(a.Model, b.Model)
			}
			return strings.Compare(a.Effort, b.Effort)
		})
		out[actor] = values
	}
	return out
}
