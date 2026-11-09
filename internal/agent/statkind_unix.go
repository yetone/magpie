//go:build unix

package agent

import "syscall"

// statKind is what is at p, following links, as os.Stat's IsDir would say,
// without the os.FileInfo os.Stat makes on the heap: detection asks it of
// some thousand paths on each of the window's 15s drift asks, and the
// FileInfos were a fifth of what forty asks allocated with real accounts.
func statKind(p string) (dir, ok bool) {
	var st syscall.Stat_t
	if !statInto(p, &st) {
		return false, false
	}
	return uint32(st.Mode)&syscall.S_IFMT == syscall.S_IFDIR, true
}

// statFile is whether p, links followed, is a regular file, as os.Stat's
// Mode().IsRegular() would say.
func statFile(p string) bool {
	var st syscall.Stat_t
	return statInto(p, &st) && uint32(st.Mode)&syscall.S_IFMT == syscall.S_IFREG
}

// statInto is syscall.Stat of p into st, tried again when a signal cuts
// it short, as os.Stat is.
func statInto(p string, st *syscall.Stat_t) bool {
	for {
		err := syscall.Stat(p, st)
		if err == syscall.EINTR {
			continue
		}
		return err == nil
	}
}
