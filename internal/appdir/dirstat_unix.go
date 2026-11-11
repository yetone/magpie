//go:build unix

package appdir

import (
	"io/fs"
	"time"

	"golang.org/x/sys/unix"
)

// DirStat is os.Stat's mod time, size and whether path is a folder, read
// into a Stat_t on the stack: os.Stat made a FileInfo of each of the few
// hundred folders a Sessions poll looks at, ~4.5 MB of garbage per 40 polls
// on a real history (#2278). Its errors are the errno, which errors.Is
// matches to fs.ErrNotExist as os.Stat's are.
func DirStat(path string) (mod time.Time, size int64, dir bool, err error) {
	var st unix.Stat_t
	for {
		err = unix.Stat(path, &st)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return time.Time{}, 0, false, &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	return time.Unix(st.Mtim.Unix()), st.Size, st.Mode&unix.S_IFMT == unix.S_IFDIR, nil
}
