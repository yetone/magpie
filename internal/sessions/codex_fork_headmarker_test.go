package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A copied head can hold a turn of codex-rs's own naming, "rollout-<n>",
// that the parent's file doesn't (a parent's turn ids are uuids). It is not
// the fork's work: with it ending the copies, every copied checkpoint after
// it was booked as the fork's own call (#5090, SKYhuangjing: one parent's 29
// subagents put 1.1B of the parent's tokens on their day a second time).
func TestCodexForkHeadMarkerDoesNotEndTheCopies(t *testing.T) {
	d := setupCalls(t)
	const parent, fork = "019d186b-a100-7aaa-8bbb-cccdddeeefff", "019d1f2d-f9c0-7111-8222-333444555666"
	place := func(fixture, day, stamp, id string) {
		b, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		writeLines(t, filepath.Join(d.codex, "sessions", "2026", "03", day, "rollout-2026-03-"+day+"T"+stamp+"-"+id+".jsonl"), strings.TrimSpace(string(b)))
	}
	place("codex-fork-parent.jsonl", "23", "10-00-00", parent)
	place("codex-fork-child-headmarker.jsonl", "24", "17-30-00", fork)

	var onFork Tokens
	var forkCalls int
	for _, c := range Calls(time.Time{}) {
		if strings.Contains(c.File, fork) {
			onFork.add(c.Tokens)
			forkCalls++
		}
	}
	want := Tokens{Input: 47328 - 44672, CacheRead: 44672, Output: 550}
	if forkCalls != 1 || onFork != want {
		t.Fatalf("the fork booked %d calls %+v, want its own one call %+v (the marker must not end the copies)", forkCalls, onFork, want)
	}
}
