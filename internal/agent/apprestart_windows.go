//go:build windows

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/proc"
)

var procGetApplicationUserModelID = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetApplicationUserModelId")

// listWinProcs lists the processes named exe, case aside, from a Toolhelp
// snapshot: no child process, no WMI. One whose image can't be read
// (another user's) is left out. Nothing in a test binary testenv isolated.
func listWinProcs(exe string) []winProc {
	if proc.Isolated() || exe == "" {
		return nil
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var out []winProc
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == 0 || !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), exe) {
			continue
		}
		if p, ok := readWinProc(e.ProcessID); ok {
			p.parent = int(e.ParentProcessID)
			out = append(out, p)
		}
	}
	return out
}

func readWinProc(pid uint32) (winProc, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return winProc{}, false
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return winProc{}, false
	}
	p := winProc{pid: int(pid), image: windows.UTF16ToString(buf[:n]), cmd: proc.CommandLine(int(pid))}
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil {
		p.start = time.Unix(0, created.Nanoseconds())
	}
	if procGetApplicationUserModelID.Find() == nil {
		id := make([]uint16, 256)
		l := uint32(len(id))
		if r, _, _ := procGetApplicationUserModelID.Call(uintptr(h), uintptr(unsafe.Pointer(&l)), uintptr(unsafe.Pointer(&id[0]))); r == 0 {
			p.aumid = windows.UTF16ToString(id)
		}
	}
	return p, true
}

// closeWinProcs asks the processes to close, as their windows' close does
// (taskkill without /F posts WM_CLOSE); one with no window ignores it.
func closeWinProcs(pids []int) error {
	if len(pids) == 0 {
		return nil
	}
	args := make([]string, 0, 2*len(pids))
	for _, pid := range pids {
		args = append(args, "/PID", strconv.Itoa(pid))
	}
	return proc.Command("taskkill", args...).Run()
}

// shownCallback is EnumWindows' callback, made once: Go has room for only
// so many. shownPids and shownSeen are its input and output, under shownMu.
var (
	shownMu       sync.Mutex
	shownPids     map[uint32]bool
	shownSeen     bool
	shownCallback = windows.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(h, &pid); err == nil && shownPids[pid] && windows.IsWindowVisible(h) {
			shownSeen = true
			return 0
		}
		return 1
	})
)

// winWindowsShown says whether any of pids has a visible top-level window,
// true when the windows can't be listed. An Electron app closed to the
// tray hides its window and keeps running.
func winWindowsShown(pids []int) bool {
	shownMu.Lock()
	defer shownMu.Unlock()
	shownPids = make(map[uint32]bool, len(pids))
	for _, p := range pids {
		shownPids[uint32(p)] = true
	}
	shownSeen = false
	if err := windows.EnumWindows(shownCallback, nil); err != nil && !shownSeen {
		// a callback that stops the walk makes EnumWindows return an
		// error too; any other error leaves it unknown
		return true
	}
	return shownSeen
}

// endWinTree ends pid and every process under it.
func endWinTree(pid int) error {
	out, err := proc.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).CombinedOutput()
	return cmdErr(out, err)
}

// launchWin opens the app main was again: a packaged app by its
// AppUserModelID through the shell, as its Start menu tile does; Squirrel's
// Claude through the claude.exe stub beside its app-<version> folders, which
// starts the newest; anything else by its image.
func launchWin(main winProc) error {
	if main.aumid != "" {
		return shellOpen("explorer.exe", `shell:AppsFolder\`+main.aumid)
	}
	exe := main.image
	if dir := filepath.Dir(exe); strings.HasPrefix(strings.ToLower(filepath.Base(dir)), "app-") {
		if stub := filepath.Join(filepath.Dir(dir), filepath.Base(exe)); isFile(stub) {
			exe = stub
		}
	}
	if _, err := os.Stat(exe); err != nil {
		return err
	}
	return shellOpen(exe, "")
}

func shellOpen(file, args string) error {
	f, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var a *uint16
	if args != "" {
		if a, err = windows.UTF16PtrFromString(args); err != nil {
			return err
		}
	}
	if err := windows.ShellExecute(0, nil, f, a, nil, windows.SW_SHOWNORMAL); err != nil {
		return errors.New("couldn't open " + file + ": " + err.Error())
	}
	return nil
}
