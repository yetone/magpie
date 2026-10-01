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

func enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	if _, _, err = k.GetStringValue(name); err != nil {
		return false
	}
	return !switchedOff()
}

// switchedOff: turned off in Task Manager, which leaves the Run value be
func switchedOff() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(name)
	return err == nil && len(b) > 0 && b[0]&1 == 1
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
	if a, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE); err == nil {
		a.DeleteValue(name)
		a.Close()
	}
	return nil
}

func disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// refresh: nothing an older magpie wrote here needs writing again
func refresh() error { return nil }
