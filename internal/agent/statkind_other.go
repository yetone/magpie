//go:build !unix

package agent

import "os"

// statKind is what is at p, following links (statkind_unix.go).
func statKind(p string) (dir, ok bool) {
	st, err := os.Stat(p)
	if err != nil {
		return false, false
	}
	return st.IsDir(), true
}

// statFile is whether p, links followed, is a regular file.
func statFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
