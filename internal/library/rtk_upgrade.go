package library

import (
	"context"
	"encoding/json"
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
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// rtk's version is kept up to date the way it was installed: Homebrew's
// with brew upgrade, winget's with winget upgrade, cargo's with cargo
// install again, and one its own script put in a folder with that script
// again, into the same folder. Its latest release is read from GitHub, as
// its script does.

// rtkReleases redirects to rtk's latest release (…/releases/tag/v0.50.0);
// rtkReleasesAPI says it in JSON, rate-limited, for when that doesn't.
var (
	rtkReleases    = "https://github.com/rtk-ai/rtk/releases/latest"
	rtkReleasesAPI = "https://api.github.com/repos/rtk-ai/rtk/releases/latest"
)

// rtkLatest is the latest release as last read: good for six hours, a
// failure for fifteen minutes.
var rtkLatest struct {
	sync.Mutex
	v    string
	next time.Time
}

// CheckLatest fills in rtk's latest release (see RTKLatest).
func (v *RTKView) CheckLatest() {
	if v.Path != "" {
		v.Latest = RTKLatest()
	}
}

// RTKLatest is rtk's latest release, from GitHub unless it was read lately;
// "" when GitHub can't be reached and never could.
func RTKLatest() string {
	rtkLatest.Lock()
	defer rtkLatest.Unlock()
	if time.Now().Before(rtkLatest.next) {
		return rtkLatest.v
	}
	ver, err := latestRTK()
	if err != nil {
		rtkLatest.next = time.Now().Add(15 * time.Minute)
		return rtkLatest.v // an older answer is better than none
	}
	rtkLatest.v, rtkLatest.next = ver, time.Now().Add(6*time.Hour)
	return ver
}

func latestRTK() (string, error) {
	c := &http.Client{
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequest("HEAD", rtkReleases, nil)
	req.Header.Set("User-Agent", "magpie")
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		if _, tag, ok := strings.Cut(resp.Header.Get("Location"), "/releases/tag/"); ok {
			if m := semver.FindStringSubmatch(tag); m != nil {
				return m[1], nil
			}
		}
	}
	req, _ = http.NewRequest("GET", rtkReleasesAPI, nil)
	req.Header.Set("User-Agent", "magpie")
	withGitHubToken(req)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	if m := semver.FindStringSubmatch(r.Tag); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("no version in %q", r.Tag)
}

// RTKNewer says whether version a is after b, by their numbers.
func RTKNewer(a, b string) bool { return newer(a, b) }

// newer says whether version a is after b, by their numbers.
func newer(a, b string) bool {
	var x, y [3]int
	fmt.Sscanf(a, "%d.%d.%d", &x[0], &x[1], &x[2])
	fmt.Sscanf(b, "%d.%d.%d", &y[0], &y[1], &y[2])
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// rtkUpgrader is how the rtk at bin is brought up to date, by where it was
// installed; nil when magpie can't tell.
func rtkUpgrader(bin string) []string {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		real = bin
	}
	have := func(name string) bool { _, err := exec.LookPath(name); return err == nil }
	slash := filepath.ToSlash(real)
	dir := filepath.Dir(bin)
	switch {
	case strings.Contains(slash, "/Cellar/rtk/") || strings.Contains(slash, "/.linuxbrew/"):
		if have("brew") {
			return []string{"brew", "upgrade", "rtk"}
		}
	case runtime.GOOS == "windows" && strings.Contains(strings.ToLower(slash), "/winget/"):
		if have("winget") {
			return []string{"winget", "upgrade", "--id", "rtk-ai.rtk", "--exact", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
		}
	case dir == filepath.Join(home(), ".cargo", "bin"):
		if have("cargo") {
			return []string{"cargo", "install", "--git", "https://github.com/rtk-ai/rtk", "--force"}
		}
	case runtime.GOOS != "windows":
		// its own script, told the folder it is in (RTK_INSTALL_DIR)
		if have("curl") {
			return []string{"sh", "-c", "curl -fsSL " + rtkScript + " | sh", dir}
		}
	}
	return nil
}

// UpgradeRTK brings rtk up to its latest release the way it was installed.
func UpgradeRTK() (*RTKView, error) {
	rtkMu.Lock()
	defer rtkMu.Unlock()
	bin := rtkPath()
	if bin == "" {
		return nil, fmt.Errorf("rtk isn't installed — install it first (%s)", RTKURL)
	}
	c := rtkUpgrader(bin)
	if c == nil {
		return nil, fmt.Errorf("magpie can't tell how the rtk at %s was installed — update it the way you installed it", bin)
	}
	before := rtkVersion()
	var env []string
	if c[0] == "sh" {
		env = []string{"RTK_INSTALL_DIR=" + c[3]}
		c = c[:3]
	}
	timeout := 10 * time.Minute
	if c[0] == "cargo" {
		timeout = 30 * time.Minute // it builds rtk
	}
	// brew upgrade updates Homebrew first, for its newest rtk
	if _, err := runInstaller(c, timeout, env...); err != nil {
		return nil, err
	}
	v := ReadRTK()
	v.CheckLatest()
	if v.Latest != "" && newer(v.Latest, v.Version) {
		if c[0] == "brew" {
			v.Note = fmt.Sprintf("Homebrew's RTK is %s so far; RTK %s is out, and Homebrew usually has it within a few days", v.Version, v.Latest)
		} else if v.Version == before {
			v.Note = fmt.Sprintf("RTK is still %s after %s; RTK %s is out", v.Version, shown(c), v.Latest)
		}
	}
	return v, nil
}

// runInstaller runs an installer with nothing to answer it, and says what
// it printed last when it fails.
func runInstaller(c []string, timeout time.Duration, env ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, c[0], c[1:]...)
	cmd.Stdin = nil
	cmd.Env = append(append(os.Environ(), "NONINTERACTIVE=1"), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			text = strings.TrimSpace(text + " " + err.Error())
		}
		return "", fmt.Errorf("%s: %s", shown(c), lastLines(text, 4))
	}
	return string(out), nil
}
