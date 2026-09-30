//go:build !windows

package edit

import (
	"os"
	"syscall"
)

// hardLinked reports whether the file at path has other names too. Where
// the system gives no link count it is taken to have none.
func hardLinked(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
