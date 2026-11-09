package agent

import (
	"syscall"
	"time"
)

// statStamp is p's modification time and size, links followed, as
// os.Stat's ModTime and Size would say, without the os.FileInfo it makes
// on the heap: bundleInfo and folderSubs ask it of every desktop app on
// each drift ask.
func statStamp(p string) (mod time.Time, size int64, ok bool) {
	var st syscall.Stat_t
	if !statInto(p, &st) {
		return time.Time{}, 0, false
	}
	return time.Unix(st.Mtimespec.Unix()), st.Size, true
}
