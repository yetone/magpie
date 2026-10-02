package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// The ChatGPT account running out of its allowance, or getting it back, is
// told to the agents' sync (#540), which makes magpie Codex's provider while
// the account is out; the same state again is not.
func TestNoteCodexUsedUp(t *testing.T) {
	told := 0
	was := catalog.Changed
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = was; codexWasUsedUp.Store(false) })
	codexWasUsedUp.Store(false)
	for i, c := range []struct {
		now  bool
		told int
	}{{false, 0}, {true, 1}, {true, 1}, {false, 2}, {false, 2}} {
		noteCodexUsedUp(c.now)
		if told != c.told {
			t.Fatalf("step %d (used up %v): told %d times, want %d", i, c.now, told, c.told)
		}
	}
}
