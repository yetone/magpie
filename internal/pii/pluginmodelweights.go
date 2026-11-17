package pii

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
)

// weightsStamp covers a single weights file or the files in a model
// directory. Follow links, including a model directory linked to a snapshot,
// without reading large weights on each lookup.
func weightsStamp(path string) string {
	sum := sha256.New()
	seen := map[string]bool{}
	var walk func(string) error
	walk = func(path string) error {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(sum, "%q %q\n", path, resolved)
		if seen[resolved] {
			return nil
		}
		seen[resolved] = true
		return filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return walk(path)
			}
			info, err := d.Info()
			if err == nil {
				fmt.Fprintf(sum, "%q %d %d %d\n", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
			}
			return err
		})
	}
	if walk(path) != nil {
		return "" // unavailable until the weights can be identified again
	}
	return hex.EncodeToString(sum.Sum(nil))
}
