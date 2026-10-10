package autostart

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
	name   = "magpie"
	// where Task Manager's Startup apps keeps an entry switched off: an odd
	// first byte is off
	approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// record is where the system keeps it, as a file: none, it's the registry's
func record() string { return "" }

// openRunKey is how the Run key is reached. A test hands back a key the user's
// ACL refuses, which no key this machine's own user has.
var openRunKey = registry.OpenKey

// openApprovedKey is how Task Manager's Startup apps marker is reached, for the
// same reason: read to answer switchedOff, opened for writing to clear it.
var openApprovedKey = registry.OpenKey

// readRunValue is how the magpie Run value is read off a key that opened. A
// test has no Run value to hand back, so it stands in for this too.
var readRunValue = func(k registry.Key) (string, error) {
	s, _, err := k.GetStringValue(name)
	return s, err
}

func enabled() bool {
	k, err := openRunKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		// No Run key at all is off. A Run key that can't be read is not a
		// no: Windows may still be starting magpie, and Set is what says so
		// when it can't reach the key to turn that off.
		return !errors.Is(err, registry.ErrNotExist)
	}
	defer k.Close()
	if _, err = readRunValue(k); err != nil {
		return !errors.Is(err, registry.ErrNotExist)
	}
	return !switchedOff()
}

// switchedOff: turned off in Task Manager, which leaves the Run value be. A
// marker that isn't there, or that can't be read, is not switched off: only a
// marker that says so is, since the Run value is what starts magpie.
func switchedOff() bool {
	k, err := openApprovedKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE)
	if err != nil {
		// No marker at all is not switched off, and neither is one that
		// can't be read: Windows writes this key the first time Startup
		// apps lists an entry, so a machine whose user has never opened it
		// has no key at all while every Run value it has is on. The Run
		// value is what starts magpie, so reporting switched off here
		// would hide a magpie that does start.
		return false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(name)
	return err == nil && len(b) > 0 && b[0]&1 == 1
}

// clearApprovedMarker takes Task Manager's Startup apps marker off the Run
// value, so Windows starts magpie again at sign-in. A marker that can't be
// reached or deleted leaves the Run value switched off, so saying nothing
// about it would let Set(true) report on while Windows starts nothing.
func clearApprovedMarker() error {
	a, err := openApprovedKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if err != nil {
		if !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil // no marker is nothing to clear
	}
	defer a.Close()
	if err := a.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// the user's own Run value, which Windows starts at sign-in (and which
// Task Manager's Startup apps can switch off)
func enable(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue(name, `"`+exe+`" `+Arg); err != nil {
		return err
	}
	// switched on here after Task Manager switched it off: on again there
	return clearApprovedMarker()
}

func disable() error {
	k, err := openRunKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	if err != nil {
		return nil // no Run key is nothing to turn off
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// refresh: nothing an older magpie wrote here needs writing again
func refresh() error { return nil }
