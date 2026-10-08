package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	ompMain  = "019a0000-0000-7000-8000-00000000000a"
	ompFork  = "019a0000-0000-7000-8000-00000000000c"
	ompNamed = "019a0000-0000-7000-8000-00000000000d"
)

// omp's sessions (#303): Pi's kind, with its title line and title changes,
// model_usage entries, and its subagents' and advisor's sessions beside it.
func TestOmp(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	ss := List(0)
	if n := count(ss, "omp"); n != 3 {
		t.Fatalf("want 3 omp sessions (the subagents and advisor in theirs), got %d", n)
	}
	m := find(t, ss, "omp", ompMain)
	if m.Cwd != "/work/omp" || m.Title != "Port the parser to omp" {
		t.Fatalf("omp: %+v", m)
	}
	// the task tool's summed usage not counted again
	for _, c := range []struct {
		model string
		want  Tokens
	}{
		{"claude-opus-5-5", Tokens{110, 55, 1500, 200, 0}},
		{"claude-haiku-5", Tokens{20, 5, 0, 0, 0}},      // a model_usage entry
		{"claude-sonnet-5", Tokens{340, 34, 100, 0, 0}}, // the subagent and its own
		{"gpt-6-astra", Tokens{50, 10, 0, 0, 0}},        // the advisor
	} {
		if got := model(m, c.model); got.Tokens != c.want {
			t.Fatalf("%s: %+v, want %+v", c.model, got, c.want)
		}
	}
	if len(m.Models) != 4 || !model(m, "claude-opus-5-5").Priced {
		t.Fatalf("models %+v", m.Models)
	}
	if !m.Start.Equal(at("2026-09-28T08:00:00Z")) || !m.Last.Equal(at("2026-09-28T08:02:00Z")) {
		t.Fatalf("omp times %s %s", m.Start, m.Last)
	}
	if runtime.GOOS != "windows" {
		if want := "cd '/work/omp' && omp --resume " + ompMain; m.Resume != want {
			t.Fatalf("resume %q", m.Resume)
		}
	}

	// the fork: what it copied counted in the session it came from
	f := find(t, ss, "omp", ompFork)
	if f.Tokens != (Tokens{7, 3, 0, 0, 0}) || f.Title != "Port the parser" {
		t.Fatalf("fork: %+v", f)
	}
	if !f.Start.Equal(at("2026-09-29T09:00:00Z")) || !f.Last.Equal(at("2026-09-29T09:00:20Z")) {
		t.Fatalf("fork times %s %s", f.Start, f.Last)
	}

	// begun from a template: the title it was last given
	if n := find(t, ss, "omp", ompNamed); n.Title != "Tidy the tests" {
		t.Fatalf("named: %+v", n)
	}

	// a profile's sessions, and those in $XDG_DATA_HOME/omp
	from := filepath.Join(home, ".omp", "agent", "sessions", "--work-omp--")
	moves := map[string]string{ompNamed: filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions", "--work-omp--")}
	if runtime.GOOS != "windows" {
		moves[ompFork] = filepath.Join(os.Getenv("XDG_DATA_HOME"), "omp", "sessions", "--work-omp--")
	}
	for id, to := range moves {
		names, _ := filepath.Glob(filepath.Join(from, "*_"+id+".jsonl"))
		if len(names) != 1 {
			t.Fatalf("fixture for %s: %v", id, names)
		}
		os.MkdirAll(to, 0o755)
		if err := os.Rename(names[0], filepath.Join(to, filepath.Base(names[0]))); err != nil {
			t.Fatal(err)
		}
	}
	Reset()
	ss = List(0)
	for id := range moves {
		m := find(t, ss, "omp", id)
		if id == ompNamed && !strings.Contains(m.Resume, "omp --profile work --resume "+id) {
			t.Fatalf("profile resume %q", m.Resume)
		}
	}
}
