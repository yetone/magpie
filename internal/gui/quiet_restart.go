package gui

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// A restart magpie makes by itself (Settings' AutoRestart, or `magpie
// update` run in a terminal) brings the app back in the tray alone
// (restartBySelf), and leaves the user where they were. Before, its
// window came back if it was up (#2096), taking no focus (xyxyxy_15591 on Discord: "自动更新
// 安装更新之后，不跳出界面"). The window came up in front of the app the
// user was typing in and took the keyboard, for a restart nobody asked
// for: the Mac's open brought magpie to the front, its Dock icon joining
// activated it, and the window's Focus did on every system.
//
// The old version writes quietRestartFile before it starts the new one;
// the new one reads it once as it starts (takeQuietRestart), and shows
// its window, if it was up, without Focus or activating (quietShow). The
// Mac's open is -g as well (update.Relaunch's quiet).
//
// This version marks the seconds alone, and asks for no window. A version
// before restarts into this one with its window up as it was, and minimised
// if it was (#3495, Lemonawa): its mark then says so ("<seconds>
// minimised"), and this one puts the window on the taskbar as it was,
// neither shown nor activated (showMinimised).

// quietRestartFile, in magpie's folder, holds when (Unix seconds) a
// restart by itself began.
const quietRestartFile = "restart-quiet"

// quietFor is how long the mark counts: a handover given up waits for the
// requests in flight before the new one is opened (fallbackWait), and a
// mark older than that is a restart that never came back, not this start.
var quietFor = 10 * time.Minute

// quietMinimisedWord follows the mark's seconds when an older version's
// window was minimised.
const quietMinimisedWord = "minimised"

// markQuietRestart notes that the restart under way is one by itself.
func markQuietRestart() {
	dir := settings.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10) + "\n"
	if err := os.WriteFile(filepath.Join(dir, quietRestartFile), []byte(stamp), 0o644); err != nil {
		log.Println("quiet restart:", err)
	}
}

// unmarkQuietRestart takes the mark back: the restart didn't happen.
func unmarkQuietRestart() { os.Remove(filepath.Join(settings.Dir(), quietRestartFile)) }

// takeQuietRestart is whether this start is a restart by itself, the mark
// read once and removed. One unreadable or stale reads as not: the start
// is then the ordinary one, which shows its window as it always has.
// minimised is whether the window was minimised, for a quiet one only.
func takeQuietRestart() (quiet, minimised bool) {
	f := filepath.Join(settings.Dir(), quietRestartFile)
	b, err := os.ReadFile(f)
	if err != nil {
		return false, false
	}
	os.Remove(f)
	words := strings.Fields(string(b))
	if len(words) == 0 || len(words) > 2 || len(words) == 2 && words[1] != quietMinimisedWord {
		return false, false
	}
	sec, err := strconv.ParseInt(words[0], 10, 64)
	if err != nil {
		return false, false
	}
	age := time.Since(time.Unix(sec, 0))
	if age < -time.Minute || age > quietFor {
		return false, false
	}
	return true, len(words) == 2
}

// quietShow: the next show of the main window is the one a restart by
// itself brings back, shown without taking focus; read once (openMain).
var quietShow atomic.Bool

// quietMinimised: that show puts the window back minimised (showMinimised);
// read with quietShow.
var quietMinimised atomic.Bool
