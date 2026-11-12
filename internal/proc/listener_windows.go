package proc

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

// listeningOn asks Get-NetTCPConnection, which names each listener's owner.
// When nothing listens the cmdlet fails with ObjectNotFound, and with
// -ErrorAction SilentlyContinue PowerShell still exits 1 for it, so a port
// nobody holds read as an error and a take-over waited for the older
// magpie's port to be let go until it said "stuck" (#2231). That one
// failure is no listener, as lsof and /proc say it; any other failure, or
// a PowerShell cut short, is still an error.
func listeningOn(ctx context.Context, port int) ([]int, error) {
	out, err := CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command",
		`try { Get-NetTCPConnection -State Listen -LocalPort `+strconv.Itoa(port)+` -ErrorAction Stop | ForEach-Object { $_.OwningProcess } } `+
			`catch { if ($_.CategoryInfo.Category -eq 'ObjectNotFound') { exit 0 }; [Console]::Error.WriteLine($_); exit 1 }`).Output()
	if err != nil {
		return nil, err
	}
	var ps []int
	seen := map[int]bool{}
	for _, l := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && !seen[n] {
			seen[n] = true
			ps = append(ps, n)
		}
	}
	return ps, nil
}

func executable(pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// terminate ends it at once: a tray app has no console for a Ctrl+Break,
// and no window a close message would reach.
func terminate(pid int) error { return kill(pid) }

func kill(pid int) error { return exitWith(pid, 1) }

// end ends pid with exit code 0, which its parent reads as a clean exit:
// Windows has no SIGTERM for a console program without a window.
func end(pid int) error { return exitWith(pid, 0) }

func exitWith(pid int, code uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return ErrDenied
		}
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil // gone already
		}
		return err
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, code); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return ErrDenied
		}
		return err
	}
	return nil
}

// processName is Get-Process' name, which names a service's process where
// opening it for its path is denied.
func processName(pid int) string {
	out, err := CommandContext(context.Background(), "powershell", "-NoProfile", "-NonInteractive", "-Command",
		`(Get-Process -Id `+strconv.Itoa(pid)+` -ErrorAction SilentlyContinue).ProcessName`).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
