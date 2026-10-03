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
	"github.com/yetone/magpie/internal/source"
)

// BunVersion is the Bun magpie downloads to run plugins with the first
// time one is needed, and the oldest it runs them on: newer releases are
// taken as they come (see CheckBun). Update bunSums below when it changes.
const BunVersion = "1.4.2"

// bunSums are the SHA-256s of BunVersion's builds, from Bun's own
// SHASUMS256.txt. They let the default Bun be downloaded through a mirror
// when Bun's GitHub release page can't be reached.
var bunSums = map[string]string{
	"bun-darwin-aarch64.zip":             "90987a3a16d7db556d886ac3d551e7b6d3edf0a1cf43acaed622e8676be1d12f",
	"bun-darwin-x64.zip":                 "80520d7e17526308c9185d261679ac6d27798d3803a0e9f7ff9121ab8affb012",
	"bun-linux-aarch64.zip":              "54328bbc2d9c8e0c9f892c544d66c57a83b84139e34909e5ee81758f1ac8fda7",
	"bun-linux-x64-baseline.zip":         "c678040f14fe0440eb839d37cbd0ce4c051a32da72806ac97de6a6aab6bf728f",
	"bun-windows-x64-baseline.zip":       "78c221c2376f79731ccf4e4af0b3bb46d81fefa3296c5abee09ad8a1b21e68c6",
	"bun-linux-aarch64-android.zip":      "a1c7e2983f1bb65146beb256a4d72449f23042412bc2cf278aa6397ba27e0274",
	"bun-linux-x64-android-baseline.zip": "fe36d8d4795e0eadc22fb6696d44d168491c2e5b9b7cbb12b8c96b0c0c40a4f9",
}

func bunChecksum(version, target string) string {
	if version != BunVersion {
		return ""
	}
	return bunSums[target+".zip"]
}

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
	case "android":
		t := "bun-linux-" + arch + "-android"
		if arch == "x64" {
			t += "-baseline"
		}
		return t, nil
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
	want := bunChecksum(version, target)
	if want == "" {
		sums, err := getURLOfficial(ctx, base+"SHASUMS256.txt", 1<<20)
		if err != nil {
			return err
		}
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
	return getURLFrom(ctx, url, limit, true)
}

func getURLOfficial(ctx context.Context, url string, limit int64) ([]byte, error) {
	return getURLFrom(ctx, url, limit, false)
}

func getURLFrom(ctx context.Context, url string, limit int64, mirror bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	var res *http.Response
	if mirror {
		res, err = source.Do(http.DefaultClient, req)
	} else {
		res, err = source.DoOfficial(http.DefaultClient, req)
	}
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
