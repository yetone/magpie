package provider

// The Devin CLI installed without a shell, as its installer
// (https://cli.devin.ai/install.sh) installs it: the manifest of the current
// version, that version's bundle for this platform checked against the
// manifest's SHA-256, unpacked into $XDG_DATA_HOME/devin/cli/_versions/<version>,
// with `current` and ~/.local/bin/devin linked to it. magpie runs this only
// where the installer can't run, its Docker image having no curl or wget.
// Not `devin setup`, which the installer ends with: it signs in, and wants a
// terminal. A sign-in moved onto the Devin plugin installs the CLI this way
// too (installCLI), so a fix here reaches moved users.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

// devinCLIBase is where the installer looks for the manifest. A var so tests
// can point it elsewhere.
var devinCLIBase = "https://static.devin.ai/cli"

var devinVersionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]*$`)

// devinMaxBundle bounds what is downloaded and what is unpacked from it.
const (
	devinMaxBundle   = 1 << 30
	devinMaxUnpacked = 2 << 30
)

type devinManifest struct {
	Version   string `json:"version"`
	Platforms map[string]struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"platforms"`
}

// devinTarget is the installer's name for this OS and CPU.
func devinTarget(goos, goarch string) (string, error) {
	var arch string
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("the Devin CLI has no build for %s", goarch)
	}
	switch goos {
	case "linux":
		return arch + "-unknown-linux", nil
	case "darwin":
		return arch + "-apple-darwin", nil
	}
	return "", fmt.Errorf("the Devin CLI has no build for %s", goos)
}

func installDevinCLI(ctx context.Context) error {
	target, err := devinTarget(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	data := appdir.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	m, err := devinCurrentManifest(ctx)
	if err != nil {
		return err
	}
	p, ok := m.Platforms[target]
	if !ok || p.URL == "" || p.SHA256 == "" {
		return fmt.Errorf("Devin CLI %s has no bundle for %s", m.Version, target)
	}
	versions := filepath.Join(data, "devin", "cli", "_versions")
	dir := filepath.Join(versions, m.Version)
	if st, err := os.Stat(filepath.Join(dir, "bin", "devin")); err != nil || st.IsDir() {
		if err := devinUnpack(ctx, versions, m.Version, p.URL, p.SHA256); err != nil {
			return err
		}
	}
	current := filepath.Join(versions, "current")
	_ = os.Remove(current)
	if err := os.Symlink(m.Version, current); err != nil {
		return err
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	link := filepath.Join(bin, "devin")
	if st, err := os.Lstat(link); err == nil {
		// as the installer: a file of the user's own is theirs to remove
		if st.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink; remove it to install the Devin CLI", link)
		}
		_ = os.Remove(link)
	}
	if err := os.Symlink(filepath.Join(current, "bin", "devin"), link); err != nil {
		return err
	}
	// the marker the updater reads for which URLs to use
	return os.WriteFile(filepath.Join(dir, "distribution"), []byte("curl-bash\n"), 0o644)
}

// devinCurrentManifest is the current version's manifest.
func devinCurrentManifest(ctx context.Context) (devinManifest, error) {
	var m devinManifest
	u := strings.TrimRight(devinCLIBase, "/") + "/current/manifest.json"
	res, err := grokOpen(ctx, u)
	if err != nil {
		return m, fmt.Errorf("reading the Devin CLI's latest version: %w", err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("reading the Devin CLI's latest version: %w", err)
	}
	if !devinVersionRe.MatchString(m.Version) {
		return m, fmt.Errorf("reading the Devin CLI's latest version: an odd version %q", m.Version)
	}
	return m, nil
}

// devinUnpack downloads a bundle, checks it against sum, and unpacks it into
// versions/<version>, replacing what was there only when all of it unpacked.
func devinUnpack(ctx context.Context, versions, version, url, sum string) error {
	downloads := filepath.Join(versions, "_download")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return err
	}
	bundle := filepath.Join(downloads, version+".tar.gz")
	defer os.Remove(bundle)
	if err := devinDownload(ctx, url, bundle, sum); err != nil {
		return fmt.Errorf("downloading Devin CLI %s: %w", version, err)
	}
	tmp := filepath.Join(versions, "_"+version+".tmp")
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := devinExtract(bundle, tmp); err != nil {
		return fmt.Errorf("unpacking Devin CLI %s: %w", version, err)
	}
	dir := filepath.Join(versions, version)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

// devinDownload writes what url has to path, when its SHA-256 is sum.
func devinDownload(ctx context.Context, url, path, sum string) error {
	res, err := grokOpen(ctx, url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, devinMaxBundle+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return err
	case n > devinMaxBundle:
		return errors.New("a download too big to be the Devin CLI")
	case !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), sum):
		return errors.New("checksum mismatch")
	}
	return nil
}

// devinExtract unpacks a .tar.gz into dest: files, folders and links that
// stay inside it, and nothing else.
func devinExtract(bundle, dest string) error {
	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		path := filepath.Join(dest, filepath.FromSlash(h.Name))
		if path != dest && !strings.HasPrefix(path, dest+string(filepath.Separator)) {
			return fmt.Errorf("%q leaves the folder", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			if total += h.Size; total > devinMaxUnpacked {
				return errors.New("an archive too big to be the Devin CLI")
			}
			err = devinWrite(path, tr, h.FileInfo().Mode().Perm()|0o600)
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || !strings.HasPrefix(filepath.Join(filepath.Dir(path), filepath.FromSlash(h.Linkname)), dest+string(filepath.Separator)) {
				return fmt.Errorf("link %q leaves the folder", h.Name)
			}
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = os.Symlink(filepath.FromSlash(h.Linkname), path)
			}
		case tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("%q is a kind of file the Devin CLI doesn't ship", h.Name)
		}
		if err != nil {
			return err
		}
	}
}

func devinWrite(path string, r io.Reader, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
