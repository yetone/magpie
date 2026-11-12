package sessions

import (
	"bytes"
	"encoding/gob"
	"os"
	"path/filepath"
	"strings"
)

// A Claude Code session's revision window (claudeUsageState) lived in memory
// alone, so once magpie started again, or let a ninth live session's window
// go, every Claude file that had grown was read again from its first line: a
// live 2 GB session took seconds of the first Requests page after every
// update (#3493). The window is bounded (claudeRevisionEntries), so it is
// kept beside the file's shard, with where the read that made it stopped.

// callRevisions is a shard's Claude window, for the read that ended at Off.
type callRevisions struct {
	Off         int64
	ContentHash string
	CompactMs   int64
	Window      []claudeWindowEntry
}

func callRevisionsPath(path string) string {
	return strings.TrimSuffix(callCachePath(path), ".gob") + ".rev"
}

// writeCallRevisions keeps st's window beside its shard, or takes away a
// window kept before when st has none.
func writeCallRevisions(st *callFile) {
	path := callRevisionsPath(st.Path)
	if st.Claude == nil {
		_ = os.Remove(path)
		return
	}
	r := callRevisions{Off: st.Off, ContentHash: st.ContentHash, CompactMs: st.CompactMs, Window: st.Claude.window()}
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
	if old == nil || old.Claude != nil || (old.Agent != "claude" && old.Agent != "claude-desktop") || old.ContentHash == "" {
		return old
	}
	data, err := os.ReadFile(callRevisionsPath(old.Path))
	if err != nil {
		return old
	}
	var r callRevisions
	if gob.NewDecoder(bytes.NewReader(data)).Decode(&r) != nil || r.Off != old.Off || r.ContentHash != old.ContentHash {
		return old
	}
	c := *old
	c.Claude, c.CompactMs = claudeWindowOf(r.Window), r.CompactMs
	return &c
}
