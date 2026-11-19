package proc

// PowerState is how the computer is powered now: running on its battery
// (unplugged), and whether the system's energy saver is on.
type PowerState struct {
	OnBattery   bool
	EnergySaver bool
}

// EfficiencyActive reports whether magpie runs in Windows' efficiency mode
// (EcoQoS): while the computer runs on battery or Energy Saver is on, and
// none of magpie's GUI windows is in the foreground.
func EfficiencyActive(p PowerState, inForeground bool) bool {
	return (p.OnBattery || p.EnergySaver) && !inForeground
}

// powerFromWindowsStatus interprets Win32's SYSTEM_POWER_STATUS fields. A
// failed read (ok false) or unknown ACLineStatus (255) is treated as not on
// battery, so magpie never throttles on an unreadable power state.
func powerFromWindowsStatus(acLine, statusFlag byte, ok bool) PowerState {
	if !ok {
		return PowerState{}
	}
	// ACLineStatus: 0 unplugged, 1 plugged in, 255 unknown.
	// SystemStatusFlag: 1 when Windows Energy Saver is on.
	return PowerState{
		OnBattery:   acLine == 0,
		EnergySaver: statusFlag == 1,
	}
}

// PowerNow reports how the computer is powered now; where that cannot be
// read, both fields are false.
func PowerNow() PowerState { return powerNow() }

// SetEfficiencyMode puts this process in Windows' efficiency mode
// (SetProcessInformation with ProcessPowerThrottling and
// PROCESS_POWER_THROTTLING_EXECUTION_SPEED), or with on false hands the
// choice back to Windows. The priority class is never changed, so localhost
// gateway requests still wake at normal priority while background threads
// run on efficiency cores.
func SetEfficiencyMode(on bool) error { return setEfficiencyMode(on) }

// ClearProcessEfficiencyMode hands a started child process's power-throttling
// choice back to Windows, so a program magpie opens for the user never runs
// in efficiency mode.
func ClearProcessEfficiencyMode(pid int) { clearProcessEfficiencyMode(pid) }
