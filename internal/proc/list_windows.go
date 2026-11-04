package proc

import (
	"context"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// list walks a Toolhelp snapshot for the processes named prefix*, case
// aside, and reads each one's command line from the process itself. It
// used to ask WMI through a new PowerShell each time, which cost a .NET
// start and a WMI query every few seconds while a Codex daemon was
// watched (#3467); this takes no child process and no WMI. A process
// whose command line isn't readable (another user's, a protected one)
// is listed with its id alone, as WMI listed it.
func list(ctx context.Context, prefix string) ([]Process, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	prefix = strings.ToLower(prefix)
	var ps []Process
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := windows.UTF16ToString(e.ExeFile[:])
		if e.ProcessID == 0 || !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		ps = append(ps, Process{PID: int(e.ProcessID), Args: commandLine(e.ProcessID)})
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return nil, err
	}
	return ps, nil
}

// CommandLine is the command line process pid was started with, "" when
// it can't be read.
func CommandLine(pid int) string { return commandLine(uint32(pid)) }

// commandLine is a process's command line as it was started, "" when it
// can't be read.
func commandLine(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	// the answer is a UNICODE_STRING followed by its text; ask for the
	// size first, since a command line can run to 32K characters
	var n uint32
	_ = windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, nil, 0, &n)
	if n < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) {
		return ""
	}
	buf := make([]byte, n)
	if err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), n, &n); err != nil {
		return ""
	}
	return strings.TrimSpace((*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String())
}

// family walks a Toolhelp snapshot of every process, with its parent's
// id and its command line.
func family(ctx context.Context) ([]Kin, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var ps []Kin
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.ProcessID != 0 {
			ps = append(ps, Kin{PID: int(e.ProcessID), PPID: int(e.ParentProcessID), Args: commandLine(e.ProcessID)})
		}
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return nil, err
	}
	return ps, nil
}

// endFamily is endTree, which tells what pid started from a later process
// given an old parent's id by its start.
func endFamily(pid int, _ []int) { _ = endTree(pid) }
