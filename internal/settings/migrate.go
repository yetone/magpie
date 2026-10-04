package settings

import (
	"io"
	"os"
	"path/filepath"

	"github.com/yetone/magpie/internal/appdir"
)

// Migrate carries the files of an install that predates the name over:
// ~/.config/dial and ~/.cache/dial become ~/.config/magpie and
// ~/.cache/magpie. It copies rather than moves, so a dial that is still
// running keeps working, and it only fills folders that do not exist yet.
// The old folders can be deleted once nothing uses them. A portable
// magpie has only its own data folder and copies nothing in.
func Migrate() {
	if Portable() != "" {
		return
	}
	copyTree(filepath.Join(filepath.Dir(Dir()), "dial"), Dir())
	cache := appdir.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home, _ := os.UserHomeDir()
		cache = filepath.Join(home, ".cache")
	}
	copyTree(filepath.Join(cache, "dial"), filepath.Join(cache, "magpie"))
}

// copyTree copies src into dst, files and modes alike, when src exists and
// dst does not. Errors are not reported: the worst case is a fresh start,
// which is what a missing folder means anyway.
func copyTree(src, dst string) {
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return
	}
	_ = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = os.MkdirAll(out, info.Mode().Perm())
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer in.Close()
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return nil
		}
		defer f.Close()
		_, _ = io.Copy(f, in)
		return nil
	})
}
