//go:build !unix

package appdir

import (
	"os"
	"time"
)

// DirStat is os.Stat's mod time, size and whether path is a folder.
func DirStat(path string) (mod time.Time, size int64, dir bool, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0, false, err
	}
	return st.ModTime(), st.Size(), st.IsDir(), nil
}
