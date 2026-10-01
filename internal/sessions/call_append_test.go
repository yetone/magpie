package sessions

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCallFramesAppendPatchAndRecover(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "p", "s.jsonl")
	var lines []string
	for i := 0; i < 2000; i++ {
		lines = append(lines, claudeMsg(fmt.Sprint(i), "claude-sonnet-5", 10, 1, 2, 3, i))
	}
	writeLines(t, path, lines...)
	Calls(time.Time{})
	before, _ := os.ReadFile(callCachePath(path))
	// A later block updates an existing message, in addition to a new message.
	appendText(t, path, claudeMsg("0", "claude-sonnet-5", 10, 19, 2, 3, 2001)+"\n"+claudeMsg("new", "claude-sonnet-5", 12, 2, 1, 0, 2002)+"\n")
	want := Calls(time.Time{})
	after, _ := os.ReadFile(callCachePath(path))
	if !bytes.HasPrefix(after, before) || len(after)-len(before) > 16<<10 {
		t.Fatalf("append rewrote history or grew too much: %d -> %d", len(before), len(after))
	}
	if len(want) != 2001 || want[1].Output != 19 {
		t.Fatal("existing message update lost")
	}
	resetCalls()
	if got := Calls(time.Time{}); !reflect.DeepEqual(got, want) {
		t.Fatal("frame restart lost patches or new calls")
	}
	// Simulate a process dying midway through the next frame.
	appendText(t, callCachePath(path), "\x20\x00\x00")
	resetCalls()
	if got := Calls(time.Time{}); !reflect.DeepEqual(got, want) {
		t.Fatal("partial frame was not rebuilt")
	}
	// Recovery must leave a usable shard, including the following append.
	appendText(t, path, claudeMsg("final", "claude-sonnet-5", 1, 1, 0, 0, 2003)+"\n")
	Calls(time.Time{})
	resetCalls()
	if n := len(Calls(time.Time{})); n != 2002 {
		t.Fatal(n)
	}
}

func TestSamePrefixRewriteWithGrowth(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "p", "s.jsonl")
	prefix := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("p", 400) + `"},"sessionId":"s"}`
	writeLines(t, path, prefix, claudeMsg("old", "claude-sonnet-5", 10, 1, 0, 0, 1))
	Calls(time.Time{})
	List(0)
	writeLines(t, path, prefix, claudeMsg("new", "claude-sonnet-5", 20, 2, 0, 0, 2), claudeMsg("extra", "claude-sonnet-5", 30, 3, 0, 0, 3))
	cs := Calls(time.Time{})
	if len(cs) != 2 || cs[1].Input != 20 {
		t.Fatalf("same-head grown rewrite treated as append: %+v", cs)
	}
	ss := List(0)
	if len(ss) != 1 || ss[0].Input != 50 {
		t.Fatalf("summary missed rewritten content: %+v", ss)
	}
}

func TestAbandonedCallTempsAndLRU(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "p", "s.jsonl")
	writeLines(t, path, claudeMsg("m", "m", 1, 1, 0, 0, 1))
	Calls(time.Time{})
	old := filepath.Join(callCacheDir(), "calls-old.tmp")
	live := filepath.Join(callCacheDir(), "calls-live.tmp")
	os.WriteFile(old, []byte("partial"), 0600)
	os.WriteFile(live, []byte("partial"), 0600)
	then := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, then, then)
	Calls(time.Time{})
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("abandoned temp remains")
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatal("active writer temp removed")
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	for i := 0; i < maxKeptFiles; i++ {
		keepCalls(fmt.Sprint(i), &callFile{Calls: []Call{{}}})
	}
	keepCalls("0", callCache["0"])
	keepCalls("next", &callFile{Calls: []Call{{}}})
	if callCache["0"] == nil || callCache["1"] != nil {
		t.Fatal("cache evicts recent entry instead of least recent")
	}
}

func TestDiscoveryDetectsNewCoworkSessions(t *testing.T) {
	d := setupCalls(t)
	Calls(time.Time{})
	for i := 0; i < 2; i++ {
		path := filepath.Join(d.desktop, "local-agent-mode-sessions", "a", "o", fmt.Sprintf("local_%d", i), ".claude", "projects", "p", "s.jsonl")
		writeLines(t, path, claudeMsg(fmt.Sprint(i), "m", 1, 1, 0, 0, i))
		if n := len(Calls(time.Time{})); n != i+1 {
			t.Fatalf("new nested session hidden: %d", n)
		}
	}
	os.RemoveAll(filepath.Join(d.desktop, "local-agent-mode-sessions", "a", "o", "local_0"))
	if n := len(Calls(time.Time{})); n != 1 {
		t.Fatal("deleted session retained", n)
	}
}

// A request reader releases its expanded cache, then resumes from the shard.
// Codex's previous cumulative counters must survive that disk round trip.
func TestCodexAppendAfterShardReload(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.codex, "sessions", "rollout-2026-09-20T10-00-00-s.jsonl")
	writeLines(t, path, `{"type":"session_meta","payload":{"id":"s"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6-astra"}}`,
		tokenCountLine(1, cxUse(100, 20, 0, 10, 2), cxUse(100, 20, 0, 10, 2)))
	read := func() []Call {
		fs := CallSources()
		if len(fs) != 1 {
			t.Fatal(len(fs))
		}
		return ReadCallSource(fs[0])
	}
	if n := len(read()); n != 1 {
		t.Fatal(n)
	}
	appendText(t, path, tokenCountLine(2, cxUse(150, 30, 0, 15, 3), cxUse(50, 10, 0, 5, 1))+"\n")
	got := read()
	if len(got) != 2 || got[1].Tokens != (Tokens{Input: 40, CacheRead: 10, Output: 5}) || got[1].Reasoning != 1 {
		t.Fatalf("lost previous cumulative counters: %+v", got)
	}
	// Repeated totals after yet another reload must not create a phantom call.
	appendText(t, path, tokenCountLine(3, cxUse(150, 30, 0, 15, 3), cxUse(50, 10, 0, 5, 1))+"\n")
	if n := len(read()); n != 2 {
		t.Fatalf("duplicate total created %d calls", n)
	}
}
