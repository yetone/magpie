//go:build unix

package agent

import "syscall"

// statKind is what is at p, following links, as os.Stat's IsDir would say,
// without the os.FileInfo os.Stat makes on the heap: detection asks it of
// some thousand paths on each of the window's 15s drift asks, and the
// FileInfos were a fifth of what forty asks allocated with real accounts.
func statKind(p string) (dir, ok bool) {
	var st syscall.Stat_t
	for {
		err := syscall.Stat(p, &st)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return false, false
		}
		return uint32(st.Mode)&syscall.S_IFMT == syscall.S_IFDIR, true
	}
}
