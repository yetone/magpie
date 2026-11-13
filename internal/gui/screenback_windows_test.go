//go:build windows && !nogui

package gui

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The screen coming back, and only that, draws magpie's windows again: an
// unlock, or the display on after it was off. Windows says the display is
// on when the watch is registered; that is no change.
func TestScreenBackFrom(t *testing.T) {
	displayOff.Store(false)
	t.Cleanup(func() { displayOff.Store(false) })
	display := func(state uint32, guid windows.GUID) string {
		s := powerBroadcastSetting{PowerSetting: guid, DataLength: 4, Data: state}
		return screenBackFrom(wmPowerBroadcast, pbtPowerSettingChange, uintptr(unsafe.Pointer(&s)))
	}
	other := windows.GUID{Data1: 1}
	for _, c := range []struct {
		name, want string
		got        func() string
	}{
		{"unlocked", "the session was unlocked", func() string { return screenBackFrom(wmWTSSessionChange, wtsSessionUnlock, 0) }},
		{"locked", "", func() string { return screenBackFrom(wmWTSSessionChange, 0x7, 0) }},
		{"on when registered", "", func() string { return display(1, guidConsoleDisplayState) }},
		{"off", "", func() string { return display(0, guidConsoleDisplayState) }},
		{"another setting's on", "", func() string { return display(1, other) }},
		{"dimmed", "", func() string { return display(2, guidConsoleDisplayState) }},
		{"on after off", "the display came on", func() string { return display(1, guidConsoleDisplayState) }},
		{"on again", "", func() string { return display(1, guidConsoleDisplayState) }},
		{"another power event", "", func() string { return screenBackFrom(wmPowerBroadcast, 0x7, 0) }},
	} {
		if got := c.got(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The app's interceptor hands an unlock on and still lets the screens'
// own handling see every message.
func TestAppWndProcSaysTheScreenIsBack(t *testing.T) {
	var said []string
	back := func(why string) { said = append(said, why) }
	was := screenBack.Load()
	screenBack.Store(&back)
	t.Cleanup(func() { screenBack.Store(was) })
	appWndProc(0, wmWTSSessionChange, wtsSessionUnlock, 0)
	appWndProc(0, wmWTSSessionChange, 0x7, 0)
	if len(said) != 1 || said[0] != "the session was unlocked" {
		t.Fatalf("said %q, want one unlock", said)
	}
}
