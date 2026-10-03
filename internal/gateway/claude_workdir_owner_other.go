//go:build !windows

package gateway

import (
	"os"
	"strconv"
	"syscall"
)

// ownedByMe says the file belongs to the user magpie runs as.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// claudeWorkName is the work folder's name in the temp folder, which on
// Linux every user shares.
func claudeWorkName() string { return "magpie-claude-work-" + strconv.Itoa(os.Getuid()) }
