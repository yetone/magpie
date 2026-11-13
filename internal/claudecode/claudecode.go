// Package claudecode finds the Claude Code magpie's Claude subscription
// runs, and downloads Anthropic's own build of it when the user asks, for a
// machine with none: a server or a container (Jorben on Discord).
//
// The build comes from where Anthropic's installer (claude.ai/install.sh)
// takes it: the release's manifest.json names each platform's SHA-256 and
// size, and the binary is checked against both before it is kept. It is
// kept in magpie's cache folder, not on PATH: it is what the bridge, the
// sign-in and `magpie claude` run, and the Agents page doesn't take
// ~/.claude for an uninstall's leftover while it is here (agent's
// cliHeld). While magpie serves, it
// keeps that download at the newest release (KeepCurrent), as Claude
// Code's own updater would, which magpie's runs turn off.
package claudecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/libc"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/stall"
)

// Base is where Anthropic publishes Claude Code's builds, as its installer
// downloads them; a var for tests.
var Base = "https://downloads.claude.ai/claude-code-releases"

// Root is the folder magpie keeps the Claude Code it downloaded in, one
// folder per version.
func Root() string { return filepath.Join(appdir.Cache(), "claude-code") }

func exeName(goos string) string {
	if goos == "windows" {
		return "claude.exe"
	}
	return "claude"
}

var versionRe = lazyre.New(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// Downloaded is the Claude Code magpie downloaded and its version, or ""
// when there is none: the newest of Root's versions that has the binary.
func Downloaded() (path, version string) {
	ents, err := os.ReadDir(Root())
	if err != nil {
		return "", ""
	}
	for _, e := range ents {
		v := e.Name()
		if !e.IsDir() || !versionRe.MatchString(v) {
			continue
		}
		p := filepath.Join(Root(), v, exeName(runtime.GOOS))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		if version == "" || newer(v, version) {
			path, version = p, v
		}
	}
	return path, version
}

// newer is whether version a is after b (numbers compared one by one).
func newer(a, b string) bool {
	pa, pb := strings.SplitN(strings.SplitN(a, "-", 2)[0], ".", 3), strings.SplitN(strings.SplitN(b, "-", 2)[0], ".", 3)
	for i := range 3 {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x > y
		}
	}
	return a > b
}

// Find is the Claude Code to run on this machine: the user's own (on PATH
// or where its installers put it, proc.FindTool), else the one magpie
// downloaded; "" when there is neither.
func Find() string {
	if p := proc.FindTool("claude"); p != "" {
		return p
	}
	p, _ := Downloaded()
	return p
}

// InstallHint is what an error about a missing Claude Code adds: how to
// have magpie download it.
const InstallHint = "or run `magpie claude-code install` to download Anthropic's own build for magpie"

// Platform is the name Anthropic's manifest gives the build for goos/goarch
// (musl: a Linux whose C library is musl, as Alpine's).
func Platform(goos, goarch string, musl bool) (string, error) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	if arch == "" {
		return "", fmt.Errorf("Claude Code has no build for %s/%s", goos, goarch)
	}
	switch goos {
	case "darwin":
		return "darwin-" + arch, nil
	case "linux":
		if musl {
			return "linux-" + arch + "-musl", nil
		}
		return "linux-" + arch, nil
	case "windows":
		return "win32-" + arch, nil
	}
	return "", fmt.Errorf("Claude Code has no build for %s", goos)
}

// Release is a build of Claude Code for this machine, as the manifest
// names it.
type Release struct {
	Version  string
	Platform string
	URL      string
	Checksum string // SHA-256, hex
	Size     int64
}

// Latest asks for the newest release (the "latest" channel, Claude Code's
// own default) and its build for this machine.
func Latest(ctx context.Context) (Release, error) {
	return latest(ctx, runtime.GOOS, runtime.GOARCH, libc.Musl())
}

func latest(ctx context.Context, goos, goarch string, musl bool) (Release, error) {
	platform, err := Platform(goos, goarch, musl)
	if err != nil {
		return Release{}, err
	}
	b, err := get(ctx, Base+"/latest", 256)
	if err != nil {
		return Release{}, err
	}
	v := strings.TrimSpace(string(b))
	if !versionRe.MatchString(v) {
		// an HTML page in its place: a region downloads.claude.ai doesn't serve
		return Release{}, fmt.Errorf("%s/latest didn't name a version (got %q): downloads.claude.ai may not be reachable from here", Base, short(v))
	}
	b, err = get(ctx, Base+"/"+v+"/manifest.json", 1<<20)
	if err != nil {
		return Release{}, err
	}
	var m struct {
		Platforms map[string]struct {
			Binary   string `json:"binary"`
			Checksum string `json:"checksum"`
			Size     int64  `json:"size"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return Release{}, fmt.Errorf("Claude Code %s's manifest: %w", v, err)
	}
	p, ok := m.Platforms[platform]
	if !ok {
		return Release{}, fmt.Errorf("Claude Code %s has no %s build", v, platform)
	}
	if ok, _ := regexp.MatchString(`^[0-9a-f]{64}$`, p.Checksum); !ok {
		return Release{}, fmt.Errorf("Claude Code %s's manifest has no checksum for %s", v, platform)
	}
	if p.Size <= 0 {
		return Release{}, fmt.Errorf("Claude Code %s's manifest has no size for %s", v, platform)
	}
	bin := p.Binary
	if bin == "" || strings.ContainsAny(bin, `/\`) {
		bin = exeName(goos)
	}
	return Release{Version: v, Platform: platform, URL: Base + "/" + v + "/" + platform + "/" + bin, Checksum: p.Checksum, Size: p.Size}, nil
}

func short(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
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
		return nil, fmt.Errorf("%s: larger than expected", url)
	}
	return b, nil
}

// Install downloads r into Root, checks its size and SHA-256 against the
// manifest's, and keeps it as the Claude Code magpie runs when the machine
// has none of its own; the versions downloaded before are removed. progress,
// when set, is told the bytes so far. It returns the binary's path.
func Install(ctx context.Context, r Release, progress func(done int64)) (string, error) {
	exe, err := install(ctx, r, progress)
	if err == nil {
		prune(r.Version)
	}
	return exe, err
}

// install is Install without removing the versions before it.
func install(ctx context.Context, r Release, progress func(done int64)) (string, error) {
	dir := filepath.Join(Root(), r.Version)
	if !versionRe.MatchString(r.Version) {
		return "", fmt.Errorf("not a version: %q", r.Version)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	exe := filepath.Join(dir, exeName(runtime.GOOS))
	tmp := exe + ".part"
	if err := download(ctx, r, tmp, progress); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return exe, nil
}

// prune removes every version but kept: what a run takes is the newest, and
// each is ~240 MB. A folder with a download under way (a .part) stays.
// Removing the binary of a Claude Code still running is fine on Linux and
// macOS (the run keeps its file); on Windows that one stays, for the next
// prune.
func prune(kept string) {
	ents, _ := os.ReadDir(Root())
	for _, e := range ents {
		if !e.IsDir() || e.Name() == kept || !versionRe.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(Root(), e.Name())
		if _, err := os.Stat(filepath.Join(dir, exeName(runtime.GOOS)+".part")); err == nil {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

// download fetches r to to, once more over HTTP/1.1 when the connection
// broke off (netproxy.AgainOverHTTP1): a proxy that cuts HTTP/2 streams
// part way carries HTTP/1.1 whole.
func download(ctx context.Context, r Release, to string, progress func(int64)) error {
	return netproxy.AgainOverHTTP1(ctx, func(ctx context.Context) error { return downloadOnce(ctx, r, to, progress) })
}

// downloadStall is how long a download waits for its next byte. There is
// no bound on the whole: a slow link still moving finishes the ~200 MB
// build (#5572). A var for tests.
var downloadStall = 60 * time.Second

func downloadOnce(ctx context.Context, r Release, to string, progress func(int64)) (err error) {
	ctx, w := stall.Start(ctx, downloadStall)
	defer w.Stop()
	defer func() { err = w.Err(err) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", r.URL, res.Status)
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h, progressWriter(progress)), io.LimitReader(w.Reader(res.Body), r.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != r.Size {
		return fmt.Errorf("the download is %d bytes, the manifest says %d", n, r.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != r.Checksum {
		return errors.New("the download's SHA-256 doesn't match the one in Anthropic's manifest")
	}
	return nil
}

type progressFunc func(int64)

func progressWriter(f func(int64)) io.Writer {
	var done int64
	return progressFunc(func(n int64) {
		done += n
		if f != nil {
			f(done)
		}
	})
}

func (f progressFunc) Write(p []byte) (int, error) {
	f(int64(len(p)))
	return len(p), nil
}

// Remove deletes every Claude Code magpie downloaded.
func Remove() error { return os.RemoveAll(Root()) }
