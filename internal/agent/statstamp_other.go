//go:build !darwin && !linux

package agent

import (
	"os"
	"time"
)

// statStamp is p's modification time and size, links followed
// (statstamp_darwin.go).
func statStamp(p string) (mod time.Time, size int64, ok bool) {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}, 0, false
	}
	return st.ModTime(), st.Size(), true
}
