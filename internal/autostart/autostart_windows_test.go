package autostart

import (
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Turning Open at login off has to say so when it cannot reach the Run key.
// A Run key whose ACL refuses SET_VALUE is still readable, so nothing else
// changed: the magpie value stays where it was and Windows starts it at the
// next sign-in, which is the opposite of what was asked for. No Run key at
// all is nothing to turn off, and is not an error.
func TestDisableReportsAKeyItCannotReach(t *testing.T) {
	refuse := func() {
		openRunKey = func(registry.Key, string, uint32) (registry.Key, error) {
			return 0, windows.ERROR_ACCESS_DENIED
		}
	}
	absent := func() {
		openRunKey = func(registry.Key, string, uint32) (registry.Key, error) {
			return 0, registry.ErrNotExist
		}
	}
	restore := openRunKey
	t.Cleanup(func() { openRunKey = restore })

	refuse()
	if err := Set(false); err == nil {
		t.Fatal("a Run key that refuses SET_VALUE was reported as turned off")
	}
	absent()
	if err := Set(false); err != nil {
		t.Fatalf("no Run key is nothing to turn off, not an error: %v", err)
	}
}

// A Run key that cannot be read is not read as off: Windows may still be
// starting magpie, and the switch says so when it can't turn it off.
func TestEnabledUnreadableKeyIsNotOff(t *testing.T) {
	restore := openRunKey
	t.Cleanup(func() { openRunKey = restore })
	openRunKey = func(registry.Key, string, uint32) (registry.Key, error) {
		return 0, windows.ERROR_ACCESS_DENIED
	}
	if !Enabled() {
		t.Fatal("a Run key that cannot be read reads as off")
	}
	openRunKey = func(registry.Key, string, uint32) (registry.Key, error) {
		return 0, registry.ErrNotExist
	}
	if Enabled() {
		t.Fatal("no Run key reads as on")
	}
}

// Turning Open at login on has to say so when it cannot clear Task Manager's
// Startup apps marker. The Run value is written either way, so the switch
// would read as on, but the marker says Windows is to leave magpie switched
// off: Set(true) answering success there is the same lie Set(false) used to
// tell. No marker at all is nothing to clear, and is not an error.
func TestClearApprovedMarkerReportsOneItCannotReach(t *testing.T) {
	refuse := func() {
		openApprovedKey = func(registry.Key, string, uint32) (registry.Key, error) {
			return 0, windows.ERROR_ACCESS_DENIED
		}
	}
	absent := func() {
		openApprovedKey = func(registry.Key, string, uint32) (registry.Key, error) {
			return 0, registry.ErrNotExist
		}
	}
	restore := openApprovedKey
	t.Cleanup(func() { openApprovedKey = restore })

	refuse()
	if err := clearApprovedMarker(); err == nil {
		t.Fatal("a Startup apps marker that cannot be cleared was reported as on")
	}
	absent()
	if err := clearApprovedMarker(); err != nil {
		t.Fatalf("no marker is nothing to clear, not an error: %v", err)
	}
}

// A marker that cannot be read is not read as switched off. No marker at all
// is not either: Startup apps writes this key the first time it lists an
// entry, so a machine whose user has never opened it has no key while every
// Run value it has is on, and only a marker that says so is switched off.
func TestSwitchedOffUnreadableOrAbsentMarkerIsNotOff(t *testing.T) {
	restore := openApprovedKey
	t.Cleanup(func() { openApprovedKey = restore })
	openApprovedKey = func(registry.Key, string, uint32) (registry.Key, error) {
		return 0, windows.ERROR_ACCESS_DENIED
	}
	if switchedOff() {
		t.Fatal("a Startup apps marker that cannot be read reads as switched off")
	}
	openApprovedKey = func(registry.Key, string, uint32) (registry.Key, error) {
		return 0, registry.ErrNotExist
	}
	if switchedOff() {
		t.Fatal("no marker reads as switched off")
	}
}

// The machine that has the Run value and no Startup apps marker at all is
// the ordinary one: Startup apps writes the marker key the first time it
// lists an entry, so a user who never opened it has no key, and Windows
// still starts every Run value it has. Reading that as switched off told
// Settings "login: off" for a magpie that does start.
func TestEnabledWithNoMarkerIsOn(t *testing.T) {
	restoreRun, restoreApproved := openRunKey, openApprovedKey
	restoreRead := readRunValue
	t.Cleanup(func() { openRunKey, openApprovedKey, readRunValue = restoreRun, restoreApproved, restoreRead })
	openRunKey = func(registry.Key, string, uint32) (registry.Key, error) {
		return 0, nil
	}
	readRunValue = func(registry.Key) (string, error) { return "C:\\magpie.exe", nil }
	openApprovedKey = func(registry.Key, string, uint32) (registry.Key, error) {
		return 0, registry.ErrNotExist
	}
	if !Enabled() {
		t.Fatal("a Run value with no Startup apps marker reads as off")
	}
}
