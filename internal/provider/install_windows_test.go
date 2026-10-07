package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Devin's installer ends in `devin setup`, whose menu asking how to sign in
// waits for a key on a console no one can answer: the sign-in went on only
// after installTimeout, with `devin setup` left running. Once the CLI is in
// place the installer is ended a moment later, with what it started.
func TestWindowsDevinSetupDoesNotHoldTheSignIn(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "devin.exe")
	pidFile := filepath.Join(dir, "setup.pid")
	oldExe := DevinExecutable
	DevinExecutable = func() string {
		if isFile(exe) {
			return exe
		}
		return ""
	}
	t.Cleanup(func() { DevinExecutable = oldExe })
	oldTimeout := installTimeout
	installTimeout = 45 * time.Second
	t.Cleanup(func() { installTimeout = oldTimeout })
	c, _ := cliFor("devin")
	// as Devin's: the CLI goes in place, then a setup runs that waits
	c.ps = fmt.Sprintf("Set-Content -LiteralPath '%s' -Value devin; "+
		"$p = Start-Process -FilePath ping.exe -ArgumentList '-n','600','127.0.0.1' -NoNewWindow -PassThru; "+
		"Set-Content -LiteralPath '%s' -Value $p.Id; $p.WaitForExit()", exe, pidFile)
	start := time.Now()
	err := installCLI(context.Background(), c)
	took := time.Since(start)
	pid := 0
	if b, rerr := os.ReadFile(pidFile); rerr == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	t.Cleanup(func() { endPing(pid) })
	if err != nil {
		t.Fatal(err)
	}
	if pid == 0 {
		t.Fatal("the installer's setup never ran")
	}
	if took > 30*time.Second {
		t.Fatalf("the sign-in waited %s for the installer after the CLI was in place", took.Round(time.Second))
	}
	if pingRunning(pid) {
		t.Fatalf("the installer's setup (pid %d) is left running", pid)
	}
}

// pingRunning is whether pid is a ping.exe that is still running: a pid
// let go of can be another program's soon after.
func pingRunning(pid int) bool {
	if pid == 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil || code != 259 { // STILL_ACTIVE
		return false
	}
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return false
	}
	return strings.EqualFold(filepath.Base(windows.UTF16ToString(buf[:n])), "ping.exe")
}

// endPing ends the setup a test started, where it was left running.
func endPing(pid int) {
	if !pingRunning(pid) {
		return
	}
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}
