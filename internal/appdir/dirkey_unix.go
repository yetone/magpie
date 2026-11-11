//go:build unix

package appdir

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// dirKey is the key two names of one folder share: its device and inode,
// which a link to it has too. sameDir's EvalSymlinks looked at every part
// of the name, and the homes look ran it for every Claude and Codex folder
// on each Sessions poll, ~3 MB of garbage per 40 polls on a real home
// (#2278). A folder that can't be looked at is keyed by its name.
type dirKey struct {
	dev, ino uint64
	name     string
}

func keyOf(dir string) dirKey {
	var st unix.Stat_t
	for {
		err := unix.Stat(dir, &st)
		if err == nil {
			return dirKey{dev: uint64(st.Dev), ino: uint64(st.Ino)}
		}
		if err != unix.EINTR {
			return dirKey{name: filepath.Clean(dir)}
		}
	}
}
