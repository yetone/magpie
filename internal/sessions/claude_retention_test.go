package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQoderUsageAfterWindowLoss(t *testing.T) {
	for _, agent := range []string{"qoder", "qoder-cn"} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%v", agent, restart), func(t *testing.T) {
				setupCalls(t)
				path := filepath.Join(QoderDir(agent), "projects", "fixture", "session.jsonl")
				at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
				writeLines(t, path, string(claudeUsageLine(at, "a", "r", "m", Tokens{Input: 100, Output: 1}, "", true)))
				List(0)
				if restart {
					// A sessions.json from before the window was kept (#3493).
					Saved()
					b, err := os.ReadFile(CachePath())
					if err != nil {
						t.Fatal(err)
					}
					var c map[string]any
					dec := json.NewDecoder(bytes.NewReader(b))
					dec.UseNumber()
					if err := dec.Decode(&c); err != nil {
						t.Fatal(err)
					}
					delete(c["files"].(map[string]any)[path].(map[string]any), "claude")
					b, _ = json.Marshal(c)
					if err := os.WriteFile(CachePath(), b, 0o644); err != nil {
						t.Fatal(err)
					}
					Reset()
					List(0)
				} else {
					for i := 0; i < revisionFiles; i++ {
						p := filepath.Join(filepath.Dir(path), fmt.Sprintf("other-%d.jsonl", i))
						writeLines(t, p, string(claudeUsageLine(at, "other", "", "m", Tokens{Output: 1}, `[{"type":"text","text":"`+strings.Repeat("x", 512)+`"}]`, true)))
					}
					List(0)
				}
				if cache[path].Claude != nil {
					t.Fatal("test did not lose its revision window")
				}
				appendText(t, path, string(claudeUsageLine(at.Add(time.Second), "a", "r", "m", Tokens{Input: 100, Output: 50}, "", true))+"\n")
				List(0)
				if got := cache[path].Models.of("m"); got != (Tokens{Input: 100, Output: 50}) {
					t.Fatalf("after window loss: %+v, want input=100 output=50", got)
				}
			})
		}
	}
}

func TestClaudeLargeWindowSurvivesIdleAndSmallFiles(t *testing.T) {
	for _, sourceReader := range []bool{false, true} {
		t.Run(fmt.Sprint("source-reader=", sourceReader), func(t *testing.T) {
			d := setupCalls(t)
			path := filepath.Join(d.claude, "projects", "fixture", "large.jsonl")
			at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
			lines := make([]string, 4000)
			for i := range lines {
				lines[i] = string(claudeUsageLine(at.Add(time.Duration(i)*time.Second), fmt.Sprint(i), "", "m", Tokens{Input: 1, Output: 2}, "", true))
			}
			writeLines(t, path, lines...)
			read := func() {
				List(0)
				if sourceReader {
					for _, s := range CallSources() {
						ReadCallSource(s)
					}
				} else {
					Calls(time.Time{})
				}
			}
			read()
			before, err := os.Stat(callCachePath(path))
			if err != nil {
				t.Fatal(err)
			}
			snapshot := cache[path]
			snapshot.own().Named = "must resume this summary"
			// Cross the old hard cutoff without sleeping. The cutoff used source
			// mtime, so advancing only the eviction clock reproduces idle expiry.
			later := time.Now().Add(2 * revisionIdle)
			mu.Lock()
			trimSummaryRevisions(later)
			mu.Unlock()
			callsMu.Lock()
			trimCallRevisions(later)
			callsMu.Unlock()
			if cache[path].Claude == nil {
				t.Fatal("idle cutoff discarded the large window")
			}
			if sourceReader {
				if callContinuations[path].state.Claude == nil {
					t.Fatal("idle cutoff discarded source continuation")
				}
			} else if callCache[path].Claude == nil {
				t.Fatal("idle cutoff discarded calls window")
			}
			// Eight newer, cheaper files must not evict the expensive source.
			for i := 0; i < revisionFiles; i++ {
				p := filepath.Join(filepath.Dir(path), fmt.Sprintf("small-%d.jsonl", i))
				writeLines(t, p, string(claudeUsageLine(at, "small-"+fmt.Sprint(i), "", "m", Tokens{Output: 1}, "", true)))
			}
			read()
			if cache[path].Claude == nil {
				t.Fatal("eight newer small files evicted the large summary")
			}
			appendText(t, path, string(claudeUsageLine(at.Add(time.Hour), "new", "", "m", Tokens{Input: 1, Output: 2}, "", true))+"\n")
			read()
			if cache[path].more().Named != "must resume this summary" {
				t.Fatal("summary reparsed the large prefix")
			}
			if got := cache[path].Models.of("m"); got != (Tokens{Input: 4001, Output: 8002}) {
				t.Fatal(got)
			}
			after, err := os.Stat(callCachePath(path))
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("call append rebuilt the large shard")
			}
			if snapshot.Claude == nil {
				t.Fatal("eviction changed a published snapshot")
			}
			n, weight := 0, 0
			for _, s := range cache {
				if s.Claude != nil {
					n++
					weight += s.Claude.weight()
				}
			}
			if n > revisionFiles || weight > revisionEntries {
				t.Fatalf("summary windows: files=%d weight=%d", n, weight)
			}
			n, weight = 0, 0
			for _, s := range callCache {
				if s.Claude != nil {
					n++
					weight += s.Claude.weight()
				}
			}
			if n > revisionFiles || weight > revisionEntries {
				t.Fatalf("call windows: files=%d weight=%d", n, weight)
			}
			weight = 0
			for _, c := range callContinuations {
				weight += c.state.revisionWeight()
			}
			if len(callContinuations) > revisionFiles || weight > revisionEntries {
				t.Fatalf("continuation windows: files=%d weight=%d", len(callContinuations), weight)
			}

		})
	}
}

func TestClaudeWindowReservationsAndRemoval(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "fixture", "small.jsonl")
	now := time.Now()
	writeLines(t, path, string(claudeUsageLine(now, "a", "r", "m", Tokens{Output: 1}, "", true)))
	List(0)
	// Unchanged large summaries loaded from disk have no window to retain.
	// They must not reserve slots that stop a smaller active file continuing.
	for i := 0; i < revisionFiles; i++ {
		cache[fmt.Sprint("disk-only-", i)] = &state{Size: 1 << 30}
	}
	cache[path] = cache[path].withoutRevisions()
	// An additional Claude-compatible source uses the same default dispatch;
	// retention must not carry a second hard-coded list of agent names.
	windows := summaryRevisionWindows([]file{{path: path, agent: "claude-compatible", size: 1024, mod: modOf(now)}}, now)
	if !windows[path] || len(windows) != 1 {
		t.Fatal("disk-only summaries reserved phantom slots", windows)
	}
	for _, s := range CallSources() {
		ReadCallSource(s)
	}
	if callContinuations[path].state.Claude == nil {
		t.Fatal("no continuation to prune")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	CallSources()
	if _, ok := callContinuations[path]; ok {
		t.Fatal("deleted source retained its window")
	}
}
