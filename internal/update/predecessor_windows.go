package update

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the magpie before must have let go of its WebView2 as well as
// gone itself: the new one's WebView2 opens the same profile folder
// (EBWebView), and one made while the old one's msedgewebview2 processes
// still run there joins the browser going down, and its window never comes
// (vsoullai on X, 0.1.4096: the new instance hung after an update's
// restart). The folder's lockfile is no sign: the browser lets it go before
// its last processes have ended. And an old magpie still quitting after
// 20s holds Wails' single instance mutex, so the new one took itself for a
// second launch and handed its start to the magpie quitting.
var (
	// predecessorMax is how long the magpie before may take to quit: its
	// OnShutdown ports Codex's rollouts, puts agents' configs back and
	// may install an update
	predecessorMax = 60 * time.Second
	// predecessorWebMax is how long its WebView2's processes may take after
	predecessorWebMax = 30 * time.Second
	// stuckFor is how long the magpie before may sit with one thread left
	// before it is taken for stuck in its exit and ended (stuckExiting)
	stuckFor = time.Second
)

// awaitGone waits for the magpie that relaunched this one to exit, and then
// for every msedgewebview2 process it had, each bounded.
func awaitGone(pid int) {
	start := time.Now()
	// the magpie first: while its handle is open its pid is no other's, so
	// the processes naming it their parent are its own
	self, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		self, err = windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	}
	if err != nil {
		return // gone already; its WebView2 can't be told apart from another's
	}
	defer windows.CloseHandle(self)
	var webs []windows.Handle
	for _, p := range webViewProcs(uint32(pid)) {
		if h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, p); err == nil {
			webs = append(webs, h)
			defer windows.CloseHandle(h)
		}
	}
	if !awaitExit(self, uint32(pid), predecessorMax) {
		logPredecessor("the magpie before (%d) is still running after %s; going on", pid, predecessorMax)
		return
	}
	deadline := time.Now().Add(predecessorWebMax)
	left := 0
	for _, h := range webs {
		if !waitHandle(h, time.Until(deadline)) {
			left++
		}
	}
	switch {
	case left > 0:
		logPredecessor("%d of the magpie before's %d WebView2 processes still run after %s; going on", left, len(webs), predecessorWebMax)
	case time.Since(start) > time.Second:
		logPredecessor("waited %s for the magpie before (%d) and its %d WebView2 processes to end", time.Since(start).Round(100*time.Millisecond), pid, len(webs))
	}
}

// awaitExit waits up to limit for the magpie before (h, pid) to end. One
// that sits with a single thread left is stuck in its exit: ExitProcess
// has ended every other thread and a DLL's detach on the last one waits
// for one of them, forever, while the process still holds the gateway's
// port and the WebView2 profile this one is about to open (BESTLJP0412
// on X: after an update's restart the window was "Not Responding" and the
// old magpie sat there with one thread). Nothing of it is left to finish,
// so it is ended.
func awaitExit(h windows.Handle, pid uint32, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	var oneSince time.Time
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		if waitHandle(h, min(left, 250*time.Millisecond)) {
			return true
		}
		if !stuckExiting(pid) {
			oneSince = time.Time{}
			continue
		}
		if oneSince.IsZero() {
			oneSince = time.Now()
			continue
		}
		if time.Since(oneSince) < stuckFor {
			continue
		}
		logPredecessor("the magpie before (%d) is stuck in its exit with one thread left; ending it", pid)
		if err := windows.TerminateProcess(h, 1); err != nil {
			logPredecessor("ending the magpie before (%d): %v", pid, err)
			oneSince = time.Time{}
			continue
		}
		return waitHandle(h, max(time.Until(deadline), 5*time.Second))
	}
}

// stuckExiting says whether pid has a single thread left: a Go program
// running has several (the scheduler's monitor at least), so one left is
// ExitProcess under way.
func stuckExiting(pid uint32) bool { return threads(pid) == 1 }

// threads is how many threads pid has, 0 when it isn't found.
func threads(pid uint32) uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == pid {
			return e.Threads
		}
	}
	return 0
}

func logPredecessor(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "magpie: "+format+"\n", a...)
}

// waitHandle waits up to max for the process h to end.
func waitHandle(h windows.Handle, max time.Duration) bool {
	ms := uint32(0)
	if max > 0 {
		ms = uint32(max / time.Millisecond)
	}
	ev, err := windows.WaitForSingleObject(h, ms)
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// webViewProcs are the msedgewebview2.exe processes under pid: the browser
// WebView2 started for it and every process that browser started
// (renderers, GPU, utilities, crashpad).
func webViewProcs(pid uint32) []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	children := map[uint32][]uint32{}
	web := map[uint32]bool{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == e.ParentProcessID {
			continue
		}
		children[e.ParentProcessID] = append(children[e.ParentProcessID], e.ProcessID)
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "msedgewebview2.exe") {
			web[e.ProcessID] = true
		}
	}
	var out []uint32
	seen := map[uint32]bool{pid: true}
	for queue := []uint32{pid}; len(queue) > 0; queue = queue[1:] {
		for _, c := range children[queue[0]] {
			if web[c] && !seen[c] {
				seen[c] = true
				out = append(out, c)
				queue = append(queue, c)
			}
		}
	}
	return out
}
