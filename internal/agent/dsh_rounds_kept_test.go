package agent

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// A start's first dsh round read every profile through the yaml emitter,
// ~43 MB of a real account's first minute, to find what the last magpie's
// round had found: nothing to write (#2278). It goes by the last one's
// record now, and does the round when a file, the models or magpie's
// version is not what that record went by.
func TestDshFirstRoundGoesByTheLastMagpies(t *testing.T) {
	was := gateway.Version
	t.Cleanup(func() { gateway.Version = was })
	gateway.Version = "0.1.9999-test"
	_, web := dshSettled(t)

	start := func() *dshRounds {
		t.Helper()
		r := &dshRounds{}
		r.once()
		if !r.done {
			t.Fatal("a start's round with nothing to write is not kept")
		}
		return r
	}
	worked := func(f func()) int64 {
		n := dshRoundsWorked.Load()
		f()
		return dshRoundsWorked.Load() - n
	}
	if n := worked(func() { start() }); n != 0 {
		t.Fatalf("a start with nothing changed since the last magpie read and wrote dsh's files %d times", n)
	}

	b, _ := os.ReadFile(web)
	if err := os.WriteFile(web, append(b, "# the user's\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := worked(func() { start() }); n != 1 {
		t.Fatalf("a start after the patch list changed did %d rounds, want 1", n)
	}
	if n := worked(func() { start() }); n != 0 {
		t.Fatalf("the round after a change is not what the next start goes by (%d rounds)", n)
	}

	gateway.Version = "0.1.10000-test"
	if n := worked(func() { start() }); n != 1 {
		t.Fatalf("another magpie version went by this one's record (%d rounds)", n)
	}
	gateway.Version = "dev"
	if n := worked(func() { start() }); n != 1 {
		t.Fatalf("a dev build went by a record (%d rounds)", n)
	}
}
