package sessions

import (
	"bytes"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// reuseLine is reply i of a history: its own message, block and two tools.
func reuseLine(i int, at time.Time) []byte {
	content := fmt.Sprintf(`[{"type":"tool_use","id":"t%da","name":"Read","input":{}},{"type":"tool_use","id":"t%db","name":"Skill","input":{"skill":"s%d"}}]`, i, i, i)
	b := claudeUsageLine(at.Add(time.Duration(i)*time.Second), fmt.Sprint(i), fmt.Sprint("r", i), "m", Tokens{Input: 1, Output: i + 1}, content, true)
	return bytes.Replace(b, []byte(`{`), []byte(fmt.Sprintf(`{"uuid":"u%d",`, i)), 1)
}

// A contribution evicted from the window is made again for a later
// message: what it held of its own message is gone, in the Sessions read
// and in the calls' read alike.
func TestClaudeEvictedContributionsLeaveNothing(t *testing.T) {
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	n := 3 * claudeRevisionEntries
	s, st := &state{}, &callFile{Agent: "claude"}
	for i := range n {
		b := reuseLine(i, at)
		claudeLine(s, b, true)
		claudeCallLine(st, b)
	}
	if len(s.Claude.free) == 0 || len(st.Claude.free) == 0 {
		t.Fatal("nothing evicted was kept to use again")
	}
	for j, r := range []*claudeUsageState{s.Claude, st.Claude} {
		// the Sessions read keeps a message's tools, the calls' read its blocks
		tools, blocks := 2*(1-j), j
		for key, branches := range r.Messages {
			id := strings.TrimPrefix(key, "message:")
			for request, m := range branches {
				if request != "r"+id || m.Request != request || m.key != key {
					t.Fatalf("%s: a branch %q holds %q of %q", key, request, m.Request, m.key)
				}
				for tool := range m.Tools {
					if !strings.HasPrefix(tool, "t"+id) || len(tool) != len(id)+2 {
						t.Fatalf("%s holds another message's tool %s", key, tool)
					}
				}
				if len(m.Tools) != tools || len(m.Blocks) != blocks || blocks == 1 && !m.Blocks["uuid:u"+id] {
					t.Fatalf("%s: tools %v blocks %v", key, m.Tools, m.Blocks)
				}
				if m.Usage == nil || m.Usage.Tokens.Output != atoi(id)+1 || len(m.Previous) != 0 {
					t.Fatalf("%s: usage %+v previous %v", key, m.Usage, m.Previous)
				}
			}
		}
	}
	if len(st.Calls) != n {
		t.Fatalf("%d calls of %d replies", len(st.Calls), n)
	}
	for i, c := range st.Calls {
		if c.Output != i+1 || c.RequestID != fmt.Sprint("r", i) {
			t.Fatalf("call %d: %+v", i, c)
		}
	}
	if got := claudeUsageReplies(s); got != n {
		t.Fatalf("%d replies counted of %d", got, n)
	}
	if got := s.Models.of("m").Output; got != n*(n+1)/2 {
		t.Fatalf("output %d, want %d", got, n*(n+1)/2)
	}
	// the last reply read again is the same call, not a new one
	last := reuseLine(n-1, at)
	claudeCallLine(st, last)
	claudeLine(s, last, true)
	if len(st.Calls) != n || claudeUsageReplies(s) != n {
		t.Fatalf("a replay made a call: %d calls, %d replies", len(st.Calls), claudeUsageReplies(s))
	}
}

func atoi(s string) int {
	n := 0
	fmt.Sscan(s, &n)
	return n
}

// TestClaudeHistoryReadAllocations: once the window is full, a reply of
// a history's read makes no contribution, branch list or maps of its own.
func TestClaudeHistoryReadAllocations(t *testing.T) {
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	n := 2 * claudeRevisionEntries
	lines := make([][]byte, 2*n)
	for i := range lines {
		lines[i] = reuseLine(i, at)
	}
	s, st := &state{}, &callFile{Agent: "claude"}
	for _, b := range lines[:n] {
		claudeLine(s, b, true)
		claudeCallLine(st, b)
	}
	runtime.GC()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	before := ms.TotalAlloc
	for _, b := range lines[n:] {
		claudeLine(s, b, true)
		claudeCallLine(st, b)
	}
	runtime.ReadMemStats(&ms)
	per := (ms.TotalAlloc - before) / uint64(n)
	t.Logf("%d bytes a reply", per)
	// ~3.2 KB: the two reads' decoding and the call's strings; a
	// contribution, branch list and maps for each made ~5.3 KB
	if per > 4200 {
		t.Fatalf("a reply's two reads made %d bytes, want at most 4200", per)
	}
}

// TestClaudeMessageLookupAllocations: the later lines of a message find
// it without making its key again, and a new branch of it takes the key
// its first one made.
func TestClaudeMessageLookupAllocations(t *testing.T) {
	r := &claudeUsageState{}
	id := "msg_01ABCDEFGHIJKLMNOPQRSTUVWX"
	m := r.message(id, "req_1")
	r.finish(m)
	if n := testing.AllocsPerRun(100, func() { r.message(id, "req_1") }); n != 0 {
		t.Fatalf("a message's later line made %v allocations, want 0", n)
	}
	if got := r.message(id, "req_1"); got != m || m.key != "message:"+id {
		t.Fatalf("found %p (%q), want %p", got, got.key, m)
	}
	other := r.message(id, "req_2")
	if other == m || other.key != m.key || unsafe.StringData(other.key) != unsafe.StringData(m.key) {
		t.Fatalf("a second branch: %q, its own key made again", other.key)
	}
	if len(r.Messages["message:"+id]) != 2 {
		t.Fatalf("branches %v", r.Messages)
	}
	if q := r.message("", "req_9"); q.key != "request:req_9" || r.Messages["request:req_9"]["req_9"] != q {
		t.Fatalf("a message with only its request: %q", q.key)
	}
}

// TestClaudeCallsListGrownToTheFile: a Claude session's calls list grows
// to the rate the file was read at so far, not by append's quarters: the
// lists made on the way to 20k calls hold ~2.8 times them, ~5.2 times by
// append, and the last ends within an eighth or so of its length. A file
// whose second half holds fewer calls ends at no more than twice them.
func TestClaudeCallsListGrownToTheFile(t *testing.T) {
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	read := func(n int, sparse func(i int) int) (made int, st *callFile) {
		// made: the calls the lists past 256 had room for
		var lines [][]byte
		size := int64(0)
		for i := range n {
			b := append(reuseLine(i, at), bytes.Repeat([]byte(" "), sparse(i))...)
			lines = append(lines, b)
			size += int64(len(b)) + 1
		}
		st = &callFile{Agent: "claude", readSize: size, Msgs: map[string]int{}, dirty: map[int]bool{}}
		end, last := int64(0), -1
		for _, b := range lines {
			st.At, end = end, end+int64(len(b))+1
			st.End = end
			claudeCallLine(st, b)
			if c := cap(st.Calls); c != last && len(st.Calls) > 256 {
				made += c
			}
			last = cap(st.Calls)
		}
		return made, st
	}
	n := 20000
	made, st := read(n, func(int) int { return 0 })
	if len(st.Calls) != n {
		t.Fatalf("%d calls of %d", len(st.Calls), n)
	}
	t.Logf("even: lists of %d calls past 256, cap %d for %d", made, cap(st.Calls), n)
	if made > 7*n/2 || cap(st.Calls) > n*5/4 {
		t.Fatalf("lists of %d calls were made, the last of %d, for %d calls", made, cap(st.Calls), n)
	}
	// the second half's lines ten times as long
	made, st = read(n, func(i int) int { return 9 * 600 * (i * 2 / n) })
	t.Logf("sparse end: lists of %d calls, cap %d for %d", made, cap(st.Calls), n)
	if len(st.Calls) != n || cap(st.Calls) > 2*n {
		t.Fatalf("%d calls in a list of %d", len(st.Calls), cap(st.Calls))
	}
}
