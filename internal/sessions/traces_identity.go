package sessions

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TraceSession names a readable local transcript, independently of whether it
// has produced any observations. Header inspection never emits history/bodies.
type TraceSession struct{ Agent, ID string }

// TraceSessionVisible resolves a first request before the next reader tick.
// Only formats with an authoritative header are eligible; filenames alone
// cannot establish that a usable local transcript exists.
func TraceSessionVisible(agent, id string) bool {
	return new(TraceSessionIndex).Visible(agent, id)
}

// TraceSessionIndex shares one discovery snapshot across all request IDs and
// polling. The caller that wins refresh performs directory I/O; other
// concurrent requests use the snapshot without waiting for that scan.
// Its zero value is usable; no state is shared between exporters or stores.
type TraceSessionIndex struct {
	refresh  sync.Mutex
	snapshot atomic.Pointer[traceIdentitySnapshot]
	headers  map[string]traceHeaderCache
}
type traceIdentitySnapshot struct {
	at       time.Time
	sessions map[TraceSession]bool
}
type traceHeaderCache struct {
	size    int64
	mod     time.Time
	session TraceSession
	ok      bool
}

func (x *TraceSessionIndex) Visible(agent, id string) bool {
	if agent != "codex" && agent != "pi" {
		return false
	}
	snapshot := x.snapshot.Load()
	if snapshot == nil || time.Since(snapshot.at) >= 2*time.Second {
		if x.refresh.TryLock() {
			// A refresh may have completed while this caller was acquiring the lock.
			snapshot = x.snapshot.Load()
			if snapshot == nil || time.Since(snapshot.at) >= 2*time.Second {
				snapshot = x.update(traceRecentFiles(traceLineFiles()))
			}
			x.refresh.Unlock()
		}
	}
	return snapshot != nil && snapshot.sessions[TraceSession{agent, id}]
}

// VisibleFresh bypasses the discovery TTL for the one check before exporting
// a request's first attempt. Join an in-progress scan rather than trusting a
// cached miss; normal request-start checks remain nonblocking and cached.
func (x *TraceSessionIndex) VisibleFresh(agent, id string) bool {
	if (agent != "codex" && agent != "pi") || id == "" {
		return false
	}
	x.refresh.Lock()
	snapshot := x.update(traceRecentFiles(traceLineFiles()))
	x.refresh.Unlock()
	return snapshot.sessions[TraceSession{agent, id}]
}

func (x *TraceSessionIndex) poll(files []file) []TraceSession {
	x.refresh.Lock()
	snapshot := x.update(files)
	x.refresh.Unlock()
	visible := make([]TraceSession, 0, len(snapshot.sessions))
	for session := range snapshot.sessions {
		visible = append(visible, session)
	}
	return visible
}

// Called under refresh. Cache complete headers by path/size/mtime and prune
// entries outside the same bounded window used by the transcript reader.
func (x *TraceSessionIndex) update(files []file) *traceIdentitySnapshot {
	snapshot := &traceIdentitySnapshot{at: time.Now(), sessions: map[TraceSession]bool{}}
	headers := make(map[string]traceHeaderCache, len(files))
	for _, f := range files {
		if f.agent != "codex" && f.agent != "pi" {
			continue
		}
		cached, exists := x.headers[f.path]
		if !exists || cached.size != f.size || !cached.mod.Equal(f.mod) {
			identity, ok := traceSessionHeader(f)
			cached = traceHeaderCache{f.size, f.mod, identity, ok}
		}
		headers[f.path] = cached
		if cached.ok && f.key == cached.session.Agent+":"+cached.session.ID {
			snapshot.sessions[cached.session] = true
		}
	}
	x.headers = headers
	x.snapshot.Store(snapshot)
	return snapshot
}

// Share request-side discovery with this reader's regular polling.
func (r *TraceReader) SetSessionIndex(index *TraceSessionIndex) { r.identities = index }

func traceSessionHeader(f file) (TraceSession, bool) {
	if f.agent != "codex" && f.agent != "pi" || !f.main || strings.HasSuffix(f.path, zstSuffix) {
		return TraceSession{}, false
	}
	h, err := os.Open(f.path)
	if err != nil {
		return TraceSession{}, false
	}
	defer h.Close()
	// Codex includes full base_instructions in session_meta (often > 22 KiB).
	// Keep a generous bound; incomplete/oversized/invalid headers retain gateway
	// tracing. Unmarshalling selects identity only and never exports instructions.
	line, err := bufio.NewReader(io.LimitReader(h, 256<<10)).ReadBytes('\n')
	if err != nil {
		return TraceSession{}, false
	}
	var header struct {
		Type, ID string
		Payload  struct{ ID string }
	}
	if json.Unmarshal(line, &header) != nil {
		return TraceSession{}, false
	}
	id := ""
	if f.agent == "codex" && header.Type == "session_meta" {
		id = header.Payload.ID
	}
	if f.agent == "pi" && header.Type == "session" {
		id = header.ID
	}
	return TraceSession{f.agent, id}, id != ""
}

// VisibleSessions is refreshed on Poll; old transcripts only contribute their
// identity, not completed historical operations.
func (r *TraceReader) VisibleSessions() []TraceSession { return r.visible }

// Main stores and Claude children each have a bounded quota, so a burst of
// children cannot evict Codex/Pi readiness. Preserve recency order for the
// unchanged shared 8 MiB polling budget.
func traceRecentFiles(files []file) []file {
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	recent := make([]file, 0, min(len(files), 2*Limit))
	main, children := 0, 0
	for _, f := range files {
		child := !f.main && (f.agent == "claude" || f.agent == "claude-desktop")
		if child {
			if children >= Limit {
				continue
			}
			children++
		} else {
			if main >= Limit {
				continue
			}
			main++
		}
		recent = append(recent, f)
	}
	return recent
}
