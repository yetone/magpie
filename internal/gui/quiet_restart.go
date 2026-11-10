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
// update` run in a terminal) brings the app back as it was, and leaves
// the user where they were. Its window comes back only if it was up
// (#2096); now it also takes no focus (xyxyxy_15591 on Discord: "自动更新
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
// A window that was minimised comes back minimised (#3495, Lemonawa):
// the mark says so ("<seconds> minimised"), and the new one puts it on
// the taskbar as it was, neither shown nor activated (showMinimised). A
// mark of the seconds alone, from a version before, is a window up.

// quietRestartFile, in magpie's folder, holds when (Unix seconds) a
// restart by itself began.
const quietRestartFile = "restart-quiet"

// quietFor is how long the mark counts: a handover given up waits for the
// requests in flight before the new one is opened (fallbackWait), and a
// mark older than that is a restart that never came back, not this start.
var quietFor = 10 * time.Minute

// quietMinimisedWord follows the mark's seconds when the window was
// minimised.
const quietMinimisedWord = "minimised"

// markQuietRestart notes that the restart under way is one by itself, and
// whether the main window was minimised as it began.
func markQuietRestart(minimised bool) {
	dir := settings.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	if minimised {
		stamp += " " + quietMinimisedWord
	}
	stamp += "\n"
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
