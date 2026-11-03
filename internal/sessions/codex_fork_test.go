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

// A fork that went on working: its calls after the copies are its own,
// even one whose numbers are those of a call its parent made after the
// fork (both given the same next prompt over the same history). Only the
// copied lines at the head of the fork are the parent's (Sky,
// skyhuang1994: forks that went on working lost their own usage on
// ZCode; this is Codex's side of it).
func TestCodexForkThatWentOnWorkingKeepsItsOwnCalls(t *testing.T) {
	d := setupCalls(t)
	const parent, fork = "019d186b-a100-7aaa-8bbb-cccdddeeefff", "019d1f2d-f9c0-7111-8222-333444555666"
	read := func(name string) []string {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	p, c := read("codex-fork-parent.jsonl"), read("codex-fork-child.jsonl")
	// the fork's own first call: the parent's call after the fork, number
	// for number, at the fork's time; then a second call of its own
	same := strings.Replace(p[12], `"timestamp":"2026-03-24T10:00:20.000Z"`, `"timestamp":"2026-03-24T09:30:12.000Z"`, 1)
	if same == p[12] || !strings.Contains(c[14], `"type":"token_count"`) {
		t.Fatal("the fixtures changed")
	}
	c[14] = same
	next := strings.NewReplacer(
		`"timestamp":"2026-03-24T10:00:20.000Z"`, `"timestamp":"2026-03-24T09:40:00.000Z"`,
		`{"input_tokens":178000,"cached_input_tokens":134912,"output_tokens":3000,"reasoning_output_tokens":600,"total_tokens":181000}`,
		`{"input_tokens":228000,"cached_input_tokens":174912,"output_tokens":3400,"reasoning_output_tokens":650,"total_tokens":231400}`,
		`{"input_tokens":46928,"cached_input_tokens":46080,"output_tokens":600,"reasoning_output_tokens":88,"total_tokens":47528}`,
		`{"input_tokens":50000,"cached_input_tokens":40000,"output_tokens":400,"reasoning_output_tokens":50,"total_tokens":50400}`,
	).Replace(p[12])
	c = append(c, next)
	writeLines(t, filepath.Join(d.codex, "sessions", "2026", "03", "23", "rollout-2026-03-23T10-00-00-"+parent+".jsonl"), strings.Join(p, "\n"))
	writeLines(t, filepath.Join(d.codex, "sessions", "2026", "03", "24", "rollout-2026-03-24T17-30-00-"+fork+".jsonl"), strings.Join(c, "\n"))

	call := func(in, cached, out int) Tokens { return Tokens{Input: in - cached, CacheRead: cached, Output: out} }
	wantFork := call(46928, 46080, 600)
	wantFork.add(call(50000, 40000, 400))
	var onFork, all, want Tokens
	n := 0
	for _, x := range Calls(time.Time{}) {
		all.add(x.Tokens)
		if strings.Contains(x.File, fork) {
			onFork.add(x.Tokens)
			n++
		}
	}
	if n != 2 || onFork != wantFork {
		t.Fatalf("the fork's calls: %d %+v, want its 2 own %+v", n, onFork, wantFork)
	}
	for _, v := range []Tokens{call(41200, 3072, 812), call(43680, 41088, 688), call(46192, 44672, 900), call(46928, 46080, 600), wantFork} {
		want.add(v)
	}
	if all != want {
		t.Fatalf("calls booked %+v, want %+v", all, want)
	}
	var listed Tokens
	for _, s := range List(0) {
		listed.add(s.Tokens)
	}
	if listed != want {
		t.Fatalf("sessions booked %+v, want %+v", listed, want)
	}
}
