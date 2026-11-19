//go:build windows && !nogui

package gui

import (
	"log"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/proc"
)

// On battery or with Windows Energy Saver on, magpie and its background
// plugin host (magpie-bun) run in Windows' efficiency mode (EcoQoS) while
// none of magpie's GUI windows is in the foreground, and leave it as soon
// as AC power returns (without Energy Saver) or a GUI window comes to the
// front. Both power and foreground transitions arrive on Wails' window
// procedure (appWndProc), so nothing is polled.

const (
	wmSize        = 0x0005
	wmActivate    = 0x0006
	wmShowWindow  = 0x0018
	wmActivateApp = 0x001C
	gaRoot        = 2 // GetAncestor's GA_ROOT
)

// Power setting GUIDs (winnt.h): AC/DC power source and Energy Saver.
var (
	guidACDCPowerSource = windows.GUID{
		Data1: 0x5d3e9a59, Data2: 0xe9d5, Data3: 0x4b00,
		Data4: [8]byte{0xa6, 0xbd, 0xff, 0x34, 0xff, 0x51, 0x65, 0x48},
	}
	guidPowerSavingStatus = windows.GUID{
		Data1: 0xe00958c0, Data2: 0xc213, Data3: 0x4ace,
		Data4: [8]byte{0xac, 0x77, 0xfe, 0xcc, 0xed, 0x2e, 0xee, 0xa5},
	}
)

var (
	procGetForegroundWindow                = windows.NewLazySystemDLL("user32.dll").NewProc("GetForegroundWindow")
	procWindowIconic                       = windows.NewLazySystemDLL("user32.dll").NewProc("IsIconic")
	procUnregisterPowerSettingNotification = windows.NewLazySystemDLL("user32.dll").NewProc("UnregisterPowerSettingNotification")
)

var (
	powerNotifyMu      sync.Mutex
	powerNotifyHandles []uintptr
	efficiencyCheck    atomic.Pointer[func()]
	activeEfficiency   atomic.Pointer[efficiencyCoordinator]
)

// efficiencyMsg reports whether msg may reflect a change in power or in
// whether one of magpie's windows is in the foreground.
func efficiencyMsg(msg uint32) bool {
	switch msg {
	case wmPowerBroadcast, wmActivateApp, wmActivate, wmShowWindow, wmSize:
		return true
	}
	return false
}

// windowInForeground reports whether fg (or its root window, when a child
// control such as WebView2 holds focus) is a visible, unminimised window of
// ownPID. Wails' hidden main-thread and tray message windows are excluded
// by IsWindowVisible, and minimised windows by IsIconic.
func windowInForeground(fg uintptr, ownPID uint32) bool {
	if fg == 0 {
		return false
	}
	if root, _, _ := procGetAncestor.Call(fg, gaRoot); root != 0 {
		fg = root
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(windows.HWND(fg), &pid); err != nil || pid != ownPID {
		return false
	}
	iconic, _, _ := procWindowIconic.Call(fg)
	return w32.IsWindowVisible(fg) && iconic == 0
}

func ownWindowInForeground() bool {
	fg, _, _ := procGetForegroundWindow.Call()
	return windowInForeground(fg, windows.GetCurrentProcessId())
}

func watchEfficiencyPower() {
	hwnd := ownMainThreadWindow()
	if hwnd == 0 {
		return
	}
	powerNotifyMu.Lock()
	defer powerNotifyMu.Unlock()
	if len(powerNotifyHandles) > 0 {
		return
	}
	for _, g := range []*windows.GUID{&guidACDCPowerSource, &guidPowerSavingStatus} {
		h, _, err := procRegisterPowerSettingNotification.Call(hwnd, uintptr(unsafe.Pointer(g)), deviceNotifyWindowHndl)
		if h == 0 {
			log.Printf("efficiency mode: power notification unavailable: %v", err)
			continue
		}
		powerNotifyHandles = append(powerNotifyHandles, h)
	}
}

func unwatchEfficiencyPower() {
	powerNotifyMu.Lock()
	hs := powerNotifyHandles
	powerNotifyHandles = nil
	powerNotifyMu.Unlock()
	for _, h := range hs {
		procUnregisterPowerSettingNotification.Call(h)
	}
}

func (h *host) startEfficiencyMode() {
	coord := newEfficiencyCoordinator(proc.PowerNow, ownWindowInForeground, func(on bool) {
		if err := proc.SetEfficiencyMode(on); err != nil {
			log.Printf("efficiency mode: %v", err)
			return
		}
		plugin.SetEfficiencyMode(on)
		if on {
			log.Print("efficiency mode: on (background on battery or Energy Saver)")
		} else {
			log.Print("efficiency mode: off")
		}
	})
	activeEfficiency.Store(coord)
	check := func() { application.InvokeAsync(coord.update) }
	efficiencyCheck.Store(&check)
	h.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		application.InvokeAsync(func() {
			watchEfficiencyPower()
			coord.update()
		})
	})
}

func stopEfficiencyMode() {
	efficiencyCheck.Store(nil)
	unwatchEfficiencyPower()
	if coord := activeEfficiency.Swap(nil); coord != nil {
		coord.stop()
	}
}
