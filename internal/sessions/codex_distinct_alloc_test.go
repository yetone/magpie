package sessions

import (
	"fmt"
	"testing"
)

// distinctCodexFiles runs on every Sessions poll over every rollout; its
// set of names is kept from call to call, not made again for each (#2278).
func TestDistinctCodexFilesKeepsItsSet(t *testing.T) {
	files := make([]file, 4000)
	for i := range files {
		files[i] = file{agent: "codex", path: fmt.Sprintf("/c/sessions/2026/11/10/rollout-%05d.jsonl", i)}
	}
	work := make([]file, len(files))
	run := func() {
		copy(work, files)
		if got := distinctCodexFiles(work); len(got) != len(files) {
			t.Fatalf("kept %d of %d distinct rollouts", len(got), len(files))
		}
	}
	run()
	if n := testing.AllocsPerRun(10, run); n > 4 {
		t.Errorf("a call over %d rollouts: %.0f allocations, want a few", len(files), n)
	}
}
