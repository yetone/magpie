package sessions

import "time"

// A request page releases expanded Calls after packing them. Retain only a
// bounded set of transient parser continuations, so the next append can reuse
// the disk shard's rows without rescanning an active source from its start.
// These indexes are never written to the shard or the summary JSON.
var callContinuations = map[string]callContinuation{}
var callContinuationOrder []string

type callContinuation struct {
	state callFile
	count int
}

// keepCallContinuation is called with callsMu held. Published continuations
// are immutable; prepareCalls clones them before applying another append.
func keepCallContinuation(st *callFile) {
	defer trimCallRevisions(time.Now())
	if st.Agent != "codex" && st.Agent != "claude" && st.Agent != "claude-desktop" {
		return
	}
	old, exists := callContinuations[st.Path]
	if exists && old.state.Off == st.Off && old.state.ContentHash == st.ContentHash {
		return
	}
	delete(callContinuations, st.Path)
	order := callContinuationOrder[:0]
	for _, path := range callContinuationOrder {
		if path != st.Path {
			order = append(order, path)
		}
	}
	callContinuationOrder = order
	if !recentRevision(st.Mod, st.revisionWeight(), time.Now()) {
		return
	}
	n := len(st.Calls)
	if n > maxKeptCalls {
		return
	}
	for _, entry := range callContinuations {
		n += entry.count
	}
	for len(callContinuationOrder) > 0 && (n > maxKeptCalls || len(callContinuationOrder) >= maxKeptFiles) {
		path := callContinuationOrder[0]
		n -= callContinuations[path].count
		delete(callContinuations, path)
		callContinuationOrder = callContinuationOrder[1:]
	}
	state := *st
	state.Calls, state.Msgs, state.Began, state.Strs = nil, nil, nil, nil
	state.dirty = nil
	callContinuations[st.Path] = callContinuation{state, len(st.Calls)}
	callContinuationOrder = append(callContinuationOrder, st.Path)
}

func restoreCallContinuation(st *callFile) *callFile {
	if st == nil {
		return nil
	}
	callsMu.Lock()
	entry, ok := callContinuations[st.Path]
	callsMu.Unlock()
	previous := entry.state
	if !ok || previous.Off != st.Off || previous.Size != st.Size || previous.Mod != st.Mod || previous.ContentHash != st.ContentHash {
		return st
	}
	previous.Calls, previous.Msgs, previous.Began, previous.Strs = st.Calls, st.Msgs, st.Began, st.Strs
	return &previous
}
