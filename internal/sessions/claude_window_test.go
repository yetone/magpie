package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLargeClaudeFileKeepsAppendContinuation(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "fixture", "session.jsonl")
	const count = 24000
	lines := make([]string, count)
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for i := range lines {
		lines[i] = string(claudeUsageLine(at.Add(time.Duration(i)*time.Second), fmt.Sprint(i), "", "m", Tokens{Input: 1, Output: 2}, "", true))
	}
	writeLines(t, path, lines...)
	read := func() []Call {
		t.Helper()
		for _, source := range CallSources() {
			if source.Path == path {
				return ReadCallSource(source)
			}
		}
		t.Fatal("source missing")
		return nil
	}
	live := true
	check := func(n int) {
		t.Helper()
		ss := List(0)
		if len(ss) != 1 || ss[0].Input != n || ss[0].Output != 2*n {
			t.Fatalf("summary after append: %+v", ss)
		}
		cs := read()
		var total Tokens
		for _, c := range cs {
			total.add(c.Tokens)
		}
		if len(cs) != n || total.Input != n || total.Output != 2*n {
			t.Fatalf("calls=%d tokens=%+v", len(cs), total)
		}
		if live && (cache[path].Claude == nil || callContinuations[path].state.Claude == nil) {
			t.Fatal("large active file lost continuation")
		}
		if cache[path].Claude.weight() > claudeRevisionEntries || callContinuations[path].state.Claude.weight() > claudeRevisionEntries {
			t.Fatal("recent window exceeded budget")
		}
	}
	check(count)
	before, err := os.Stat(callCachePath(path))
	if err != nil {
		t.Fatal(err)
	}
	// A cold reparse cannot preserve this in-memory marker. This proves List
	// clones its old summary and resumes at Off instead of replaying the prefix.
	cache[path].own().Named = "append continuation marker"
	old := cache[path].Claude.clone()
	for i := 0; i < 3; i++ {
		line := string(claudeUsageLine(at.Add(time.Duration(count+i)*time.Second), fmt.Sprint(count+i), "", "m", Tokens{Input: 1, Output: 2}, "", true))
		appendText(t, path, line+"\n")
		check(count + i + 1)
		if cache[path].more().Named != "append continuation marker" {
			t.Fatal("summary reparsed the prefix")
		}
		after, err := os.Stat(callCachePath(path))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Fatal("call append rebuilt the shard")
		}
	}
	// Recent A/B/A replays still reconcile after the file has outgrown the
	// budget. Replaying the previous final message must not add a call.
	appendText(t, path, lines[count-1]+"\n")
	check(count + 3)
	if old.Messages["message:23999"][""].Usage.Tokens.Output != 2 {
		t.Fatal("mutated published revision snapshot")
	}
	Reset()
	live = false
	check(count + 3)
}

func TestClaudeWindowBoundsOneLongMessage(t *testing.T) {
	s := &state{}
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < claudeRevisionEntries*2; i++ {
		content := fmt.Sprintf(`[{"type":"tool_use","id":%q,"name":"Read","input":{}}]`, fmt.Sprint(i))
		claudeLine(s, claudeUsageLine(at.Add(time.Duration(i)*time.Second), "a", "r", "m", Tokens{Output: i + 1}, content, true), true)
		if s.Claude.weight() > claudeRevisionEntries {
			t.Fatal("single message grew beyond window")
		}
	}
	want := Tokens{Output: claudeRevisionEntries * 2}
	if s.Models.of("m") != want || claudeUsageReplies(s) != 1 {
		t.Fatal("window reset lost the current contribution")
	}
	copy := s.clone()
	claudeLine(copy, claudeUsageLine(at.Add(time.Hour), "b", "r-b", "m", Tokens{Output: 1}, "", true), true)
	if reflect.DeepEqual(s.Models, copy.Models) || len(s.Claude.Messages) != 1 {
		t.Fatal("clone shares mutable state")
	}
}

func TestClaudeCallsReuseRowsBeyondWindow(t *testing.T) {
	st := &callFile{Agent: "claude"}
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < claudeRevisionEntries; i++ {
		claudeCallLine(st, claudeUsageLine(at.Add(time.Duration(i)*time.Second), fmt.Sprint(i), "req", "m", Tokens{Output: 1}, "", true))
	}
	// The old message is outside the window, but its materialized row is still
	// available. A missing request ID must not duplicate it or erase its ID.
	claudeCallLine(st, claudeUsageLine(at.Add(time.Hour), "0", "", "m", Tokens{Output: 3}, "", true))
	if len(st.Calls) != claudeRevisionEntries || st.Calls[0].Output != 3 || st.Calls[0].RequestID != "req" {
		t.Fatal("old row was duplicated or lost identity")
	}
}

// Optional: go test -run '^$' -bench BenchmarkClaudeLargeActive -benchtime=3x
// Keep byte-heavy performance fixtures out of the ordinary regression suite.
func BenchmarkClaudeLargeActive(b *testing.B) {
	for _, padding := range []int{0, 40000} {
		b.Run(fmt.Sprintf("padding=%d", padding), func(b *testing.B) {
			b.Setenv("XDG_CACHE_HOME", b.TempDir())
			Reset()
			b.Cleanup(Reset)
			path := filepath.Join(b.TempDir(), "session.jsonl")
			f, err := os.Create(path)
			if err != nil {
				b.Fatal(err)
			}
			at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
			content := `[{"type":"text","text":"` + strings.Repeat("x", padding) + `"}]`
			for i := 0; i < 24000; i++ {
				nativeLine := claudeUsageLine(at.Add(time.Duration(i)*time.Second), fmt.Sprint(i), "", "m", Tokens{Output: 1}, content, true)
				fmt.Fprintf(f, "{\"uuid\":\"block\",%s\n", nativeLine[1:])
			}
			f.Close()
			source := func() file {
				info, e := os.Stat(path)
				if e != nil {
					b.Fatal(e)
				}
				return file{path: path, key: "fixture", agent: "claude", main: true, size: info.Size(), mod: info.ModTime()}
			}
			b.Run("cold", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					parse(source(), nil)
				}
			})
			b.Run("calls-cold", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					Reset()
					os.Remove(callCachePath(path))
					b.StartTimer()
					readCalls(source())
				}
			})
			b.Run("calls-append", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					if e != nil {
						b.Fatal(e)
					}
					nativeLine := claudeUsageLine(at.Add(time.Duration(48000+i)*time.Second), fmt.Sprint("call-", i), "", "m", Tokens{Output: 1}, "", true)
					fmt.Fprintf(f, "{\"uuid\":\"block\",%s\n", nativeLine[1:])
					f.Close()
					next := source()
					b.StartTimer()
					readCalls(next)
				}
			})

			s := parse(source(), nil)
			b.Run("append", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					if e != nil {
						b.Fatal(e)
					}
					nativeLine := claudeUsageLine(at.Add(time.Duration(24000+i)*time.Second), fmt.Sprint(24000+i), "", "m", Tokens{Output: 1}, "", true)
					fmt.Fprintf(f, "{\"uuid\":\"block\",%s\n", nativeLine[1:])
					f.Close()
					next := source()
					b.StartTimer()
					s = parse(next, s)
				}
			})
		})
	}
}

// After a restart a grown Claude file is read on from where it was left, with
// the window it had: a live 2 GB session was read from its first line on the
// first Requests page after every update (#3493). What it reads is what a
// read from the start makes.
func TestClaudeFileReadOnAfterRestart(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "fixture", "session.jsonl")
	const count = 3000
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	line := func(i int, id string, out int) string {
		return string(claudeUsageLine(at.Add(time.Duration(i)*time.Second), id, "req-"+id, "m", Tokens{Input: 1, Output: out}, "", true))
	}
	lines := make([]string, count)
	for i := range lines {
		lines[i] = line(i, fmt.Sprint(i), 2)
	}
	writeLines(t, path, lines...)
	read := func() []Call {
		t.Helper()
		for _, source := range CallSources() {
			if source.Path == path {
				return ReadCallSource(source)
			}
		}
		t.Fatal("source missing")
		return nil
	}
	read()
	before, err := os.Stat(callCachePath(path))
	if err != nil {
		t.Fatal(err)
	}
	Reset()
	// a recent message written again with more output, the last one replayed
	// as it was, and a new one
	appendText(t, path, line(count, "2990", 9)+"\n"+lines[count-1]+"\n"+line(count+1, "new", 2)+"\n")
	got := read()
	after, err := os.Stat(callCachePath(path))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the grown file was read again from its start")
	}
	Reset()
	os.Remove(callCachePath(path))
	os.Remove(callRevisionsPath(path))
	want := read()
	if len(got) != count+1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("read on: %d calls, from the start: %d", len(got), len(want))
	}
	for _, c := range got {
		if c.Msg == "2990" && c.Output != 9 {
			t.Fatalf("the rewritten message kept its old output: %+v", c)
		}
	}
}

// The Sessions list's own window is kept in sessions.json: after a restart a
// grown Claude file is read on, and what it counts is what a read from the
// start counts (#3493).
func TestClaudeSummaryReadOnAfterRestart(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "fixture", "session.jsonl")
	const count = 3000
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	line := func(i int, id string, out int) string {
		return string(claudeUsageLine(at.Add(time.Duration(i)*time.Second), id, "req-"+id, "m", Tokens{Input: 1, Output: out}, "", true))
	}
	lines := make([]string, count)
	for i := range lines {
		lines[i] = line(i, fmt.Sprint(i), 2)
	}
	writeLines(t, path, lines...)
	List(0)
	// a reparse from the start cannot know this marker: it proves the read
	// after the restart went on from the kept parse
	mu.Lock()
	marked := *cache[path]
	marked.own().Named = "read on marker"
	cache[path] = &marked
	saveCache()
	mu.Unlock()
	Reset()
	appendText(t, path, line(count, "2990", 9)+"\n"+lines[count-1]+"\n"+line(count+1, "new", 2)+"\n")
	got := List(0)
	mu.Lock()
	named := cache[path].more().Named
	mu.Unlock()
	if named != "read on marker" {
		t.Fatal("the grown file was parsed again from its start")
	}
	Reset()
	os.Remove(CachePath())
	want := List(0)
	if len(got) != 1 || len(want) != 1 || got[0].Input != want[0].Input || got[0].Output != want[0].Output || got[0].Input != count+1 || got[0].Output != 2*(count+1)+7 {
		t.Fatalf("read on: %+v, from the start: %+v", got, want)
	}
}
