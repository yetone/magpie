//go:build !unix

package appdir

import "path/filepath"

// dirKey is the key two names of one folder share: where its links lead.
type dirKey struct{ name string }

func keyOf(dir string) dirKey { return dirKey{name: sameDir(dir)} }

// sameDir is the key two names of one folder share: where its links lead.
func sameDir(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	return filepath.Clean(dir)
}
