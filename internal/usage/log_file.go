package usage

import (
	"fmt"
	"os"
	"time"
)

// Keep native file identity alongside a change time captured from the same
// handle. os.SameFile requires the original os.FileInfo, not this wrapper.
type logFileInfo struct {
	os.FileInfo
	changeTime time.Time
}

var logChangeTime = nativeLogChangeTime

func logChangeStamp(info os.FileInfo) string {
	c := logChangeTime(info)
	if c.IsZero() {
		return ""
	}
	return fmt.Sprintf("%d:%d", c.Unix(), c.Nanosecond())
}

func sameLogFile(a, b os.FileInfo) bool {
	if wrapped, ok := a.(logFileInfo); ok {
		a = wrapped.FileInfo
	}
	if wrapped, ok := b.(logFileInfo); ok {
		b = wrapped.FileInfo
	}
	return a != nil && b != nil && os.SameFile(a, b)
}
