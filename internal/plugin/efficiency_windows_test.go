package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func readPIDPowerThrottling(t *testing.T, pid int) (uint32, uint32) {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	type state struct {
		Version, ControlMask, StateMask uint32
	}
	st := state{Version: 1}
	r, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessInformation").Call(
		uintptr(h), 4, uintptr(unsafe.Pointer(&st)), unsafe.Sizeof(st),
	)
	if r == 0 {
		t.Fatalf("GetProcessInformation(%d): %v", pid, callErr)
	}
	return st.ControlMask, st.StateMask
}

func TestPluginHostEfficiencyModeFollowsLiveAndRestartedHosts(t *testing.T) {
	real, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	bunHome(t, BunVersion, time.Now())
	t.Cleanup(Settle)

	old := filepath.Join(bunDirOf(BunVersion), bunExe())
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	placeBun(t, real, old)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if ps, err := Providers(ctx); err != nil || len(ps) != 1 {
		t.Fatalf("Providers = %+v, %v", ps, err)
	}

	hostMu.Lock()
	h1 := current
	hostMu.Unlock()
	if h1 == nil || h1.cmd == nil || h1.cmd.Process == nil {
		t.Fatal("no host running")
	}
	pid1 := h1.cmd.Process.Pid

	if ctrl, state := readPIDPowerThrottling(t, pid1); ctrl != 0 || state != 0 {
		t.Fatalf("initial host masks = (%d, %d), want (0, 0)", ctrl, state)
	}

	SetEfficiencyMode(true)
	if ctrl, state := readPIDPowerThrottling(t, pid1); ctrl != 1 || state != 1 {
		t.Fatalf("after SetEfficiencyMode(true): host masks = (%d, %d), want (1, 1)", ctrl, state)
	}

	// A host started after Restart while efficiency mode is on starts in EcoQoS too.
	Restart()
	if ps, err := Providers(ctx); err != nil || len(ps) != 1 {
		t.Fatalf("Providers after Restart = %+v, %v", ps, err)
	}
	hostMu.Lock()
	h2 := current
	hostMu.Unlock()
	if h2 == nil || h2.cmd == nil || h2.cmd.Process == nil || h2.cmd.Process.Pid == pid1 {
		t.Fatalf("expected a new host after Restart, got %+v", h2)
	}
	pid2 := h2.cmd.Process.Pid
	if ctrl, state := readPIDPowerThrottling(t, pid2); ctrl != 1 || state != 1 {
		t.Fatalf("restarted host masks = (%d, %d), want (1, 1)", ctrl, state)
	}

	SetEfficiencyMode(false)
	if ctrl, state := readPIDPowerThrottling(t, pid2); ctrl != 0 || state != 0 {
		t.Fatalf("after SetEfficiencyMode(false): host masks = (%d, %d), want (0, 0)", ctrl, state)
	}
}
