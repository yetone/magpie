package sessions

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// A Codex rollout's summary window (state.Codex) lived in memory alone, as
// its calls' (cxRun) did, so once magpie started again the first line a
// live rollout added had it read again from its first line, for the
// summary and for the calls: a 100 MB rollout took 2.2 s of the first
// Sessions list after it grew on an M5, and over ten seconds on a slower
// Windows PC (shangyuan0818 on X). Like a Claude session's (#3493), the
// window of a rollout being written is kept on disk with where the read
// that made it stopped: the summary's beside sessions.json, written with
// it, and the calls' beside the file's shard (writeCallRevisions).

// codexWindowVersion is the shape of a kept window: raised whenever
// codexUsageState or cxRun changes what its fields mean, so a window an
// older magpie kept is read again from the source rather than taken in.
const codexWindowVersion = 1

// codexWindowHead says which read a kept summary window is of. It is
// written ahead of the window, so readsWhole reads it alone.
type codexWindowHead struct {
	Version     int
	Parser      int // parseVersions["codex"]
	Size, Off   int64
	ContentHash string
}

func codexWindowDir() string { return filepath.Join(filepath.Dir(CachePath()), "codex-windows") }

func codexWindowPath(path string) string {
	return filepath.Join(codexWindowDir(), hashHead(path)+".gob")
}

// codexWindowHeadOf is the head of the window kept for path, and the
// decoder its window is read on from.
func codexWindowHeadOf(path string) (codexWindowHead, *gob.Decoder, func(), bool) {
	var h codexWindowHead
	in, err := os.Open(codexWindowPath(path))
	if err != nil {
		return h, nil, nil, false
	}
	dec := gob.NewDecoder(bufio.NewReader(in))
	if dec.Decode(&h) != nil {
		in.Close()
		return h, nil, nil, false
	}
	return h, dec, func() { in.Close() }, true
}

func (h codexWindowHead) of(s *state) bool {
	return s != nil && h.Version == codexWindowVersion && h.Parser == parseVersions["codex"] && s.ContentHash != (digest{}) && h.Size == s.Size && h.Off == s.Off && parseDigest(h.ContentHash) == s.ContentHash
}

// codexWindowKept says whether a window of old's read is kept for f.
func codexWindowKept(f file, old *state) bool {
	h, _, done, ok := codexWindowHeadOf(f.path)
	if ok {
		done()
	}
	return ok && h.of(old)
}

// codexWindowed is old with the window kept for its read, when old is a
// rollout's summary without one; old as it is otherwise. A window kept
// for another read, or none, leaves old as it is, read again whole.
func codexWindowed(f file, old *state) *state {
	if f.agent != "codex" || old == nil || old.Codex != nil {
		return old
	}
	h, dec, done, ok := codexWindowHeadOf(f.path)
	if !ok {
		return old
	}
	defer done()
	var w codexUsageState
	if !h.of(old) || dec.Decode(&w) != nil {
		return old
	}
	c := *old
	c.Codex = &w
	return &c
}

// codexWindowsWritten are the summaries whose windows are on disk: a
// parse is never changed once in the cache, so one written is not
// written again.
var codexWindowsWritten struct {
	sync.Mutex
	files map[string]*state
}

// writeCodexWindows keeps the windows of files' rollouts on disk, and
// takes away those of rollouts that have none now. The windows in the
// cache are already only those trimSummaryRevisions keeps.
func writeCodexWindows(files map[string]*state) {
	codexWindowsWritten.Lock()
	defer codexWindowsWritten.Unlock()
	dir := codexWindowDir()
	keep := map[string]bool{}
	written := map[string]*state{}
	for path, s := range files {
		if s == nil || s.Agent != "codex" || s.Codex == nil || s.ContentHash == (digest{}) {
			continue
		}
		name := filepath.Base(codexWindowPath(path))
		if codexWindowsWritten.files[path] == s {
			keep[name], written[path] = true, s
			continue
		}
		var data bytes.Buffer
		enc := gob.NewEncoder(&data)
		if enc.Encode(codexWindowHead{Version: codexWindowVersion, Parser: parseVersions["codex"], Size: s.Size, Off: s.Off, ContentHash: s.ContentHash.String()}) != nil || enc.Encode(s.Codex) != nil {
			continue
		}
		if os.MkdirAll(dir, 0o700) != nil {
			return
		}
		out, err := os.CreateTemp(dir, "window-*.tmp")
		if err != nil {
			return
		}
		_, err = out.Write(data.Bytes())
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(out.Name(), codexWindowPath(path))
		}
		if err != nil {
			os.Remove(out.Name())
			continue
		}
		keep[name], written[path] = true, s
	}
	codexWindowsWritten.files = written
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !keep[e.Name()] && (strings.HasSuffix(e.Name(), ".gob") || strings.HasSuffix(e.Name(), ".tmp")) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
