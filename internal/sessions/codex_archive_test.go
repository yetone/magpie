package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Archiving changes where a rollout lives, not its usage. A temporary
// byte-for-byte copy during that move must not count twice, even if packed.
func TestCodexArchivedRollouts(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "packed"}[compressed], func(t *testing.T) {
			d := setupCalls(t)
			name := "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"
			live := filepath.Join(d.codex, "sessions", "2026", "09", "20", name)
			archived := filepath.Join(d.codex, "archived_sessions", name)
			lines := append(cxTurnLines(1, "turn-1", "gpt-6-astra", "low"),
				tokenCountLine(3, cxUse(1000, 0, 0, 50, 0), cxUse(1000, 0, 0, 50, 0)))
			writeLines(t, live, lines...)
			before := codexOf(t, List(0))
			calls := Calls(time.Time{})
			if len(calls) != 1 {
				t.Fatalf("calls: %d", len(calls))
			}
			writeLines(t, archived, lines...)
			if compressed {
				archived = pack(t, archived, false)
			}
			if fs := codexFiles(); len(fs) != 1 {
				t.Fatalf("copied rollout counted %d times", len(fs))
			}
			if got := codexOf(t, List(0)); got.Tokens != before.Tokens {
				t.Fatalf("copy: %+v != %+v", got.Tokens, before.Tokens)
			}
			if err := os.Remove(live); err != nil {
				t.Fatal(err)
			}
			for _, reset := range []bool{false, true} {
				if reset {
					Reset()
				}
				got := codexOf(t, List(0))
				if got.ID != before.ID || got.Tokens != before.Tokens || got.Path != archived {
					t.Fatalf("archive (reset=%v): %+v", reset, got)
				}
				gotCalls := Calls(time.Time{})
				if len(gotCalls) != 1 || gotCalls[0].Tokens != calls[0].Tokens || !gotCalls[0].Time.Equal(calls[0].Time) {
					t.Fatalf("archive calls: %+v", gotCalls)
				}
			}
		})
	}
}

func TestCodexArchivedPrefixCopies(t *testing.T) {
	for _, longerArchive := range []bool{false, true} {
		for _, packedLive := range []bool{false, true} {
			for _, packedArchive := range []bool{false, true} {
				t.Run(fmt.Sprintf("longer_archive=%v/live_zst=%v/archive_zst=%v", longerArchive, packedLive, packedArchive), func(t *testing.T) {
					d := setupCalls(t)
					name := "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"
					live, archived := filepath.Join(d.codex, "sessions", name), filepath.Join(d.codex, "archived_sessions", name)
					first := append(cxTurnLines(1, "t1", "gpt-6-astra", "low"), tokenCountLine(3, cxUse(1000, 0, 0, 50, 0), cxUse(1000, 0, 0, 50, 0)))
					longer := append(append([]string(nil), first...), tokenCountLine(4, cxUse(2000, 0, 0, 100, 0), cxUse(1000, 0, 0, 50, 0)))
					liveLines, archiveLines := longer, first
					if longerArchive {
						liveLines, archiveLines = first, longer
					}
					writeLines(t, live, liveLines...)
					writeLines(t, archived, archiveLines...)
					if packedLive {
						live = pack(t, live, false)
					}
					if packedArchive {
						archived = pack(t, archived, false)
					}
					wantPath := live
					if longerArchive {
						wantPath = archived
					}
					if fs := codexFiles(); len(fs) != 1 || fs[0].path != wantPath {
						t.Fatalf("did not select complete decoded prefix extension: %+v", fs)
					}
					if calls := Calls(time.Time{}); len(calls) != 2 {
						t.Fatalf("prefix copies produced %d calls, want 2", len(calls))
					}
					if got := codexOf(t, List(0)); got.Tokens != (Tokens{Input: 2000, Output: 100}) {
						t.Fatalf("prefix copied tokens were counted twice: %+v", got.Tokens)
					}
				})
			}
		}
	}
}

func TestCodexArchivedDiscoveryCacheAndAppend(t *testing.T) {
	d := setupCalls(t)
	name := "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"
	live, archived := filepath.Join(d.codex, "sessions", name), filepath.Join(d.codex, "archived_sessions", name)
	lines := append(cxTurnLines(1, "t1", "gpt-6-astra", "low"), tokenCountLine(3, cxUse(1000, 0, 0, 50, 0), cxUse(1000, 0, 0, 50, 0)))
	writeLines(t, live, lines...)
	writeLines(t, archived, lines...)
	packedLive := pack(t, live, false)
	info, err := codexDiscoveryStat(packedLive)
	if err != nil {
		t.Fatal(err)
	}
	if codexChangeStamp(info) == "" {
		t.Skip("filesystem exposes no change-time stamp; safe fallback reads content")
	}
	reads, compare := 0, compareCodexCopies
	compareCodexCopies = func(a, b string) (int, error) {
		reads++
		return compare(a, b)
	}
	t.Cleanup(func() { compareCodexCopies = compare })
	for range 3 {
		if len(codexFiles()) != 1 || len(Calls(time.Time{})) != 1 {
			t.Fatal("identical archive was not deduplicated")
		}
		List(0)
	}
	if reads != 1 {
		t.Fatalf("unchanged archive was decoded %d times, want 1", reads)
	}
	appendText(t, archived, tokenCountLine(4, cxUse(2000, 0, 0, 100, 0), cxUse(1000, 0, 0, 50, 0))+"\n")
	if fs := codexFiles(); len(fs) != 1 || fs[0].path != archived {
		t.Fatalf("appended archive did not replace shorter live file: %+v", fs)
	}
	if reads != 2 || len(Calls(time.Time{})) != 2 {
		t.Fatalf("append did not invalidate comparison: reads=%d", reads)
	}
}

func TestCodexArchivedSameSizeRewriteInvalidatesDiscovery(t *testing.T) {
	d := setupCalls(t)
	name := "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"
	live, archived := filepath.Join(d.codex, "sessions", name), filepath.Join(d.codex, "archived_sessions", name)
	writeLines(t, live, `{"x":1}`)
	writeLines(t, archived, `{"x":1}`)
	info, err := os.Stat(archived)
	if err != nil {
		t.Fatal(err)
	}
	if len(codexFiles()) != 1 {
		t.Fatal("initial copies were not deduplicated")
	}
	writeLines(t, archived, `{"x":2}`)
	if err := os.Chtimes(archived, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if fs := codexFiles(); len(fs) != 2 {
		t.Fatalf("same-size rewrite with restored mtime reused a stale relation: %+v", fs)
	}
}

func TestCodexArchivedReadErrorIsNotCached(t *testing.T) {
	d := setupCalls(t)
	name := "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"
	live, archived := filepath.Join(d.codex, "sessions", name), filepath.Join(d.codex, "archived_sessions", name)
	writeLines(t, live, `{"x":1}`)
	writeLines(t, archived, `{"x":1}`)
	compressed := pack(t, archived, false)
	b, err := os.ReadFile(compressed)
	if err != nil {
		t.Fatal(err)
	}
	// Even if all decoded bytes were available, a missing trailer cannot
	// prove the compressed copy complete and must be retried next time.
	if err := os.WriteFile(compressed, b[:len(b)-1], 0600); err != nil {
		t.Fatal(err)
	}
	reads, compare := 0, compareCodexCopies
	compareCodexCopies = func(a, b string) (int, error) {
		reads++
		return compare(a, b)
	}
	t.Cleanup(func() { compareCodexCopies = compare })
	for range 2 {
		if len(codexFiles()) != 2 {
			t.Fatal("incomplete compressed data was accepted as a proven copy")
		}
	}
	if reads != 2 {
		t.Fatalf("read error was cached: comparisons=%d", reads)
	}
	if err := os.WriteFile(compressed, b, 0600); err != nil {
		t.Fatal(err)
	}
	if len(codexFiles()) != 1 {
		t.Fatal("complete repaired copy was not deduplicated")
	}
}

func TestCodexPlainTwinProtectsIncompleteCompression(t *testing.T) {
	d := setupCalls(t)
	name := "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"
	live := filepath.Join(d.codex, "sessions", name)
	lines := append(cxTurnLines(1, "t1", "gpt-6-astra", "low"), tokenCountLine(3, cxUse(1000, 0, 0, 50, 0), cxUse(1000, 0, 0, 50, 0)))
	writeLines(t, live, lines...)
	if err := os.WriteFile(live+zstSuffix, []byte("incomplete compression"), 0600); err != nil {
		t.Fatal(err)
	}
	if fs := codexFiles(); len(fs) != 1 || fs[0].path != live {
		t.Fatalf("incomplete same-directory twin bypassed plain copy: %+v", fs)
	}
	if calls := Calls(time.Time{}); len(calls) != 1 {
		t.Fatalf("plain twin should provide exactly one call, got %d", len(calls))
	}
}

// Similar names alone are no evidence of duplication. Preserve distinct
// segments and same-name files whose contents differ.
func TestCodexArchivedDivergentFiles(t *testing.T) {
	d := setupCalls(t)
	name := "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl"
	writeLines(t, filepath.Join(d.codex, "sessions", name), `{"type":"first"}`)
	writeLines(t, filepath.Join(d.codex, "archived_sessions", name), `{"type":"second"}`)
	writeLines(t, filepath.Join(d.codex, "archived_sessions", name[:len(name)-6]+"_segment.jsonl"), `{"type":"first"}`)
	fs := codexFiles()
	if len(fs) != 3 {
		t.Fatalf("want all three distinct files, got %+v", fs)
	}
	if !reflect.DeepEqual(fs, codexFiles()) {
		t.Fatal("discovery order changed")
	}
}
