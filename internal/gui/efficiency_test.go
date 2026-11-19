package gui

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/proc"
)

func TestEfficiencyCoordinatorAppliesTransitionsOnce(t *testing.T) {
	var (
		power   proc.PowerState
		inFront bool
		applied []bool
	)
	coord := newEfficiencyCoordinator(
		func() proc.PowerState { return power },
		func() bool { return inFront },
		func(on bool) { applied = append(applied, on) },
	)

	// AC at launch does not apply a redundant false transition.
	coord.update()
	if len(applied) != 0 || coord.active() {
		t.Fatalf("AC start: applied = %v, active = %v", applied, coord.active())
	}

	// Unplugged in background turns efficiency mode on once.
	power = proc.PowerState{OnBattery: true}
	coord.update()
	coord.update()
	if !slices.Equal(applied, []bool{true}) || !coord.active() {
		t.Fatalf("battery background: applied = %v, active = %v", applied, coord.active())
	}

	// Energy Saver turning on while already on battery is no change.
	power = proc.PowerState{OnBattery: true, EnergySaver: true}
	coord.update()
	if !slices.Equal(applied, []bool{true}) {
		t.Fatalf("battery + saver: applied = %v", applied)
	}

	// A GUI window coming to the foreground lifts efficiency mode.
	inFront = true
	coord.update()
	if !slices.Equal(applied, []bool{true, false}) || coord.active() {
		t.Fatalf("window in foreground: applied = %v, active = %v", applied, coord.active())
	}

	// Leaving the foreground puts efficiency mode back on.
	inFront = false
	coord.update()
	if !slices.Equal(applied, []bool{true, false, true}) || !coord.active() {
		t.Fatalf("back to tray: applied = %v, active = %v", applied, coord.active())
	}

	// Plugging into AC while Energy Saver stays on keeps efficiency mode active.
	power = proc.PowerState{OnBattery: false, EnergySaver: true}
	coord.update()
	if !slices.Equal(applied, []bool{true, false, true}) || !coord.active() {
		t.Fatalf("AC with Energy Saver: applied = %v, active = %v", applied, coord.active())
	}

	// Turning Energy Saver off on AC lifts efficiency mode.
	power = proc.PowerState{}
	coord.update()
	if !slices.Equal(applied, []bool{true, false, true, false}) || coord.active() {
		t.Fatalf("AC without Energy Saver: applied = %v, active = %v", applied, coord.active())
	}

	// Stopping while active lifts efficiency mode once and ignores later updates.
	power = proc.PowerState{OnBattery: true}
	coord.update()
	coord.stop()
	coord.stop()
	coord.update()
	if !slices.Equal(applied, []bool{true, false, true, false, true, false}) || coord.active() {
		t.Fatalf("after stop: applied = %v, active = %v", applied, coord.active())
	}
}
