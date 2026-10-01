package provider

// Grok Build installed without a shell, as its installer
// (https://x.ai/cli/install.sh) installs it: the channel's version, that
// version's binary for this platform from the same place, kept in
// ~/.grok/downloads and linked from ~/.grok/bin as grok and agent. magpie
// runs this only where the installer can't run, its Docker image having no
// bash, curl or wget; the binary is static, so it runs there.

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// grokCLIBases are where the installer looks, first to last: x.ai, then
// the bucket behind it. A var so tests can point it elsewhere.
var grokCLIBases = []string{"https://x.ai/cli", "https://storage.googleapis.com/grok-build-public-artifacts/cli"}

var grokVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9._]+)?$`)

// grokMaxBinary bounds what is unpacked, as the installer bounds it.
const grokMaxBinary = 512 << 20

// grokPlatform is the installer's name for this OS and CPU.
func grokPlatform(goos, goarch string) (string, error) {
	var sys, arch string
	switch goos {
	case "linux":
		sys = "linux"
	case "darwin":
		sys = "macos"
	default:
		return "", fmt.Errorf("Grok Build has no build for %s", goos)
	}
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("Grok Build has no build for %s", goarch)
	}
	return sys + "-" + arch, nil
}

func installGrokBuild(ctx context.Context) error {
	platform, err := grokPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	base, version, err := grokStableVersion(ctx)
	if err != nil {
		return err
	}
	downloads := filepath.Join(home, ".grok", "downloads")
	bin := os.Getenv("GROK_BIN_DIR")
	if bin == "" {
		bin = filepath.Join(home, ".grok", "bin")
	}
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	final := filepath.Join(downloads, "grok-"+platform)
	tmp := fmt.Sprintf("%s.tmp.%d", final, os.Getpid())
	defer os.Remove(tmp)
	artifact := base + "/grok-" + version + "-" + platform
	// the gzip one is less than half the size; the bare binary if it's missing
	if err := grokDownload(ctx, artifact+".gz", tmp, true); err != nil {
		if err := grokDownload(ctx, artifact, tmp, false); err != nil {
			return fmt.Errorf("downloading Grok Build %s: %w", version, err)
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	// as the installer checks it: one that won't run leaves what was there
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = proc.CommandContext(vctx, tmp, "--version").Run()
	cancel()
	if err != nil {
		return fmt.Errorf("the downloaded Grok Build %s doesn't run here: %w", version, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	// relative when they are side by side, as the installer links them
	target := final
	if filepath.Dir(bin) == filepath.Dir(downloads) {
		target = filepath.Join("..", filepath.Base(downloads), filepath.Base(final))
	}
	for _, name := range []string{"grok", "agent"} {
		link := filepath.Join(bin, name)
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	grokNoteInstaller(filepath.Join(home, ".grok", "config.toml"))
	return nil
}

// grokStableVersion is the stable channel's version, and where it was read.
func grokStableVersion(ctx context.Context) (base, version string, err error) {
	err = errors.New("no answer")
	for _, b := range grokCLIBases {
		var body []byte
		if body, err = grokGet(ctx, b+"/stable", 1<<10); err != nil {
			continue
		}
		v := strings.TrimSpace(strings.SplitN(strings.ReplaceAll(string(body), "\r", ""), "\n", 2)[0])
		if !grokVersionRe.MatchString(v) {
			err = fmt.Errorf("an odd version %q", v)
			continue
		}
		return b, v, nil
	}
	return "", "", fmt.Errorf("reading Grok Build's latest version: %w", err)
}

func grokGet(ctx context.Context, u string, limit int64) ([]byte, error) {
	res, err := grokOpen(ctx, u)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

func grokOpen(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("%s: %s", u, res.Status)
	}
	return res, nil
}

// grokDownload writes what u has to path, unpacked when gzipped.
func grokDownload(ctx context.Context, u, path string, gzipped bool) error {
	res, err := grokOpen(ctx, u)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var r io.Reader = res.Body
	if gzipped {
		zr, err := gzip.NewReader(res.Body)
		if err != nil {
			return err
		}
		r = zr
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, grokMaxBinary+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return err
	case n == 0:
		return errors.New("an empty download")
	case n > grokMaxBinary:
		return errors.New("a download too big to be Grok Build")
	}
	return nil
}

// grokNoteInstaller says in the CLI's config how it was installed, as the
// installer does, where the config doesn't say yet.
func grokNoteInstaller(path string) {
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		_ = os.WriteFile(path, []byte("[cli]\ninstaller = \"internal\"\n"), 0o644)
	case err == nil && !regexp.MustCompile(`(?m)^\[cli\]`).Match(b):
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0); err == nil {
			_, _ = f.WriteString("\n[cli]\ninstaller = \"internal\"\n")
			_ = f.Close()
		}
	}
}
