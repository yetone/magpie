package gui

// Run with MAGPIE_USAGE_SCALE=1 go test -tags nogui ./internal/gui
// -run TestUsageScale -count=1 -v. The fixture is entirely synthetic.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

func TestUsageScale(t *testing.T) {
	if os.Getenv("MAGPIE_USAGE_SCALE") != "1" {
		t.Skip("large synthetic fixture; opt in with MAGPIE_USAGE_SCALE=1")
	}
	home := sandboxHome(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("OPENAI_BASE_URL", "")
	sessions.Reset()
	catalog.Reset()
	t.Cleanup(func() { sessions.Reset(); catalog.Reset() })
	now := time.Now().Add(-time.Hour)
	active := os.Getenv("MAGPIE_USAGE_ACTIVE") == "1"
	wantCalls := 300_000
	var activePaths []string
	if active {
		wantCalls += 4 * (20000 - 150)
	}
	for i := 0; i < 2000; i++ {
		at := now.AddDate(0, 0, -(i % 90))
		sid := fmt.Sprintf("scale-%04d", i)
		path := filepath.Join(sessions.ClaudeDir(), "projects", "scale", sid+".jsonl")
		if i%4 == 1 {
			path = filepath.Join(sessions.DesktopDataDirs()[0], "local-agent-mode-sessions", "account", "org", "local_"+sid, ".claude", "projects", "scale", sid+".jsonl")
		}
		if i%4 == 3 {
			path = filepath.Join(sessions.CodexDir(), "sessions", "2026", "09", "30", "rollout-2026-09-30T00-00-00-"+sid+".jsonl")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w := bufio.NewWriter(f)
		if i%4 == 3 {
			fmt.Fprintf(w, `{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"cwd":"/work/scale"}}`+"\n", at.Format(time.RFC3339), sid)
			fmt.Fprintln(w, `{"type":"turn_context","payload":{"model":"gpt-6.1-sol","effort":"high"}}`)
		} else {
			fmt.Fprintf(w, `{"type":"user","timestamp":%q,"message":{"role":"user","content":"Synthetic scale fixture"},"sessionId":%q}`+"\n", at.Format(time.RFC3339), sid)
		}
		count := 150
		if active && i%90 == 0 && len(activePaths) < 4 {
			count = 20000
			activePaths = append(activePaths, path)
		}
		for j := 0; j < count; j++ {
			stamp := at.Add(time.Duration(j+1) * time.Second).Format(time.RFC3339)
			if i%4 == 3 {
				fmt.Fprintf(w, `{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}}}`+"\n", stamp, (j+1)*100, (j+1)*20, (j+1)*10)
			} else {
				fmt.Fprintf(w, `{"type":"assistant","timestamp":%q,"sessionId":%q,"requestId":"r-%d-%d","message":{"id":"m-%d-%d","model":"claude-sonnet-5","content":[{"type":"text","text":"Synthetic reply"}],"usage":{"input_tokens":80,"cache_read_input_tokens":20,"output_tokens":10}}}`+"\n", stamp, sid, i, j, i, j)
			}
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at.Add(3*time.Minute), at.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(usage.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i := 0; i < 50_000; i++ {
		if err := enc.Encode(usage.Record{Time: now.AddDate(0, 0, -(i % 90)), Agent: "opencode", Provider: "relay", Model: "gpt-6.1-sol", Input: 80, Output: 10, Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if n := len(sessions.List(2000)); n != 2000 {
		t.Fatalf("sessions: %d", n)
	}
	if n := len(sessions.Calls(time.Time{})); n != wantCalls {
		t.Fatalf("calls: %d", n)
	}
	sessions.Saved()
	info, err := os.Stat(sessions.CachePath())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("summary index: %.2f MiB", float64(info.Size())/(1<<20))
	sessions.Reset()
	runtime.GC()
	start := time.Now()
	_ = sessions.List(0)
	t.Logf("Sessions after restart: %s", time.Since(start))
	start = time.Now()
	_ = sessions.List(0)
	t.Logf("Sessions warm: %s", time.Since(start))
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("heap after Sessions: %.2f MiB", float64(mem.HeapAlloc)/(1<<20))
	for _, p := range []usage.Period{usage.Month, usage.All} {
		for _, phase := range []string{"first", "repeat"} {
			runtime.GC()
			stop := scaleMemory()
			start = time.Now()
			page := ledgerPage(p, usage.Filter{}, 0, 100)
			elapsed := time.Since(start)
			peak := stop()
			runtime.GC()
			runtime.ReadMemStats(&mem)
			t.Logf("Requests %s %s retained heap: %.2f MiB", p, phase, float64(mem.HeapAlloc)/(1<<20))
			t.Logf("Requests %s %s: %s (%d rows), peak HeapInuse %.2f MiB", p, phase, elapsed, page.Total, float64(peak)/(1<<20))
			if p == usage.All && page.Total != wantCalls+50_000 {
				t.Fatalf("wrong total: %d", page.Total)
			}
		}
	}

	if active {
		before := scaleShardBytes(t)
		snapshot := scaleShardSnapshot()
		var written int64
		var elapsed time.Duration
		var peak uint64
		for tick := 0; tick < 12; tick++ {
			for i, path := range activePaths {
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(file, `{"type":"assistant","timestamp":%q,"sessionId":"active","requestId":"active-%d-%d","message":{"id":"active-%d-%d","model":"claude-sonnet-5","usage":{"input_tokens":80,"output_tokens":10,"cache_read_input_tokens":20}}}`+"\n", time.Now().Format(time.RFC3339Nano), i, tick, i, tick)
				file.Close()
			}
			stop := scaleMemory()
			start = time.Now()
			page := ledgerPage(usage.All, usage.Filter{}, 0, 100)
			elapsed += time.Since(start)
			next := scaleShardSnapshot()
			for path, st := range next {
				old := snapshot[path]
				if old == nil {
					written += st.Size()
				} else if !old.ModTime().Equal(st.ModTime()) || old.Size() != st.Size() {
					if os.SameFile(old, st) && st.Size() >= old.Size() {
						written += st.Size() - old.Size()
					} else {
						written += st.Size()
					}
				}
			}
			snapshot = next
			peak = max(peak, stop())
			if page.Total != wantCalls+50_000+4*(tick+1) {
				t.Fatalf("active refresh total %d", page.Total)
			}
		}
		after := scaleShardBytes(t)
		t.Logf("4 active 20k-call sessions, 12 refreshes: mean %s, peak HeapInuse %.2f MiB, shard growth %.3f MiB, cache writes %.3f MiB/min (5s cadence)", elapsed/12, float64(peak)/(1<<20), float64(after-before)/(1<<20), float64(written)/(1<<20))
	}
}

func scaleShardBytes(t *testing.T) int64 {
	t.Helper()
	var n int64
	filepath.WalkDir(filepath.Dir(sessions.CachePath()), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if st, e := d.Info(); e == nil {
				n += st.Size()
			}
		}
		return nil
	})
	return n
}
func scaleMemory() func() uint64 {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	var peak uint64
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapInuse)
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() uint64 { close(done); wg.Wait(); return peak }
}

func scaleShardSnapshot() map[string]os.FileInfo {
	out := map[string]os.FileInfo{}
	filepath.WalkDir(filepath.Dir(sessions.CachePath()), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if st, e := d.Info(); e == nil {
				out[path] = st
			}
		}
		return nil
	})
	return out
}
