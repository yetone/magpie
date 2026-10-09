package usage

import (
	"os"
	"syscall"
	"time"
)

// ctime changes even when a writer restores the size and modification time.
func nativeLogChangeTime(info os.FileInfo) time.Time {
	if info == nil {
		return time.Time{}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}
	}
	return time.Unix(stat.Ctimespec.Sec, stat.Ctimespec.Nsec)
}
