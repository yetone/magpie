//go:build !windows

package proc

import "path/filepath"

// RealPath is where the file at p really is, its links followed.
func RealPath(p string) (string, error) { return filepath.EvalSymlinks(p) }
