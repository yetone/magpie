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
)

// awaitGone waits for the magpie that relaunched this one to exit, and then
// for every msedgewebview2 process it had, each bounded.
func awaitGone(pid int) {
	start := time.Now()
	// the magpie first: while its handle is open its pid is no other's, so
	// the processes naming it their parent are its own
	self, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
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
	if !waitHandle(self, predecessorMax) {
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
