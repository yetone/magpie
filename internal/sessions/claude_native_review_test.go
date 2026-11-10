package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClaudeNativeReplayAcrossReadersAndRestart(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-native-replay.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := Tokens{10338, 37363, 281066, 63398, 0}
	for split := 0; split <= len(lines); split++ {
		t.Run(string(rune('0'+split)), func(t *testing.T) {
			d := setupCalls(t)
			path := filepath.Join(d.claude, "projects", "fixture", "session.jsonl")
			writeLines(t, path, lines[:split]...)
			Calls(time.Time{})
			List(0)
			Reset()
			if split < len(lines) {
				appendText(t, path, strings.Join(lines[split:], "\n")+"\n")
			}
			check := func(cs []Call) {
				t.Helper()
				var total Tokens
				for _, c := range cs {
					total.add(c.Tokens)
				}
				if len(cs) != 2 || total != want {
					t.Fatalf("calls=%d tokens=%+v, want 2 %+v", len(cs), total, want)
				}
			}
			cs := Calls(time.Time{})
			check(cs)
			for _, source := range CallSources() {
				if source.Path == path {
					check(ReadCallSource(source))
				}
			}
			if list := List(0); len(list) != 1 || list[0].Tokens != want {
				t.Fatalf("summary differs: %+v", list)
			}
			// The summary keeps its window on disk (#3493), bounded as in memory.
			b, err := json.Marshal(cache[path])
			if err != nil {
				t.Fatal(err)
			}
			var kept state
			if err := json.Unmarshal(b, &kept); err != nil || kept.Claude.weight() > claudeRevisionEntries {
				t.Fatalf("summary window kept: %v weight=%d", err, kept.Claude.weight())
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			shard := loadCalls(file{path: path, agent: "claude", size: info.Size(), mod: modOf(info.ModTime())})
			if shard == nil || shard.Claude != nil {
				t.Fatal("call shard persisted block/tool maps")
			}
			Reset()
			if again := Calls(time.Time{}); !reflect.DeepEqual(cs, again) {
				t.Fatal("restart changed native calls")
			}
		})
	}
}

func TestClaudeDesktopCopyOfMessageIsCountedOnce(t *testing.T) {
	d := setupCalls(t)
	line := claudeMsg("shared-message", "claude-opus-5-5", 10, 2, 30, 4, 1)
	writeLines(t, filepath.Join(d.claude, "projects", "fixture", "session.jsonl"), line)
	writeLines(t, filepath.Join(d.desktop, "local-agent-mode-sessions", "org", "workspace", "local_fixture", ".claude", "projects", "fixture", "session.jsonl"), line)
	if cs := Calls(time.Time{}); len(cs) != 1 {
		t.Fatalf("CLI/Desktop copies both counted: %+v", cs)
	}
}

func TestEvictedClaudeRevisionsReleasedAndRebuilt(t *testing.T) {
	d := setupCalls(t)
	data, err := os.ReadFile("testdata/claude-native-replay.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	path := filepath.Join(d.claude, "projects", "fixture", "session.jsonl")
	writeLines(t, path, lines[:2]...)
	old := time.Now().Add(-time.Hour)
	if err = os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	// Eight larger sources displace this window even when it is idle. Size,
	// not an idle timer, determines which bounded windows are worth keeping.
	for i := 0; i < revisionFiles; i++ {
		p := filepath.Join(filepath.Dir(path), fmt.Sprintf("larger-%d.jsonl", i))
		writeLines(t, p, string(claudeUsageLine(time.Now(), fmt.Sprint(i), "", "m", Tokens{Output: 1}, `[{"type":"text","text":"`+strings.Repeat("x", 8192)+`"}]`, true)))
	}

	List(0)
	Calls(time.Time{})
	if cache[path].Claude != nil || callCache[path].Claude != nil {
		t.Fatal("evicted Claude retained message indexes")
	}
	for _, source := range CallSources() {
		ReadCallSource(source)
	}
	if callContinuations[path].state.Claude != nil {
		t.Fatal("evicted Claude retained continuation")
	}
	appendText(t, path, lines[2]+"\n")
	want := Tokens{10338, 37363, 281066, 63398, 0}
	List(0)
	var got Tokens
	for _, v := range cache[path].Models.all() {
		got.add(v)
	}
	if got != want {
		t.Fatal("reopened summary counted replay", got)
	}
	n := 0
	for _, c := range Calls(time.Time{}) {
		if c.File == path {
			n++
		}
	}
	if n != 2 {
		t.Fatal("reopened calls counted replay", n)
	}
	mu.Lock()
	trimSummaryRevisions(time.Now().Add(revisionIdle + time.Second))
	mu.Unlock()
	callsMu.Lock()
	trimCallRevisions(time.Now().Add(revisionIdle + time.Second))
	callsMu.Unlock()
	if cache[path].Claude != nil || callCache[path].Claude != nil {
		t.Fatal("cheaper source displaced larger windows")
	}
}
