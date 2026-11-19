package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real Claude Code subagent lines (agent-<id>.jsonl), their words and ids
// replaced: a hook's progress line, which comes before the task, and the
// user line that gives the task.
func ccHookLine(n int) string {
	return `{"parentUuid":null,"isSidechain":true,"agentId":"b2","type":"progress","data":{"type":"hook_progress","hookEvent":"SessionStart","hookName":"SessionStart:startup","command":"hook"},"timestamp":"2026-09-20T10:05:0` + string(rune('0'+n%10)) + `.000Z","uuid":"h` + string(rune('a'+n%26)) + `","userType":"external"}` + "\n"
}

func ccTaskLine(task string) string {
	return `{"parentUuid":null,"isSidechain":true,"agentId":"b2","type":"user","message":{"role":"user","content":"` + task + `"},"uuid":"u1","timestamp":"2026-09-20T10:06:00.000Z","cwd":"/work/app","sessionId":"11111111-2222-3333-4444-555555555555"}` + "\n"
}

// scansOf counts the scans of the file at path from here on.
func scansOf(t *testing.T, path string) func() int {
	t.Helper()
	var mu sync.Mutex
	n := 0
	scanHook = func(p string, _ int64) {
		if p == path {
			mu.Lock()
			n++
			mu.Unlock()
		}
	}
	t.Cleanup(func() { scanHook = nil })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		c := n
		n = 0
		return c
	}
}

// A subagent's task is read from its file and kept, found or not, until
// the file's size or time changes: grown, written again from its start,
// written again at the same size, or written to while it was read.
func TestSubagentTaskReadOnlyWhenTheFileChanges(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent-b2.jsonl")
	writeFile(t, p, ccHookLine(0)+ccHookLine(1))
	scans := scansOf(t, p)
	ask := func(want string, wantScans int, why string) {
		t.Helper()
		if got := ccTask(p); got != want {
			t.Fatalf("%s: task %q, want %q", why, got, want)
		}
		if n := scans(); n != wantScans {
			t.Fatalf("%s: %d scans, want %d", why, n, wantScans)
		}
	}
	ask("", 1, "first ask")
	ask("", 0, "unchanged, no task yet")
	ask("", 0, "unchanged again")

	// appended: read again, and the task found
	appendTo(t, p, ccTaskLine("Map the parser callers"))
	ask("Map the parser callers", 1, "grown with its task")
	ask("Map the parser callers", 0, "found")
	appendTo(t, p, ccHookLine(2))
	ask("Map the parser callers", 1, "grown after its task")

	// written again from the start, shorter, with another task
	writeFile(t, p, ccTaskLine("Read the docs"))
	ask("Read the docs", 1, "rewritten shorter")

	// written again with the same size and a new time
	writeFile(t, p, ccHookLine(3))
	ask("", 1, "rewritten without a task")
	writeFile(t, p, ccHookLine(4))
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	ask("", 1, "same size, new time")

	// 16 lines without a task: none, and none as the file grows
	var b strings.Builder
	for i := range 17 {
		b.WriteString(ccHookLine(i))
	}
	writeFile(t, p, b.String())
	ask("", 1, "17 lines, none a task")
	appendTo(t, p, ccTaskLine("too late"))
	ask("", 1, "a task after the first 16 lines")

	// written to while it was read: what was read stands for now, and the
	// next ask reads the file again
	writeFile(t, p, ccHookLine(5))
	scansOf(t, p) // count afresh
	once := sync.Once{}
	scanHook = func(q string, _ int64) {
		if q == p {
			once.Do(func() { appendTo(t, p, ccTaskLine("written meanwhile")) })
		}
	}
	ccTask(p)
	scans = scansOf(t, p)
	ask("written meanwhile", 1, "appended while read")

	// gone
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	ask("", 0, "deleted")
}

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// A Claude Code subagent whose meta file names its task is titled from it,
// and its file is not read for one: cmp.Or read every subagent's file for
// a first user line it then threw away, for each one at every start.
func TestSubagentWithMetaTaskIsNotReadForIt(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	subs := filepath.Join(claude, "projects", "-work-app", "11111111-2222-3333-4444-555555555555", "subagents")
	writeFile(t, filepath.Join(subs, "agent-a1.meta.json"), `{"agentType":"Explore","description":"Inventory the parser's callers","toolUseId":"toolu_01BZaaaaaaaaaaaaaaaaaaaa","spawnDepth":1}`)
	sub := filepath.Join(subs, "agent-a1.jsonl")
	List(0) // every file read once
	Saved()
	// a restart: the parses come back from the disk, nothing else kept
	Reset()
	ccTasks.Clear()
	ccMetas.Clear()
	scans := scansOf(t, sub)
	s := find(t, List(0), "claude", "11111111-2222-3333-4444-555555555555")
	if n := scans(); n != 0 {
		t.Fatalf("the list read a subagent's file %d times for a task its meta names", n)
	}
	if len(s.Subagents) == 0 || s.Subagents[0].ID != "a1" || s.Subagents[0].Title != "Inventory the parser's callers" {
		t.Fatalf("subagents %+v", s.Subagents)
	}
}

// A restart does not open each Claude Code subagent's file or meta file
// again: its task and its meta come back from the saved parse, as the
// listing of a few thousand subagents did at every start (shangyuan0818 on X).
func TestSubagentTaskAndMetaComeBackFromTheSavedParse(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	const sid = "11111111-2222-3333-4444-555555555555"
	subs := filepath.Join(claude, "projects", "-work-app", sid, "subagents")
	meta := filepath.Join(subs, "agent-a1.meta.json")
	writeFile(t, meta, `{"agentType":"Explore","toolUseId":"toolu_01BZaaaaaaaaaaaaaaaaaaaa"}`)
	sub := filepath.Join(subs, "agent-a1.jsonl")
	writeFile(t, sub, ccHookLine(0)+ccTaskLine("Map the parser callers"))
	List(0)
	Saved()
	// a restart: the parses come back from the disk, nothing else kept
	Reset()
	ccTasks.Clear()
	ccMetas.Clear()
	// and the meta file is not there to be read again
	if err := os.Remove(meta); err != nil {
		t.Fatal(err)
	}
	scans := scansOf(t, sub)
	s := find(t, List(0), "claude", sid)
	if n := scans(); n != 0 {
		t.Fatalf("the list read a subagent's file %d times for a task the saved parse holds", n)
	}
	if len(s.Subagents) == 0 || s.Subagents[0].Kind != "Explore" || s.Subagents[0].Title != "Map the parser callers" {
		t.Fatalf("subagents %+v", s.Subagents)
	}
}

// A subagent's parse made before its task was kept is read again once, and
// is not after that, whether or not its file names a task.
func TestSubagentParseWithoutItsTaskIsReadOnce(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	const sid = "11111111-2222-3333-4444-555555555555"
	sub := filepath.Join(claude, "projects", "-work-app", sid, "subagents", "agent-a1.jsonl")
	writeFile(t, sub, ccHookLine(0))
	List(0)
	f := file{agent: "claude", path: sub}
	st := parsed(sub)
	if st == nil || !st.more().TaskRead || ccStale(f, st) {
		t.Fatalf("a parse of a subagent with no task is read again at each start: %+v", st)
	}
	old := *st
	old.StateMore = nil // as a magpie before this kept it
	if !ccStale(f, &old) {
		t.Fatal("a subagent's parse made before its task was kept is not read again")
	}
}
