//go:build !unix

package sessions

import "os"

// readCacheFile is the kept parses' file (cache_map_unix.go maps it).
func readCacheFile(path string) ([]byte, func(), error) {
	b, err := os.ReadFile(path)
	return b, func() {}, err
}
