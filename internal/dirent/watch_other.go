//go:build !darwin || !cgo

package dirent

import "os"

// startWatch is false where the system has no watch magpie uses: every
// Stats lists its folder.
func startWatch([]string) bool { return false }

func devOf(os.FileInfo) uint64 { return 0 }
