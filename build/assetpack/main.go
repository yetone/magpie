// assetpack writes the GUI's assets as one zip, which a release build
// embeds in place of the tree (entries are under assets/, as go:embed names them) (-tags packedassets, internal/gui/assets_packed.go):
// the scripts, styles and svgs shrink to a quarter, where a Windows or Linux
// download is the raw binary. Usage: go run ./build/assetpack <assets dir> <out.zip>
package main

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// already compressed: deflating them only costs time
var stored = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webm": true, ".mp4": true, ".woff": true, ".woff2": true, ".ico": true}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: assetpack <assets dir> <out.zip>")
		os.Exit(2)
	}
	if err := pack(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "assetpack:", err)
		os.Exit(1)
	}
}

func pack(dir, out string) error {
	var names []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// go:embed of a folder leaves out names that begin with . or _
		if p != dir && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(names)
	tmp := out + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(n)))
		if err != nil {
			f.Close()
			return err
		}
		h := &zip.FileHeader{Name: "assets/" + n, Method: zip.Deflate}
		if stored[strings.ToLower(path.Ext(n))] {
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err == nil {
			_, err = w.Write(b)
		}
		if err != nil {
			f.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}
