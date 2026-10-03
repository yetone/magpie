//go:build !windows

package gateway

import (
	"os"
	"syscall"
)

// ownedByMe says the file belongs to the user magpie runs as.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
