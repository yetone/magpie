package plugin

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/catalog"
)

// BunVersion is the Bun magpie downloads to run plugins with the first
// time one is needed, and the oldest it runs them on: newer releases are
// taken as they come (see CheckBun).
const BunVersion = "1.3.14"

// bunRelease is where Bun's releases are; a var for tests.
var bunRelease = "https://github.com/oven-sh/bun/releases/download"

var bunMu sync.Mutex

// bunTarget is the name of Bun's build for this machine: bun-darwin-aarch64
// and the like. x64 Linux and Windows get the baseline build, which runs
// on CPUs without AVX2 as well.
func bunTarget() (string, error) {
	arch := map[string]string{"arm64": "aarch64", "amd64": "x64"}[runtime.GOARCH]
	if arch == "" {
		return "", fmt.Errorf("Bun has no build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "darwin", "linux":
	case "windows":
		if arch != "x64" {
			return "", fmt.Errorf("Bun has no build for windows/%s", runtime.GOARCH)
		}
	default:
		return "", fmt.Errorf("Bun has no build for %s", runtime.GOOS)
	}
	t := "bun-" + runtime.GOOS + "-" + arch
	if arch == "x64" && runtime.GOOS != "darwin" {
		t += "-baseline"
	}
	return t, nil
}

func bunExe() string {
	if runtime.GOOS == "windows" {
		return "bun.exe"
	}
	return "bun"
}

// Bun is the bun to run plugins with: $MAGPIE_BUN when set, else the one
// magpie downloaded and keeps up to date, downloading it now when there
// is none yet.
func Bun(ctx context.Context) (string, error) {
	if b := os.Getenv("MAGPIE_BUN"); b != "" {
		return b, nil
	}
	bunMu.Lock()
	defer bunMu.Unlock()
	v := inUseLocked()
	exe := bunExeOf(v)
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}
	if err := downloadBun(ctx, v, exe); err != nil {
		return "", fmt.Errorf("downloading Bun %s to run plugins: %w", v, err)
	}
	return exe, nil
}

// HasBun is whether a Bun is at hand without a download.
func HasBun() bool {
	if os.Getenv("MAGPIE_BUN") != "" {
		return true
	}
	bunMu.Lock()
	defer bunMu.Unlock()
	return haveBun(inUseLocked())
}

func downloadBun(ctx context.Context, version, exe string) error {
	target, err := bunTarget()
	if err != nil {
		return err
	}
	base := bunRelease + "/bun-v" + version + "/"
	sums, err := getURL(ctx, base+"SHASUMS256.txt", 1<<20)
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == target+".zip" {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s.zip isn't in the release's checksums", target)
	}
	z, err := getURL(ctx, base+target+".zip", 200<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(z)
	if hex.EncodeToString(sum[:]) != want {
		return errors.New("the download's checksum doesn't match the release's")
	}
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != bunExe() || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			return err
		}
		tmp := exe + ".part"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(tmp, exe)
	}
	return fmt.Errorf("%s.zip has no %s", target, bunExe())
}

func getURL(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: too large", url)
	}
	return b, nil
}

// bunCommand runs bun with args in dir, the environment's proxy settings
// passed on.
var bunCommand = func(ctx context.Context, bun, dir string, args ...string) *exec.Cmd {
	cmd := command(ctx, bun, args...)
	cmd.Dir = dir
	cmd.Env = append(env(), "BUN_INSTALL_CACHE_DIR="+filepath.Join(filepath.Dir(catalog.CachePath()), "bun", "install-cache"))
	return cmd
}
