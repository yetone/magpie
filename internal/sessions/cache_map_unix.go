//go:build unix

package sessions

import (
	"os"

	"golang.org/x/sys/unix"
)

// readCacheFile is the kept parses' file, mapped read-only rather than
// read onto the heap: a long history's is ~25 MB, held whole while it is
// decoded at start, and nothing decoded points into it. The file is only
// replaced by a rename (saveCache), never written in place. done unmaps
// it; nothing read from it is used after.
func readCacheFile(path string) (b []byte, done func(), err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if n := info.Size(); n > 0 && n == int64(int(n)) {
		if b, err := unix.Mmap(int(f.Fd()), 0, int(n), unix.PROT_READ, unix.MAP_SHARED); err == nil {
			return b, func() { unix.Munmap(b) }, nil
		}
	}
	b, err = os.ReadFile(path)
	return b, func() {}, err
}
