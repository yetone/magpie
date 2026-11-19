package proc

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSetEfficiencyModeMarksEcoQoSWithoutChangingPriority(t *testing.T) {
	self := windows.CurrentProcess()
	t.Cleanup(func() { _ = SetEfficiencyMode(false) })

	prioBefore, err := windows.GetPriorityClass(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetEfficiencyMode(true); err != nil {
		t.Fatal(err)
	}
	ctrl, state, err := readHandleEfficiencyMode(self)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl != processPowerThrottlingExecutionSpeed || state != processPowerThrottlingExecutionSpeed {
		t.Fatalf("efficiency mode on: masks = (%d, %d), want (%d, %d)", ctrl, state, processPowerThrottlingExecutionSpeed, processPowerThrottlingExecutionSpeed)
	}
	prioAfter, err := windows.GetPriorityClass(self)
	if err != nil {
		t.Fatal(err)
	}
	if prioAfter != prioBefore {
		t.Fatalf("priority class changed from 0x%x to 0x%x", prioBefore, prioAfter)
	}

	if err := SetEfficiencyMode(false); err != nil {
		t.Fatal(err)
	}
	ctrl, state, err = readHandleEfficiencyMode(self)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl != 0 || state != 0 {
		t.Fatalf("efficiency mode off: masks = (%d, %d), want (0, 0)", ctrl, state)
	}
}

func TestChildrenStartedInEfficiencyModeRunNormally(t *testing.T) {
	t.Cleanup(func() { _ = SetEfficiencyMode(false) })
	if err := SetEfficiencyMode(true); err != nil {
		t.Fatal(err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command(exe)
	cmd.Env = append(os.Environ(), "MAGPIE_PROC_SLEEP=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		_ = cmd.Wait()
	})

	ch, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_SET_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ch)

	ctrl, state, err := readHandleEfficiencyMode(ch)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl != 0 || state != 0 {
		t.Fatalf("child inherited efficiency mode: masks = (%d, %d), want (0, 0)", ctrl, state)
	}
	prio, err := windows.GetPriorityClass(ch)
	if err != nil {
		t.Fatal(err)
	}
	if prio != windows.NORMAL_PRIORITY_CLASS {
		t.Fatalf("child priority class = 0x%x, want NORMAL_PRIORITY_CLASS (0x%x)", prio, windows.NORMAL_PRIORITY_CLASS)
	}

	// ClearProcessEfficiencyMode lifts EcoQoS even when set on a child.
	if err := setHandleEfficiencyMode(ch, true); err != nil {
		t.Fatal(err)
	}
	ClearProcessEfficiencyMode(cmd.Process.Pid)
	ctrl, state, err = readHandleEfficiencyMode(ch)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl != 0 || state != 0 {
		t.Fatalf("after ClearProcessEfficiencyMode: masks = (%d, %d), want (0, 0)", ctrl, state)
	}

	// The parent process itself stays in efficiency mode meanwhile.
	pCtrl, pState, err := readHandleEfficiencyMode(windows.CurrentProcess())
	if err != nil {
		t.Fatal(err)
	}
	if pCtrl != processPowerThrottlingExecutionSpeed || pState != processPowerThrottlingExecutionSpeed {
		t.Fatalf("parent lost efficiency mode: masks = (%d, %d)", pCtrl, pState)
	}
}

func TestTreeEfficiencyModeAppliesToRootProcess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command(exe)
	cmd.Env = append(os.Environ(), "MAGPIE_PROC_SLEEP=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := StartTree(cmd)
	if err != nil {
		_ = in.Close()
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = in.Close()
		_ = tree.Wait()
	}
	t.Cleanup(stop)

	ch, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ch)

	tree.SetEfficiencyMode(true)
	ctrl, state, err := readHandleEfficiencyMode(ch)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl != processPowerThrottlingExecutionSpeed || state != processPowerThrottlingExecutionSpeed {
		t.Fatalf("tree efficiency on: masks = (%d, %d), want (%d, %d)", ctrl, state, processPowerThrottlingExecutionSpeed, processPowerThrottlingExecutionSpeed)
	}
	prio, err := windows.GetPriorityClass(ch)
	if err != nil {
		t.Fatal(err)
	}
	if prio != windows.NORMAL_PRIORITY_CLASS {
		t.Fatalf("tree priority class = 0x%x, want NORMAL_PRIORITY_CLASS", prio)
	}

	tree.SetEfficiencyMode(false)
	ctrl, state, err = readHandleEfficiencyMode(ch)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl != 0 || state != 0 {
		t.Fatalf("tree efficiency off: masks = (%d, %d), want (0, 0)", ctrl, state)
	}

	stop()
	// Calling SetEfficiencyMode after Wait is a safe no-op.
	tree.SetEfficiencyMode(true)
}

func TestPowerNowMatchesSystemPowerStatus(t *testing.T) {
	var raw systemPowerStatus
	r, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&raw)))
	if r == 0 {
		t.Fatalf("GetSystemPowerStatus failed: %v", err)
	}
	want := powerFromWindowsStatus(raw.ACLineStatus, raw.SystemStatusFlag, true)
	got := PowerNow()
	// Allow only for an actual plug/saver flip mid-call by re-reading once on mismatch.
	if got != want {
		r2, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&raw)))
		want2 := powerFromWindowsStatus(raw.ACLineStatus, raw.SystemStatusFlag, r2 != 0)
		if got != want2 {
			t.Fatalf("PowerNow() = %+v, want %+v (or %+v)", got, want, want2)
		}
	}
}
