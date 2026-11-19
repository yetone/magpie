package proc

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	processPowerThrottling               = 4 // ProcessPowerThrottling (PROCESS_INFORMATION_CLASS)
	processPowerThrottlingCurrentVersion = 1 // PROCESS_POWER_THROTTLING_CURRENT_VERSION
	processPowerThrottlingExecutionSpeed = 1 // PROCESS_POWER_THROTTLING_EXECUTION_SPEED
)

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     int32
	BatteryFullLifeTime int32
}

type processPowerThrottlingState struct {
	Version     uint32
	ControlMask uint32
	StateMask   uint32
}

var (
	powerKernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemPowerStatus  = powerKernel32.NewProc("GetSystemPowerStatus")
	procSetProcessInformation = powerKernel32.NewProc("SetProcessInformation")
	procGetProcessInformation = powerKernel32.NewProc("GetProcessInformation")
)

func powerNow() PowerState {
	var s systemPowerStatus
	r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s)))
	return powerFromWindowsStatus(s.ACLineStatus, s.SystemStatusFlag, r != 0)
}

func setEfficiencyMode(on bool) error {
	return setHandleEfficiencyMode(windows.CurrentProcess(), on)
}

func setHandleEfficiencyMode(h windows.Handle, on bool) error {
	var mask uint32
	if on {
		mask = processPowerThrottlingExecutionSpeed
	}
	st := processPowerThrottlingState{
		Version:     processPowerThrottlingCurrentVersion,
		ControlMask: mask,
		StateMask:   mask,
	}
	if r, _, err := procSetProcessInformation.Call(
		uintptr(h),
		processPowerThrottling,
		uintptr(unsafe.Pointer(&st)),
		unsafe.Sizeof(st),
	); r == 0 {
		return err
	}
	return nil
}

func readHandleEfficiencyMode(h windows.Handle) (controlMask, stateMask uint32, err error) {
	st := processPowerThrottlingState{Version: processPowerThrottlingCurrentVersion}
	if r, _, callErr := procGetProcessInformation.Call(
		uintptr(h),
		processPowerThrottling,
		uintptr(unsafe.Pointer(&st)),
		unsafe.Sizeof(st),
	); r == 0 {
		return 0, 0, callErr
	}
	return st.ControlMask, st.StateMask, nil
}

func clearProcessEfficiencyMode(pid int) {
	if pid <= 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = setHandleEfficiencyMode(h, false)
}
