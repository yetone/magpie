package gui

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// The feed is asked by itself as often as Settings says, every six hours
// when it says nothing, and never with automatic updates off (#472).
func TestUpdateDue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	s := settings.Load()
	if s.NoAutoUpdate || s.UpdateEvery != 360 {
		t.Fatalf("default: auto off %v, every %d", s.NoAutoUpdate, s.UpdateEvery)
	}
	for _, c := range []struct {
		off   bool
		every int
		ago   time.Duration
		due   bool
	}{
		{false, 0, 0, false},
		{false, 360, 5*time.Hour + 59*time.Minute, false},
		{false, 360, 6 * time.Hour, true},
		{false, 30, 29 * time.Minute, false},
		{false, 30, 30 * time.Minute, true},
		{false, 1440, 7 * time.Hour, false},
		{false, 1440, 24 * time.Hour, true},
		{true, 30, 48 * time.Hour, false},
	} {
		s.NoAutoUpdate, s.UpdateEvery = c.off, c.every
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		if got := updateDue(settings.Load(), now.Add(-c.ago), now); got != c.due {
			t.Errorf("off %v, every %d, %v ago: due %v, want %v", c.off, c.every, c.ago, got, c.due)
		}
	}
	// never asked: due at start-up, unless turned off
	s.NoAutoUpdate, s.UpdateEvery = false, 1440
	if !updateDue(s, time.Time{}, now) {
		t.Error("never asked, on: not due")
	}
	s.NoAutoUpdate = true
	if updateDue(s, time.Time{}, now) {
		t.Error("never asked, off: due")
	}
	// only the presets are kept
	s.NoAutoUpdate, s.UpdateEvery = false, 120
	if err := settings.Save(s); err == nil {
		t.Error("every 120 minutes saved")
	}
}

// A check, asked for or by the clock, is when the next one counts from.
func TestUpdateAskedAt(t *testing.T) {
	u := &updater{}
	before := time.Now()
	if !u.begin() {
		t.Fatal("not begun")
	}
	if u.asked.Before(before) {
		t.Fatalf("asked at %v, before %v", u.asked, before)
	}
}
