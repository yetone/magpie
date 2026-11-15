package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// A dist tarball's add is judged by the copy Bun leaves in node_modules,
// not by what Bun says. Bun 1.4.4 installs a local tarball from its
// install cache (<cache>/@T@sha512-<32 hex>@@@1), trusting that entry
// whole once it is there: an entry missing a file (cut short, or a file
// taken out of it since) gives a copy missing that file, Bun saying "1
// package installed" and exiting 0. And a copy it can't make (a file of
// the old one held open on Windows: "EBUSY: failed copying files from
// cache to destination") still has it print "installed …", write the
// dependency and exit 1 with "Failed to install 1 package"
// (CHENGJIAHEGOXIS on X: "Unexpected: failed copying files …" adding
// GoAnyAPI on v0.1.4229). The tarball magpie checked against the index's
// sha512 is in hand, so the copy is checked against it, file by file.

// errDistPartial is a dist add whose copy in node_modules isn't the
// tarball's, after the add was tried again: the plugin isn't installed,
// whatever Bun said.
var errDistPartial = errors.New("the plugin's files didn't all reach plugins/node_modules")

// distAgainAfter is the pause before a dist add is tried again over a
// copy that wasn't whole: what held a file of it (an antivirus' scan, a
// host still letting go) has a moment to finish. Moved by tests.
var distAgainAfter = 2 * time.Second

// addDist adds the dist tarball file (plugins/dist/<file>, its bytes tgz)
// as name's, and checks the copy Bun made. A copy that isn't the
// tarball's is taken out with Bun's cached copy of the tarball, and the
// add tried once more; one still not whole is errDistPartial, with what
// Bun said. A whole copy is name installed: an error then is Bun's over
// another package, as before.
func addDist(ctx context.Context, bun, name, file string, tgz []byte) error {
	err := bunAdd(ctx, bun, "./dist/"+file, name)
	bad := distCopyDiffers(tgz, name)
	if bad == "" {
		return err
	}
	dropDistCopy(name, tgz)
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w (%s): %w", errDistPartial, bad, ctx.Err())
	case <-time.After(distAgainAfter):
	}
	err = bunAdd(ctx, bun, "./dist/"+file, name)
	bad2 := distCopyDiffers(tgz, name)
	if bad2 == "" {
		return err
	}
	if err == nil {
		return fmt.Errorf("%w (%s)", errDistPartial, bad2)
	}
	return fmt.Errorf("%w (%s): %w", errDistPartial, bad2, err)
}

// distCopyDiffers says how node_modules/<name> isn't the package in tgz,
// or "" when every file of tgz is there with its bytes. Files the copy has
// besides (Bun's .bun-tag) don't count.
func distCopyDiffers(tgz []byte, name string) string {
	at := filepath.Join(Dir(), "node_modules", filepath.FromSlash(name))
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return "the tarball can't be read: " + err.Error()
	}
	tr := tar.NewReader(gz)
	files := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "the tarball can't be read: " + err.Error()
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		// npm pack's files are under package/; the first part is the
		// package's folder whatever it is named, as Bun takes it
		_, rel, ok := strings.Cut(path.Clean(strings.TrimPrefix(h.Name, "./")), "/")
		if !ok || rel == "" || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		want, err := io.ReadAll(tr)
		if err != nil {
			return "the tarball can't be read: " + err.Error()
		}
		files++
		got, err := os.ReadFile(filepath.Join(at, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			return rel + " is missing"
		}
		if err != nil {
			return rel + " can't be read: " + err.Error()
		}
		if !bytes.Equal(got, want) {
			return rel + " isn't the tarball's"
		}
	}
	if files == 0 {
		return "the tarball has no files"
	}
	return ""
}

// dropDistCopy takes out node_modules/<name> and Bun's cached copy of
// tgz: the next add extracts the tarball afresh. Best-effort: a file
// still held stays, and the next add says so.
func dropDistCopy(name string, tgz []byte) {
	removeTree(filepath.Join(Dir(), "node_modules", filepath.FromSlash(name)))
	sum := sha512.Sum512(tgz)
	entry := "@T@sha512-" + hex.EncodeToString(sum[:])[:32] + "@@@1"
	root := installCacheDir()
	removeTree(filepath.Join(root, entry))
	os.Remove(filepath.Join(root, filepath.FromSlash(name), entry))
}

// removeTree takes out dir, making its folders writable where that is
// what stops it (a read-only folder's files can't be unlinked on unix).
func removeTree(dir string) {
	if os.RemoveAll(dir) == nil {
		return
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0o755)
		}
		return nil
	})
	os.RemoveAll(dir)
}
