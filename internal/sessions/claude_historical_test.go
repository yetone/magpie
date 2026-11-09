package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoricalClaudeFilesDoNotDisplaceActiveWindows(t *testing.T) {
	for _, sourceReader := range []bool{false, true} {
		t.Run(fmt.Sprint("source-reader=", sourceReader), func(t *testing.T) {
			d := setupCalls(t)
			at := time.Now().UTC().Add(-time.Minute)
			var historical []string
			for i := 0; i < revisionFiles; i++ {
				path := filepath.Join(d.claude, "projects", "fixture", fmt.Sprintf("history-%d.jsonl", i))
				historical = append(historical, path)
				writeLines(t, path, string(claudeUsageLine(at, fmt.Sprint("old-", i), "", "m", Tokens{Output: 1}, `[{"type":"text","text":"`+strings.Repeat("x", 64<<10)+`"}]`, true)))
				old := time.Now().Add(-48 * time.Hour)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			claude := filepath.Join(d.claude, "projects", "fixture", "active.jsonl")
			writeLines(t, claude, string(claudeUsageLine(at, "active", "r", "m", Tokens{Input: 100, Output: 1}, `[{"type":"text","text":"`+strings.Repeat("x", 32<<10)+`"}]`, true)))
			codex := filepath.Join(d.codex, "sessions", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
			writeLines(t, codex, append(cxTurnLines(0, "t", "gpt-6-astra", "high"), tokenCountLine(1, cxUse(100, 0, 0, 1, 0), cxUse(100, 0, 0, 1, 0)))...)
			read := func() {
				List(0)
				var calls []Call
				if sourceReader {
					for _, s := range CallSources() {
						calls = append(calls, ReadCallSource(s)...)
					}
				} else {
					calls = Calls(time.Time{})
				}
				totals := map[string]Tokens{}
				for _, c := range calls {
					v := totals[c.File]
					v.add(c.Tokens)
					totals[c.File] = v
				}
				if totals[claude] != cache[claude].Models.of("m") || totals[codex] != cache[codex].Models.of("gpt-6-astra") {
					t.Fatal("calls and summaries disagree", totals[claude], totals[codex])
				}
			}
			read()
			saved() // the marker below is written on a parse a save may be encoding
			before := map[string]os.FileInfo{}
			for _, path := range []string{claude, codex} {
				if cache[path].revisionWeight() == 0 {
					t.Fatal("history displaced active summary", path)
				}
				cache[path].own().Named = "incremental marker"
				st, err := os.Stat(callCachePath(path))
				if err != nil {
					t.Fatal(err)
				}
				before[path] = st
			}
			for i := 0; i < 3; i++ {
				appendText(t, claude, string(claudeUsageLine(at.Add(time.Duration(i+1)*time.Second), "active", "r", "m", Tokens{Input: 100, Output: i + 2}, "", true))+"\n")
				appendText(t, codex, tokenCountLine(i+2, cxUse(100*(i+2), 0, 0, i+2, 0), cxUse(100, 0, 0, 1, 0))+"\n")
				read()
				for _, path := range []string{claude, codex} {
					if cache[path].more().Named != "incremental marker" {
						t.Fatal("reparsed active summary", path)
					}
					st, err := os.Stat(callCachePath(path))
					if err != nil {
						t.Fatal(err)
					}
					if !os.SameFile(before[path], st) {
						t.Fatal("rebuilt active call shard", path)
					}
				}
				if got := cache[claude].Models.of("m"); got != (Tokens{Input: 100, Output: i + 2}) {
					t.Fatal(got)
				}
				if got := cache[codex].Models.of("gpt-6-astra"); got != (Tokens{Input: 100 * (i + 2), Output: i + 2}) {
					t.Fatal(got)
				}
			}
			for _, path := range historical {
				if cache[path].Claude != nil || callCache[path] != nil && callCache[path].Claude != nil || callContinuations[path].state.Claude != nil {
					t.Fatal("cold-only historical source retained a window")
				}
			}
		})
	}
}

func TestIdleWindowsYieldToRecentWork(t *testing.T) {
	now := time.Now()
	var candidates []revisionCandidate
	for i := 0; i < revisionFiles; i++ {
		candidates = append(candidates, revisionCandidate{path: fmt.Sprint("old-", i), mod: now.Add(-48 * time.Hour).UnixNano(), size: 13 << 20, weight: claudeRevisionEntries, bounded: true})
	}
	// These old windows were previously admitted while live. Age alone should
	// not drop them, but new work of either format has priority under pressure.
	if len(retainedRevisions(append([]revisionCandidate(nil), candidates...), now)) != revisionFiles {
		t.Fatal("idle windows lost spare slots")
	}
	candidates = append(candidates, revisionCandidate{path: "active-claude", mod: now.UnixNano(), size: 11 << 20, weight: claudeRevisionEntries, bounded: true}, revisionCandidate{path: "active-codex", mod: now.UnixNano(), size: 128, weight: 4})
	keep := retainedRevisions(candidates, now)
	if !keep["active-claude"] || !keep["active-codex"] || len(keep) > revisionFiles {
		t.Fatal("old windows displaced current work", keep)
	}
	// A ten-minute pause is still in the working set. Cost outranks the eight
	// newer tiny transcripts, so this does not restore the five-minute cliff.
	candidates = []revisionCandidate{{path: "large", mod: now.Add(-10 * time.Minute).UnixNano(), size: 1 << 30, weight: claudeRevisionEntries, bounded: true}}
	for i := 0; i < revisionFiles; i++ {
		candidates = append(candidates, revisionCandidate{path: fmt.Sprint("small-", i), mod: now.UnixNano(), size: 1024, weight: 2, bounded: true})
	}
	if !retainedRevisions(candidates, now)["large"] {
		t.Fatal("short pause lost expensive window")
	}
}
