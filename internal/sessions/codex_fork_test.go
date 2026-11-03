package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fork's rollout starts with every line of its parent's, token_count
// events included, stamped when the fork wrote them (codex-rs 0.117's
// InitialHistory::Forked). The fixtures are that shape: a gpt-5.4 parent of
// 2026-03-23 with three calls (and one more after the fork), and a
// spawn_agent fork of it on 03-24 that made one call of its own. The copies
// are the parent's calls, which the parent's rollout counts; read as the
// fork's, they put the parent's whole history on 03-24 again (Sky).
func TestCodexForkCountsOnlyItsOwnCalls(t *testing.T) {
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
	place("codex-fork-child.jsonl", "24", "17-30-00", fork)

	call := func(in, cached, out, reasoning int) Tokens {
		return Tokens{Input: in - cached, CacheRead: cached, Output: out}
	}
	own := map[string][]Tokens{
		parent: {call(41200, 3072, 812, 256), call(43680, 41088, 688, 144), call(46192, 44672, 900, 112), call(46928, 46080, 600, 88)},
		fork:   {call(47328, 44672, 550, 48)},
	}
	var want Tokens
	for _, ts := range own {
		for _, v := range ts {
			want.add(v)
		}
	}
	got := map[string]int{}
	var total Tokens
	var onFork Tokens
	for _, c := range Calls(time.Time{}) {
		got[c.File]++
		total.add(c.Tokens)
		if strings.Contains(c.File, fork) {
			onFork.add(c.Tokens)
		}
	}
	if total != want {
		t.Fatalf("calls booked %+v, want %+v (calls per file %v)", total, want, got)
	}
	if onFork != own[fork][0] {
		t.Fatalf("the fork's calls are %+v, want its own one call %+v", onFork, own[fork][0])
	}
	var listed Tokens
	for _, s := range List(0) {
		listed.add(s.Tokens)
	}
	if listed != want {
		t.Fatalf("sessions booked %+v, want %+v", listed, want)
	}
}

// With the parent's rollout gone, its calls are counted once, in the fork:
// they were made, and nothing else counts them now.
func TestCodexForkWithoutItsParentCountsTheCopies(t *testing.T) {
	d := setupCalls(t)
	b, err := os.ReadFile("testdata/codex-fork-child.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	writeLines(t, filepath.Join(d.codex, "sessions", "2026", "03", "24", "rollout-2026-03-24T17-30-00-019d1f2d-f9c0-7111-8222-333444555666.jsonl"), strings.TrimSpace(string(b)))
	if n := len(Calls(time.Time{})); n != 4 {
		t.Fatalf("%d calls, want the 3 copied and the fork's own", n)
	}
}

func TestUUIDV7Time(t *testing.T) {
	at, ok := uuidV7Time("019d186b-a100-7aaa-8bbb-cccdddeeefff")
	if !ok || !at.Equal(time.Date(2026, 3, 23, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("%v %v", at, ok)
	}
	if _, ok := uuidV7Time("4f2c1a7e-0000-4000-8000-000000000000"); ok {
		t.Fatal("a v4 id has no time")
	}
}
