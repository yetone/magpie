package agent

import (
	"syscall"
	"time"
)

// statStamp is p's modification time and size, links followed, as
// os.Stat's ModTime and Size would say (statstamp_darwin.go).
func statStamp(p string) (mod time.Time, size int64, ok bool) {
	var st syscall.Stat_t
	if !statInto(p, &st) {
		return time.Time{}, 0, false
	}
	return time.Unix(st.Mtim.Unix()), st.Size, true
}
