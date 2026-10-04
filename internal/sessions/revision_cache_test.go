package sessions

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettledCodexRevisionsReleasedAndRebuilt(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	path := codexUsageLog(t, append(cxTurnLines(0, "t1", "m", "high"), cxRecordLine(1, "a", u, u), tokenCountLine(2, u, u)))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	List(0)
	if cache[path] == nil || cache[path].Codex != nil {
		t.Fatal("settled summary retained its response index")
	}
	if cs := Calls(time.Time{}); len(cs) != 1 {
		t.Fatal(cs)
	}
	if callCache[path].CX != nil {
		t.Fatal("settled calls retained their response index")
	}
	for _, source := range CallSources() {
		ReadCallSource(source)
	}
	if callCache[path] != nil {
		t.Fatal("source reader retained expanded rows after index eviction")
	}
	if len(callContinuations) != 0 {
		t.Fatal("settled source retained continuation")
	}
	appendText(t, path, tokenCountLine(3, cxUse(200, 80, 0, 20, 4), u)+"\n")
	if cs := Calls(time.Time{}); len(cs) != 2 {
		t.Fatal("reopened file lost/doubled calls", cs)
	}
	if ss := List(0); len(ss) != 1 || ss[0].Output != 20 {
		t.Fatal("reopened summary", ss)
	}
}

func TestRevisionCachesBoundActiveFilesAndExpire(t *testing.T) {
	d := setupCalls(t)
	u := cxUse(100, 40, 0, 10, 2)
	for i := 0; i < revisionFiles+5; i++ {
		path := filepath.Join(d.codex, "sessions", fmt.Sprintf("rollout-2026-09-20T10-00-00-%08d-1111-7222-8333-444455556666.jsonl", i))
		writeLines(t, path, append(cxTurnLines(0, "t1", "m", "high"), cxRecordLine(1, fmt.Sprint(i), u, u), tokenCountLine(2, u, u))...)
	}
	List(0)
	Calls(time.Time{})
	n := 0
	for _, s := range cache {
		if s.Codex != nil {
			n++
		}
	}
	if n != revisionFiles {
		t.Fatalf("summary retained %d indexes", n)
	}
	for _, source := range CallSources() {
		ReadCallSource(source)
	}
	if len(callContinuations) > revisionFiles {
		t.Fatal(len(callContinuations))
	}
	// Published snapshots may still be in use by a concurrent parser.
	var snapshot *state
	for _, s := range cache {
		if s.Codex != nil {
			snapshot = s
			break
		}
	}
	mu.Lock()
	trimSummaryRevisions(time.Now().Add(revisionIdle + time.Second))
	mu.Unlock()
	if snapshot.Codex == nil {
		t.Fatal("mutated a published snapshot")
	}
	for _, s := range cache {
		if s.Codex != nil {
			t.Fatal("idle summary retained index")
		}
	}
	callsMu.Lock()
	trimCallRevisions(time.Now().Add(revisionIdle + time.Second))
	callsMu.Unlock()
	if len(callContinuations) != 0 {
		t.Fatal("idle continuations retained")
	}
	for _, s := range callCache {
		if s.CX != nil {
			t.Fatal("idle call cache retained index")
		}
	}
}

func TestRevisionEntryBudget(t *testing.T) {
	now := time.Now()
	candidates := []revisionCandidate{{"large", now.UnixNano(), revisionEntries + 1}, {"one", now.UnixNano(), revisionEntries / 2}, {"two", now.UnixNano(), revisionEntries / 2}, {"three", now.UnixNano(), revisionEntries / 2}}
	keep := retainedRevisions(candidates, now)
	if keep["large"] || len(keep) != 2 {
		t.Fatal(keep)
	}
}

func TestCallFrameOmitsRuntimeTypeGraph(t *testing.T) {
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(callFrame{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codexUsageState", "codexContribution", "claudeUsageState", "cxRun", "callFile"} {
		if bytes.Contains(b.Bytes(), []byte(name)) {
			t.Fatalf("runtime type %s persisted", name)
		}
	}
	// Main's empty frame is about 1.3 KB; the previous PR exceeded 2.3 KB.
	if b.Len() > 1300 {
		t.Fatalf("empty frame grew to %d bytes", b.Len())
	}
	t.Logf("empty frame: %d bytes", b.Len())
}

func TestCodexCounterResetAtNonIncreasingTime(t *testing.T) {
	for _, second := range []int{10, 5} {
		t.Run(fmt.Sprint(second), func(t *testing.T) {
			u := cxUse(100, 40, 0, 10, 2)
			small := cxUse(40, 10, 0, 4, 1)
			lines := append(cxTurnLines(0, "t1", "m", "high"), tokenCountLine(10, u, u), tokenCountLine(second, small, small), tokenCountLine(second+1, cxUse(80, 20, 0, 8, 2), small))
			codexUsageLog(t, lines)
			for _, restart := range []bool{false, true} {
				if restart {
					Reset()
				}
				cs := Calls(time.Time{})
				ss := List(0)
				if len(cs) != 3 || len(ss) != 1 || ss[0].Output != 18 {
					t.Fatalf("restart=%v calls=%+v summary=%+v", restart, cs, ss)
				}
			}
		})
	}
}
