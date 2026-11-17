package settings

import (
	"testing"
	"time"
)

// Settings' Classifier timeout (#2934: a Kiro subscription's haiku needed
// more than 8 seconds) is kept from 1 to 60 seconds, and 0, which is what
// a settings.json without it reads as, is the default: 15, room for a
// classifier that reasons (ESern29751 on X), where it was 8.
func TestClassifyTimeoutKeptAndBounded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if w := Load().ClassifyWait(); w != 15*time.Second {
		t.Fatalf("unset: %v", w)
	}
	for _, n := range []int{1, 30, 60} {
		if err := Save(Settings{ClassifyTimeout: n}); err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		if s := Load(); s.ClassifyTimeout != n || s.ClassifyWait() != time.Duration(n)*time.Second {
			t.Fatalf("%d read back as %d, %v", n, s.ClassifyTimeout, s.ClassifyWait())
		}
	}
	for _, n := range []int{-1, 61, 600} {
		if Save(Settings{ClassifyTimeout: n}) == nil {
			t.Fatalf("%d seconds accepted", n)
		}
	}
	if Load().ClassifyTimeout != 60 {
		t.Fatal("a refused value overwrote the saved one")
	}
	// a settings.json written by hand past the bounds still waits within them
	if w := (Settings{ClassifyTimeout: 600}).ClassifyWait(); w != time.Minute {
		t.Fatalf("600 waits %v", w)
	}
	if w := (Settings{ClassifyTimeout: -3}).ClassifyWait(); w != 15*time.Second {
		t.Fatalf("-3 waits %v", w)
	}
}
