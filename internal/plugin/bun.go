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
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/ghtoken"
	"github.com/yetone/magpie/internal/libc"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/source"
	"github.com/yetone/magpie/internal/stall"
)

// BunVersion is the Bun magpie downloads to run plugins with the first
// time one is needed, and the oldest it runs them on: newer releases are
// taken as they come (see CheckBun). Update bunSums below, testenv.MinBun
// and test.yml's bun-version when it changes.
const BunVersion = "1.4.2"

// bunSums are the SHA-256s of BunVersion's builds, from Bun's own
// SHASUMS256.txt. They let the default Bun be downloaded through a mirror
// when Bun's GitHub release page can't be reached.
var bunSums = map[string]string{
	"bun-darwin-aarch64.zip":             "90987a3a16d7db556d886ac3d551e7b6d3edf0a1cf43acaed622e8676be1d12f",
	"bun-darwin-x64.zip":                 "80520d7e17526308c9185d261679ac6d27798d3803a0e9f7ff9121ab8affb012",
	"bun-linux-aarch64.zip":              "54328bbc2d9c8e0c9f892c544d66c57a83b84139e34909e5ee81758f1ac8fda7",
	"bun-linux-x64-baseline.zip":         "c678040f14fe0440eb839d37cbd0ce4c051a32da72806ac97de6a6aab6bf728f",
	"bun-linux-aarch64-musl.zip":         "71760b6c8ea30623b81a4907cb815d48e2ea266f2e73e751534a44a0607950df",
	"bun-linux-x64-musl-baseline.zip":    "76e1db84e98f22f78de0a87e309bfbbf297732847f9720db36750646c85c8c18",
	"bun-windows-x64-baseline.zip":       "78c221c2376f79731ccf4e4af0b3bb46d81fefa3296c5abee09ad8a1b21e68c6",
	"bun-windows-aarch64.zip":            "a7a16b876a305fd1029c66dbd27007b4f6112ae896532f675878731a21e50cfd",
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

// bunPlatform is this machine as Bun's builds tell them apart: its OS, its
// architecture, and whether it is a Linux on musl (Alpine), where Bun's
// glibc builds can't start (#1877). A var for tests.
var bunPlatform = func() (goos, goarch string, musl bool) {
	return runtime.GOOS, runtime.GOARCH, libc.Musl()
}

// bunTarget is the name of Bun's build for this machine: bun-darwin-aarch64
// and the like. x64 Linux and Windows get the baseline build, which runs
// on CPUs without AVX2 as well; arm64 has no baseline build, on Windows
// either; a musl Linux gets the musl build.
func bunTarget() (string, error) {
	return bunTargetFor(bunPlatform())
}

func bunTargetFor(goos, goarch string, musl bool) (string, error) {
	arch := map[string]string{"arm64": "aarch64", "amd64": "x64"}[goarch]
	if arch == "" {
		return "", fmt.Errorf("Bun has no build for %s/%s", goos, goarch)
	}
	switch goos {
	case "darwin":
	case "linux":
		if musl {
			// arm64 has no baseline build, musl or not
			t := "bun-linux-" + arch + "-musl"
			if arch == "x64" {
				t += "-baseline"
			}
			return t, nil
		}
	case "android":
		t := "bun-linux-" + arch + "-android"
		if arch == "x64" {
			t += "-baseline"
		}
		return t, nil
	case "windows":
		// Windows on ARM has Bun's native bun-windows-aarch64 (#2183)
	default:
		return "", fmt.Errorf("Bun has no build for %s", goos)
	}
	t := "bun-" + goos + "-" + arch
	if arch == "x64" && goos != "darwin" {
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

// missingLibRe finds what a musl Linux's loader says when a program needs
// a library the system lacks: "Error loading shared library
// libstdc++.so.6: No such file or directory (needed by …)".
var missingLibRe = regexp.MustCompile(`Error loading shared library ([^:\s]+)`)

// missingLibs says which libraries a bun that started couldn't be linked
// with lacks, from what it printed, "" when that isn't why it failed. Bun's
// musl build needs libstdc++ and libgcc, which a bare Alpine doesn't have
// (#1877); the loader's first line names them, under a screenful of
// "Error relocating" lines.
func missingLibs(out string) string {
	var libs []string
	for _, m := range missingLibRe.FindAllStringSubmatch(out, -1) {
		if !slices.Contains(libs, m[1]) {
			libs = append(libs, m[1])
		}
	}
	if len(libs) == 0 {
		return ""
	}
	return fmt.Sprintf("this system lacks %s, which Bun needs (on Alpine: apk add libstdc++ libgcc), or set MAGPIE_BUN to a bun that runs here", strings.Join(libs, ", "))
}

// whyDied adds to the error of a host that died starting what the bun it
// ran on says when asked its version, when that is a library it lacks.
func whyDied(ctx context.Context, bun string, err error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, verr := command(ctx, bun, "--version").CombinedOutput()
	if verr == nil {
		return err
	}
	if ue := wontLoad(bun, verr); ue != nil {
		return fmt.Errorf("%w (%v)", ue, err)
	}
	if hint := missingLibs(string(out)); hint != "" {
		return fmt.Errorf("%s: %w", hint, err)
	}
	return err
}

// cantStart says what it means when a bun that is on disk can't be
// started with "no such file or directory": not that the file is missing,
// but that the interpreter it names is. A glibc build on a musl Linux
// (Alpine) names glibc's loader, which isn't there (#1877). A bun that
// Windows or the processor won't load says so (wontLoad). Any other
// error is given back as it is.
func cantStart(bun string, err error) error {
	if ue := wontLoad(bun, err); ue != nil {
		return ue
	}
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if fi, serr := os.Stat(bun); serr != nil || fi.IsDir() {
		return err
	}
	if b := os.Getenv("MAGPIE_BUN"); b != "" && b == bun {
		return fmt.Errorf("this system can't start $MAGPIE_BUN (%s), though it is there: it is likely a build for another C library than this system's (glibc or musl), whose loader isn't here: %w", bun, err)
	}
	target, _ := bunTarget()
	return fmt.Errorf("this system can't start the Bun magpie downloaded (%s, %s), though it is there: the build doesn't match this system's C library (glibc or musl). Set MAGPIE_BUN to a bun built for this system to run plugins on it: %w", target, bun, err)
}

// The statuses Windows ends a program with when it can't load it.
const (
	// a DLL it imports lacks a function it names: Bun 1.3.14 and later
	// import CreatePseudoConsole from kernel32, which Windows has only
	// from Windows 10 1809 and Server 2019 on (zgcwkj on Discord)
	statusEntrypointNotFound = 0xc0000139
	// the processor lacks an instruction it uses
	statusIllegalInstruction = 0xc000001d
)

// BunUnstartableError is a bun this machine can't run at all: Windows is
// older than Bun needs, or the processor lacks an instruction Bun is
// built for. No Bun magpie can download runs here, so an install isn't
// tried again from elsewhere and the GUI says it in the reader's language.
type BunUnstartableError struct {
	Bun string
	// Why is "windows" or "cpu".
	Why string
	Err error
}

func (e *BunUnstartableError) Error() string {
	if e.Why == "windows" {
		return fmt.Sprintf("Bun can't start on this version of Windows: it needs Windows 10 version 1809 or later, or Windows Server 2019 or later, and this one lacks a function it uses (%s exited with 0xc0000139, entry point not found). Update Windows to run plugins, or set MAGPIE_BUN to a bun that runs here: %v", e.Bun, e.Err)
	}
	return fmt.Sprintf("Bun can't start on this computer's processor: it lacks an instruction Bun uses (%s: %v). Bun's x64 build needs SSE4.2 (an Intel or AMD processor from 2008 on; in a virtual machine, a CPU type that passes it through). Run magpie on such a processor to run plugins, or set MAGPIE_BUN to a bun that runs here", e.Bun, e.Err)
}

func (e *BunUnstartableError) Unwrap() error { return e.Err }

// wontLoad is a *BunUnstartableError when err is bun's exit with what
// Windows or the processor says when it can't run it at all: STATUS_
// ENTRYPOINT_NOT_FOUND, STATUS_ILLEGAL_INSTRUCTION, or SIGILL elsewhere.
// Else nil.
func wontLoad(bun string, err error) *BunUnstartableError {
	if err == nil {
		return nil
	}
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		switch uint32(ec.ExitCode()) {
		case statusEntrypointNotFound:
			return &BunUnstartableError{Bun: bun, Why: "windows", Err: err}
		case statusIllegalInstruction:
			return &BunUnstartableError{Bun: bun, Why: "cpu", Err: err}
		}
	}
	var ps interface{ Sys() any }
	if errors.As(err, &ps) {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGILL {
			return &BunUnstartableError{Bun: bun, Why: "cpu", Err: err}
		}
	}
	return nil
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
	if haveBun(v) {
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

// getURLFrom reads url, once more over HTTP/1.1 when the connection broke
// off (netproxy.AgainOverHTTP1): Bun's zip is tens of MB, which a proxy
// that cuts HTTP/2 streams part way breaks on each try over HTTP/2.
func getURLFrom(ctx context.Context, url string, limit int64, mirror bool) ([]byte, error) {
	var b []byte
	err := netproxy.AgainOverHTTP1(ctx, func(ctx context.Context) (err error) {
		b, err = getURLOnce(ctx, url, limit, mirror)
		return err
	})
	return b, err
}

// downloadStall is how long a download from Bun's release waits for its
// next byte; there is no bound on the whole, so a slow link still moving
// finishes (#5572). A var for tests.
var downloadStall = 60 * time.Second

func getURLOnce(ctx context.Context, url string, limit int64, mirror bool) (b []byte, err error) {
	ctx, w := stall.Start(ctx, downloadStall)
	defer w.Stop()
	defer func() { err = w.Err(err) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Bun's newest release is asked of GitHub's API: with the user's
	// GitHub token, as every request magpie makes to it is (#1942)
	res, _, err := ghtoken.Do(req, githubAPI, func(r *http.Request) (*http.Response, error) {
		if mirror {
			return source.Do(http.DefaultClient, r)
		}
		return source.DoOfficial(http.DefaultClient, r)
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	b, err = io.ReadAll(io.LimitReader(w.Reader(res.Body), limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: too large", url)
	}
	return b, nil
}

// bunCommand runs bun with args in dir, the environment's proxy settings
// passed on (a SOCKS5 one bridged, as Bun can't use it: #1409), the roots
// the system trusts added to Bun's (caEnv), and the npm registry in use
// where bun wouldn't find it itself (registryEnv): one set in magpie, the
// login shell's, or npm's registry in China with the 「国内镜像」 switch on
// (its packages are npm's own, checked against the integrity npm gave).
var bunCommand = func(ctx context.Context, bun, dir string, args ...string) *exec.Cmd {
	cmd := command(ctx, bun, args...)
	cmd.Dir = dir
	cmd.Env = append(netproxy.EnvForBun(env()), "BUN_INSTALL_CACHE_DIR="+installCacheDir())
	cmd.Env = append(cmd.Env, caEnv(cmd.Env, dir)...)
	cmd.Env = append(cmd.Env, registryEnv(cmd.Env)...)
	return cmd
}
