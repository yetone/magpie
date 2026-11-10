package sessions

import (
	"path/filepath"
	"testing"
	"unsafe"
)

// A live file's calls read back from its shard are read on from as they
// are: no other reader has them, and a long session's were copied whole
// on every change of it. Calls a reader shares (the cache's) are copied.
func TestCallsFromTheShardAreNotCopied(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			d := setupCalls(t)
			var path, extra string
			if agent == "claude" {
				path = filepath.Join(d.claude, "projects", "fixture", "session.jsonl")
				writeLines(t, path, claudeMsg("one", "claude-opus-5-5", 10, 2, 30, 4, 1))
				extra = claudeMsg("two", "claude-opus-5-5", 10, 2, 30, 4, 2)
			} else {
				path = filepath.Join(d.codex, "sessions", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
				writeLines(t, path, append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), tokenCountLine(1, cxUse(100, 40, 0, 10, 2), cxUse(100, 40, 0, 10, 2)))...)
				extra = tokenCountLine(2, cxUse(200, 80, 0, 20, 4), cxUse(100, 40, 0, 10, 2))
			}
			source := func() CallSource {
				t.Helper()
				for _, s := range CallSources() {
					if s.Path == path {
						return s
					}
				}
				t.Fatal("source missing")
				return CallSource{}
			}
			if cs := ReadCallSource(source()); len(cs) != 1 {
				t.Fatal(cs)
			}
			appendText(t, path, extra+"\n")
			s := source()
			f := file{path: s.Path, key: s.Key, agent: s.Agent, size: s.Size, mod: modOf(s.Modified)}
			kept := loadKept(f)
			if kept == nil || len(kept.Calls) != 1 {
				t.Fatalf("the shard holds %v", kept)
			}
			if st := prepareCalls(f, withCallRevisions(kept), true); unsafe.SliceData(st.Calls) != unsafe.SliceData(kept.Calls) {
				t.Fatal("calls read from the shard were copied to be read on from")
			}
			if st := prepareCalls(f, withCallRevisions(kept), false); unsafe.SliceData(st.Calls) == unsafe.SliceData(kept.Calls) {
				t.Fatal("calls another reader shares were read on from in place")
			}
			if cs := ReadCallSource(s); len(cs) != 2 {
				t.Fatal(cs)
			}
		})
	}
}
