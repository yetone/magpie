package main

import (
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// magpie update auto turns the app's own update checks off and on and sets
// how often they run, from the presets only (#472).
func TestUpdateAutoCmd(t *testing.T) {
	groupsHome(t)
	if err := updateCmd([]string{"update", "auto"}); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.NoAutoUpdate || s.UpdateEvery != 360 {
		t.Fatalf("default: off %v, every %d", s.NoAutoUpdate, s.UpdateEvery)
	}
	if err := updateCmd([]string{"update", "auto", "off", "30m"}); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); !s.NoAutoUpdate || s.UpdateEvery != 30 {
		t.Fatalf("off 30m: off %v, every %d", s.NoAutoUpdate, s.UpdateEvery)
	}
	if err := updateCmd([]string{"update", "auto", "on"}); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.NoAutoUpdate || s.UpdateEvery != 30 {
		t.Fatalf("on: off %v, every %d", s.NoAutoUpdate, s.UpdateEvery)
	}
	for _, a := range []string{"1h", "24h", "6h"} {
		if err := updateCmd([]string{"update", "auto", a}); err != nil {
			t.Fatal(err)
		}
		if got := settings.Load().UpdateEvery; got != updateEveries[a] {
			t.Fatalf("%s: every %d", a, got)
		}
	}
	if err := updateCmd([]string{"update", "auto", "2h"}); err == nil {
		t.Fatal("2h taken")
	}
	if got := settings.Load().UpdateEvery; got != 360 {
		t.Fatalf("2h refused, yet every %d", got)
	}
}
