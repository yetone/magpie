package sessions

import (
	"os"
	"path/filepath"
)

// UnsupportedReasonixStores reports native stores that this reader cannot
// decode. It inspects file names only: it never opens a writer, migrates a
// session, or treats a cache, lock, or sidecar as a conversation.
func UnsupportedReasonixStores() int {
	root := ReasonixDir()
	_ = root
	seen := map[string]bool{}
	for _, dir := range reasonixStoreDirs() {
		st, err := os.Stat(filepath.Join(dir, "manifest.json"))
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		exists := false
		for _, name := range []string{"events.frames", "events.jsonl"} {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode().IsRegular() {
				exists = true
			}
		}
		if !exists {
			continue
		}
		reasonixStoreFailures.Lock()
		failed := reasonixStoreFailures.dirs[dir]
		reasonixStoreFailures.Unlock()
		if _, err := reasonixStoreManifestAt(dir); err != nil || failed {
			seen[dir] = true
		}
	}
	return len(seen)
}
