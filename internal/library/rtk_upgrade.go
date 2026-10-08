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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/source"
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

// CheckLatest fills in rtk's latest release (see RTKLatest) and, when it
// is newer than this rtk, whether the package manager that installed it has
// it yet (Waiting): winget and Homebrew take days to have a release GitHub
// has, and until they do an upgrade through them changes nothing (#1025).
func (v *RTKView) CheckLatest() {
	if v.Path == "" {
		return
	}
	v.Latest = RTKLatest()
	if v.Latest == "" || v.Version == "" || !newer(v.Latest, v.Version) {
		return
	}
	c := upgraderOf(v.Path)
	ch := rtkChannelName(c)
	if ch == "" {
		return
	}
	// what it has, when it said: not knowing leaves Upgrade offered
	if has := rtkChannelLatest(c); has != "" && !newer(has, v.Version) {
		v.Waiting, v.WaitingHas = ch, has
	}
}

// upgraderOf is rtkUpgrader; a var so tests can give an rtk winget's
// upgrade on any platform, with a fake winget.
var upgraderOf = rtkUpgrader

// rtkChannelName is the package manager upgrade c runs, as the page names
// it, when it is one whose rtk can lag behind rtk's GitHub release; "" for
// cargo (built from rtk's own repository) and rtk's script (its releases).
func rtkChannelName(c []string) string {
	if len(c) == 0 {
		return ""
	}
	switch c[0] {
	case "brew":
		return "Homebrew"
	case "winget":
		return "winget"
	}
	return ""
}

// rtkChannels is the newest rtk each package manager has, as last asked or
// as an upgrade through it left: good for six hours, a failure (not known)
// for fifteen minutes.
var rtkChannels = struct {
	sync.Mutex
	m map[string]rtkChannel
}{m: map[string]rtkChannel{}}

type rtkChannel struct {
	v    string
	next time.Time
}

// rtkChannelLatest is the newest rtk the package manager upgrade c runs
// has, "" when it didn't say.
func rtkChannelLatest(c []string) string {
	rtkChannels.Lock()
	defer rtkChannels.Unlock()
	if e, ok := rtkChannels.m[c[0]]; ok && time.Now().Before(e.next) {
		return e.v
	}
	v, err := askRTKChannel(c)
	if err != nil {
		rtkChannels.m[c[0]] = rtkChannel{next: time.Now().Add(15 * time.Minute)}
		return ""
	}
	rtkChannels.m[c[0]] = rtkChannel{v: v, next: time.Now().Add(6 * time.Hour)}
	return v
}

// rtkChannelHas records what an upgrade through c found: the newest rtk it
// has is the one now installed.
func rtkChannelHas(c []string, v string) {
	if rtkChannelName(c) == "" || v == "" {
		return
	}
	rtkChannels.Lock()
	rtkChannels.m[c[0]] = rtkChannel{v: v, next: time.Now().Add(6 * time.Hour)}
	rtkChannels.Unlock()
}

// askRTKChannel asks Homebrew (as it last updated itself, as the Agents
// page's update does) or winget (its source, as winget upgrade would) for
// the newest rtk it has. Nothing is installed.
func askRTKChannel(c []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var args []string
	switch c[0] {
	case "brew":
		args = []string{"info", "--json=v2", "rtk"}
	case "winget":
		args = []string{"show", "--id", "rtk-ai.rtk", "--exact", "--accept-source-agreements", "--disable-interactivity"}
	default:
		return "", errors.New("no package manager")
	}
	cmd := proc.CommandContext(ctx, c[0], args...)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return rtkChannelVersion(c[0], string(out))
}

// rtkChannelVersion reads the version out of brew info --json=v2 or winget
// show. winget's labels are in the system's language ("Version:",
// "版本:"), so its version is the first one after the line naming the
// package, which comes right before it.
func rtkChannelVersion(tool, out string) (string, error) {
	if tool == "brew" {
		var r struct {
			Formulae []struct {
				Versions struct{ Stable string }
			}
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			return "", err
		}
		if len(r.Formulae) > 0 {
			if m := semver.FindStringSubmatch(" " + r.Formulae[0].Versions.Stable); m != nil {
				return m[1], nil
			}
		}
		return "", errors.New("brew info says no version")
	}
	_, rest, ok := strings.Cut(out, "[rtk-ai.rtk]")
	if !ok {
		return "", errors.New("winget show doesn't name rtk-ai.rtk")
	}
	for _, l := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		if m := semver.FindStringSubmatch(l); m != nil {
			return m[1], nil
		}
		break // the version is the line after the package's
	}
	return "", errors.New("winget show says no version")
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
	if resp, err := source.Do(c, req); err == nil {
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
	resp, err := source.Do(c, req)
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
		// its own script, told the folder it is in (RTK_INSTALL_DIR): the
		// one a link to it points into (Put RTK on PATH's), not the link's
		if st, err := os.Lstat(bin); err == nil && st.Mode()&os.ModeSymlink != 0 {
			dir = filepath.Dir(real)
		}
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
	c := upgraderOf(bin)
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
	// winget having no newer rtk than this one yet is no failure
	none := false
	if _, err := runInstaller(c, timeout, env...); err != nil {
		var ie *installError
		if none = errors.As(err, &ie) && c[0] == "winget" && uint32(ie.code) == wingetNoUpgrade; !none {
			return nil, err
		}
	}
	v := ReadRTK()
	// the package manager just installed the newest it has
	rtkChannelHas(c, v.Version)
	v.CheckLatest()
	if v.Latest != "" && newer(v.Latest, v.Version) {
		switch {
		case c[0] == "brew":
			v.Note = fmt.Sprintf("Homebrew's RTK is %s so far; RTK %s is out, and Homebrew usually has it within a few days", v.Version, v.Latest)
		case c[0] == "winget" && v.Version == before:
			v.Note = fmt.Sprintf("winget's RTK is %s so far; RTK %s is out, and winget usually has it within a few days", v.Version, v.Latest)
		case v.Version == before:
			v.Note = fmt.Sprintf("RTK is still %s after %s; RTK %s is out", v.Version, shown(c), v.Latest)
		}
	}
	if none && v.Note == "" {
		v.Note = fmt.Sprintf("winget has no newer RTK than %s yet", v.Version)
	}
	return v, nil
}

// runInstaller runs an installer with nothing to answer it. A failure says
// which tool failed, how it ended and what it said that matters — not its
// command line, which was all the page had room for (#741: a red
// "winget upgrade --id rtk-ai.rtk --exact --silent ..." and nothing else).
func runInstaller(c []string, timeout time.Duration, env ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, c[0], c[1:]...)
	cmd.Stdin = nil
	cmd.Env = append(append(os.Environ(), "NONINTERACTIVE=1"), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		code := -1
		var ee *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			err = fmt.Errorf("didn't finish in %s", timeout)
		case errors.As(err, &ee) && ee.ExitCode() != -1:
			code = ee.ExitCode()
		default:
			err = fmt.Errorf("couldn't run: %w", err)
		}
		return string(out), installFailed(c, string(out), code, err)
	}
	return string(out), nil
}

// installError is an installer that failed: code is its exit code, -1 when
// it has none (not started, timed out).
type installError struct {
	code int
	msg  string
}

func (e *installError) Error() string { return e.msg }

// winget's exit codes are HRESULTs (AppInstallerErrors.h); the ones an
// upgrade or install of rtk-ai.rtk meets, said plainly.
const wingetNoUpgrade uint32 = 0x8A15002B // APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE

var wingetSays = map[uint32]string{
	wingetNoUpgrade: "winget has no newer RTK than this one yet",
	0x8A150014:      "winget finds no rtk-ai.rtk package",
	0x8A150050:      "winget can't tell which version of RTK is installed, so it won't upgrade it",
	0x8A150101:      "RTK is in use — close the agents running it and try again",
	0x8A150111:      "RTK is in use — close the agents running it and try again",
	0x8A150052:      "winget couldn't put the new rtk.exe in place — if an agent is running rtk, close it and try again",
	0x8A150107:      "winget couldn't reach the network",
	0x8A150008:      "winget couldn't download RTK",
	0x8A150011:      "the download didn't match winget's checksum",
	0x8A150045:      "winget couldn't open its package source",
	0x8A150046:      "winget's source agreements weren't accepted",
	0x8A15003A:      "a policy on this machine blocks winget",
	0x8A15010F:      "a policy on this machine blocks the install",
	0x8A15010C:      "the install was cancelled",
}

// exitCode is a process's exit code as its platform writes it: a Windows
// HRESULT (winget's) in hex, anything else in decimal.
func exitCode(code int) string {
	if code < 0 || code > 0xFFFF {
		return fmt.Sprintf("0x%08X", uint32(code))
	}
	return fmt.Sprint(code)
}

// installFailed is the error for installer c, which printed out and ended
// with exit code code, or -1 and err when it didn't end on its own.
func installFailed(c []string, out string, code int, err error) error {
	tool := c[0]
	if tool == "sh" {
		tool = "RTK's install script"
	}
	ie := &installError{code: code}
	if code == -1 {
		ie.msg = fmt.Sprintf("%s %v", tool, err)
	} else {
		ie.msg = fmt.Sprintf("%s failed (exit code %s)", tool, exitCode(code))
		if c[0] == "winget" {
			if s := wingetSays[uint32(code)]; s != "" {
				ie.msg += ": " + s
			}
		}
	}
	if said := installerSaid(out, 3); said != "" {
		ie.msg += " — " + said
	}
	return ie
}

var (
	ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// a line that is only a spinner frame or a progress bar (winget's
	// ██████▒▒▒ 1.20 MB / 3.40 MB, Homebrew's ###, curl's %)
	progressLine = regexp.MustCompile(`^(?:[-\\|/]|[█▓▒░#=>.\s]*\d*(?:\.\d+)?\s*(?:[KMG]i?B\s*/\s*\d+(?:\.\d+)?\s*[KMG]i?B|%)?)$`)
)

// installerSaid is the last lines an installer printed that say something:
// without the spinner frames and progress bars winget writes to a pipe,
// each redrawn after a carriage return, or its licence boilerplate.
func installerSaid(out string, n int) string {
	out = strings.ToValidUTF8(ansiSeq.ReplaceAllString(out, ""), "")
	var keep []string
	for _, l := range strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' }) {
		l = strings.TrimSpace(l)
		if l == "" || progressLine.MatchString(l) || strings.ContainsAny(l, "█▒") ||
			strings.HasPrefix(l, "This application is licensed to you by its owner") ||
			strings.HasPrefix(l, "Microsoft is not responsible for") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	s := strings.Join(keep, " ")
	if r := []rune(s); len(r) > 400 {
		s = "…" + string(r[len(r)-400:])
	}
	return s
}
