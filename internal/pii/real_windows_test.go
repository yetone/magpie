package pii

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateBytes is pid's commit charge, what Task Manager and
// Get-Process call private memory: PROCESS_MEMORY_COUNTERS'
// PagefileUsage.
func privateBytes(pid int) (int64, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var c struct {
		cb                                                 uint32
		pageFaultCount                                     uint32
		peakWorkingSetSize, workingSetSize                 uintptr
		quotaPeakPagedPoolUsage, quotaPagedPoolUsage       uintptr
		quotaPeakNonPagedPoolUsage, quotaNonPagedPoolUsage uintptr
		pagefileUsage, peakPagefileUsage                   uintptr
	}
	c.cb = uint32(unsafe.Sizeof(c))
	ok, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo").
		Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.cb))
	return int64(c.pagefileUsage), ok != 0
}
