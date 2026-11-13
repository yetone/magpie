package sessions

import (
	"bytes"
	"encoding/gob"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A Claude Code session's revision window (claudeUsageState) lived in memory
// alone, so once magpie started again, or let a ninth live session's window
// go, every Claude file that had grown was read again from its first line: a
// live 2 GB session took seconds of the first Requests page after every
// update (#3493). The window is bounded (claudeRevisionEntries), so it is
// kept beside the file's shard, with where the read that made it stopped.

// A Codex rollout's (cxRun) is kept the same way while the rollout is
// being written (codex_window.go).

// callRevisions is a shard's Claude window, or its Codex rollout's read
// state, for the read that ended at Off.
type callRevisions struct {
	Off         int64
	ContentHash string
	CompactMs   int64
	Window      []claudeWindowEntry
	CX          *cxRun
	CXVersion   int // codexWindowVersion
}

func callRevisionsPath(path string) string {
	return strings.TrimSuffix(callCachePath(path), ".gob") + ".rev"
}

// writeCallRevisions keeps st's window beside its shard, or takes away a
// window kept before when st has none.
func writeCallRevisions(st *callFile) {
	path := callRevisionsPath(st.Path)
	r := callRevisions{Off: st.Off, ContentHash: st.ContentHash.String(), CompactMs: st.CompactMs}
	switch {
	case st.Claude != nil:
		r.Window = st.Claude.window()
	case codexCallsKept(st):
		r.CX, r.CXVersion = st.CX, codexWindowVersion
	default:
		_ = os.Remove(path)
		return
	}
	var data bytes.Buffer
	if gob.NewEncoder(&data).Encode(r) != nil {
		return
	}
	out, err := os.CreateTemp(filepath.Dir(path), "calls-*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(out.Name())
	_, err = out.Write(data.Bytes())
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		_ = os.Rename(out.Name(), path)
	}
}

// withCallRevisions is old with the window kept for it, when old is a
// Claude file's read without one and the window kept is of that same read.
func withCallRevisions(old *callFile) *callFile {
	if old == nil || old.ContentHash == (digest{}) {
		return old
	}
	codex := old.Agent == "codex" && (old.CX == nil || old.CX.Usage == nil)
	if !codex && (old.Claude != nil || (old.Agent != "claude" && old.Agent != "claude-desktop")) {
		return old
	}
	data, err := os.ReadFile(callRevisionsPath(old.Path))
	if err != nil {
		return old
	}
	var r callRevisions
	if gob.NewDecoder(bytes.NewReader(data)).Decode(&r) != nil || r.Off != old.Off || parseDigest(r.ContentHash) != old.ContentHash {
		return old
	}
	c := *old
	if codex {
		if r.CX == nil || r.CX.Usage == nil || r.CXVersion != codexWindowVersion {
			return old
		}
		c.CX = r.CX
		return &c
	}
	c.Claude, c.CompactMs = claudeWindowOf(r.Window), r.CompactMs
	return &c
}

// codexCallsKept says whether st is a rollout's calls whose read state is
// kept on disk: one being written, its window no larger than the cache
// keeps for a working session (revisionActiveEntries).
func codexCallsKept(st *callFile) bool {
	return st.Agent == "codex" && st.CX != nil && st.CX.Usage != nil && workingRevision(st.Mod, time.Now()) && st.revisionWeight() <= revisionActiveEntries
}
