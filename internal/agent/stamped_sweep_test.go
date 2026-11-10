package agent

import (
	"testing"
)

// A kept list past magpieModelsFor is built again on its next ask, so the
// sweep lets it go; one still fresh stays, and the sweep looks again for it.
func TestKeptListsPastTheirMinuteLetGo(t *testing.T) {
	var s stampedLists[string]
	builds := 0
	build := func() []string { builds++; return []string{"a", "b"} }
	s.shared("old", build)
	s.shared("fresh", build)
	s.Lock()
	if !s.sweeping {
		s.Unlock()
		t.Fatal("no sweep armed for the lists kept")
	}
	b := s.m["old"]
	b.at = b.at.Add(-2 * magpieModelsFor)
	s.m["old"] = b
	s.Unlock()

	s.sweep()
	s.Lock()
	_, old := s.m["old"]
	_, fresh := s.m["fresh"]
	sweeping := s.sweeping
	s.Unlock()
	if old || !fresh || !sweeping {
		t.Fatalf("after a sweep: old kept %v, fresh kept %v, sweep armed %v; want false, true, true", old, fresh, sweeping)
	}

	s.Lock()
	b = s.m["fresh"]
	b.at = b.at.Add(-2 * magpieModelsFor)
	s.m["fresh"] = b
	s.Unlock()
	s.sweep()
	s.Lock()
	n, sweeping := len(s.m), s.sweeping
	s.Unlock()
	if n != 0 || sweeping {
		t.Fatalf("after the last went: %d kept, sweep armed %v", n, sweeping)
	}

	n0 := builds
	s.shared("old", build)
	if builds != n0+1 {
		t.Fatal("a list let go wasn't built again on its next ask")
	}
	s.Lock()
	sweeping = s.sweeping
	s.Unlock()
	if !sweeping {
		t.Fatal("no sweep armed again for a list kept after the last one went")
	}
}
