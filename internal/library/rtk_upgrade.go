package library

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/source"
	"github.com/yetone/magpie/internal/steady"
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

// CheckLatest fills in rtk's latest release (see RTKLatest), and, when it is
// newer than this rtk, what the package manager that installed it has — but
// only what that said before, and within its own age (Waiting): asking a
// package manager is slow (`winget show` refreshes its sources — seconds, on a
// slow or unreachable network far more), and a read that waits for it holds up
// the whole tab for a grey tag beside the version. CheckChannel asks in the
// background instead, and the next read draws with its answer.
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
	// what it said last: not knowing leaves Upgrade offered, which is what a
	// page drawn before that answer arrives shows
	if has, ok := rtkChannelCached(c); ok && has != "" && !newer(has, v.Version) {
		v.Waiting, v.WaitingHas = ch, has
	}
}

// CheckChannel asks the package manager upgrade runs what rtk it has, and
// keeps the answer for six hours (see rtkChannelLatest), so that the next read
// of the tab draws with it without waiting. It is asked in the background,
// after the tab has gone out: it is a question for the package manager, which
// takes seconds, and it answers nothing anyone needs unless a release newer
// than the one installed is out (#1025).
func CheckChannel(v *RTKView) {
	if v.Path == "" || v.Latest == "" || v.Version == "" || !newer(v.Latest, v.Version) {
		return
	}
	if c := upgraderOf(v.Path); rtkChannelName(c) != "" {
		rtkChannelLatest(c)
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
// for fifteen minutes. asking marks a package manager being asked right now:
// the ask runs outside the lock, which is held only to read and write this
// (its own subprocess takes seconds, and a reader that waited for it would
// hold up the tab: #1025, the tag beside the version).
var rtkChannels = struct {
	sync.Mutex
	m      map[string]rtkChannel
	asking map[string]bool
}{m: map[string]rtkChannel{}, asking: map[string]bool{}}

type rtkChannel struct {
	v    string
	next time.Time
}

// rtkChannelCached is what the package manager said last, and whether it was
// said within its own age, without asking it again. It is what a read that must
// not wait draws with (see RTKView.CheckLatest).
func rtkChannelCached(c []string) (string, bool) {
	rtkChannels.Lock()
	defer rtkChannels.Unlock()
	e, ok := rtkChannels.m[c[0]]
	if !ok || !time.Now().Before(e.next) {
		return "", false
	}
	return e.v, true
}

// rtkChannelLatest is the newest rtk the package manager upgrade c runs
// has, "" when it didn't say. It is a var so tests can count the asks (see
// TestCheckLatestNeverAsksThePackageManager).
//
// What the package manager said last is given back without asking it again,
// and the ask itself runs with the lock let go: `winget show` refreshes its
// sources and takes seconds, and a reader that only wants what is known
// (rtkChannelCached, which draws the card) must not queue behind it. An ask
// already in flight for the same manager is not started twice; it answers
// with what that one said last, or "" while nothing has been said.
var rtkChannelLatest = func(c []string) string {
	rtkChannels.Lock()
	if e, ok := rtkChannels.m[c[0]]; ok && time.Now().Before(e.next) {
		v := e.v
		rtkChannels.Unlock()
		return v
	}
	if rtkChannels.asking[c[0]] {
		v := rtkChannels.m[c[0]].v
		rtkChannels.Unlock()
		return v
	}
	rtkChannels.asking[c[0]] = true
	rtkChannels.Unlock()

	v, err := askRTKChannelNow(c)

	rtkChannels.Lock()
	delete(rtkChannels.asking, c[0])
	if err != nil {
		rtkChannels.m[c[0]] = rtkChannel{next: time.Now().Add(15 * time.Minute)}
		rtkChannels.Unlock()
		return ""
	}
	rtkChannels.m[c[0]] = rtkChannel{v: v, next: time.Now().Add(6 * time.Hour)}
	rtkChannels.Unlock()
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

// askRTKChannelNow is askRTKChannel as the ask runs: a var so a test can make
// it slow without a package manager to run, on any platform (see
// TestRTKReadWhileThePackageManagerIsBeingAsked).
var askRTKChannelNow = askRTKChannel

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
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, _, err := doGitHub(req, func(r *http.Request) (*http.Response, error) { return source.Do(c, r) })
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("couldn't read GitHub's answer: %w", err)
	}
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
	wingetUp := []string{"winget", "upgrade", "--id", "rtk-ai.rtk", "--exact", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
	switch {
	case strings.Contains(slash, "/Cellar/rtk/") || strings.Contains(slash, "/.linuxbrew/"):
		if have("brew") {
			return []string{"brew", "upgrade", "rtk"}
		}
	// winget's own folders: its package, or the link it puts in
	// WinGet\Links to one installed with --location (#1527)
	case rtkOnWindows && (strings.Contains(strings.ToLower(slash), "/winget/") || strings.Contains(strings.ToLower(filepath.ToSlash(bin)), "/winget/")):
		// winget says it has no rtk-ai.rtk installed (0x8A150014, netube99 on
		// X): the exe was put in its folder, or left there, by something
		// else, and winget upgrade would fail. rtk's own release is put over
		// it instead, over the file a WinGet\Links link points to.
		// A link in WinGet\Links to an exe outside winget's folders was made
		// by winget (--location, #1527); only the exe inside them is asked.
		if have("winget") && strings.Contains(strings.ToLower(slash), "/winget/") && wingetDisowns(bin, real) {
			if rtkArch == "amd64" {
				return []string{rtkWindowsRelease, real}
			}
			return nil
		}
		if have("winget") {
			return wingetUp
		}
	// cargo's folder, ~/.cargo/bin or CARGO_HOME's. With no cargo to be
	// found there is no upgrade, and the page says why (RTKView.NoUpgrade):
	// taking it over with rtk's Windows release would leave cargo's own
	// records of it behind
	case inCargoBin(dir):
		if cargo := cargoFor(dir); cargo != "" {
			// the package named: rtk-ai/rtk keeps proxy/ (rtk-proxy) beside
			// rtk, and cargo install --git scans every Cargo.toml in the
			// repository and won't choose between their binaries (#2328)
			return append([]string{cargo}, strings.Fields(rtkCargoCmd)[1:]...)
		}
	case !rtkOnWindows:
		// its own script, told the folder it is in (RTK_INSTALL_DIR): the
		// one a link to it points into (Put RTK on PATH's), not the link's
		if st, err := os.Lstat(bin); err == nil && st.Mode()&os.ModeSymlink != 0 {
			dir = filepath.Dir(real)
		}
		if have("curl") {
			return []string{"sh", "-c", "curl -fsSL " + rtkScript + " | sh", dir}
		}
	// installed with winget --location into a folder of the user's own
	// (D:\codes\tools\rtk): winget's records say so, not the folder's name
	case have("winget") && wingetOwns(bin, real):
		return wingetUp
	// a standalone rtk.exe somewhere else on Windows, put there by hand
	// from rtk's own Windows release (%USERPROFILE%\.local\bin, #2006): no
	// package manager installed it, so magpie replaces it itself. winget's
	// and cargo's folders were taken above, and an rtk winget's records or
	// winget list say is winget's went to winget; when winget couldn't say
	// (it timed out), magpie still can't tell rather than take it over.
	// rtk's Windows release has no arm64 build, so on one it can't either
	case rtkOnWindows && rtkArch == "amd64" &&
		strings.EqualFold(filepath.Base(bin), "rtk.exe") &&
		wingetDisowns(bin, real):
		return []string{rtkWindowsRelease, bin}
	}
	return nil
}

// rtkCargoCmd is how cargo upgrades rtk, as a terminal would run it.
// rtk-ai/rtk's default branch is develop, whose rtk is behind the release:
// on 10-27 develop's Cargo.toml said 0.49.0 while v1.1.0 was out, built
// from master, so an upgrade without the branch put 0.49.0 in place of a
// newer rtk, or left it as it was. rtk's own installation guide names
// master, as this does.
const rtkCargoCmd = "cargo install --git https://github.com/rtk-ai/rtk --branch master rtk --force"

// cargoBins are the folders cargo install puts programs in: CARGO_HOME's
// bin when it is set (rustup's installer and many guides move it off C:,
// to D:\Rust\.cargo), and ~/.cargo/bin, its default.
func cargoBins() []string {
	var dirs []string
	if h := os.Getenv("CARGO_HOME"); filepath.IsAbs(h) {
		dirs = append(dirs, filepath.Join(h, "bin"))
	}
	return append(dirs, filepath.Join(home(), ".cargo", "bin"))
}

// inCargoBin says whether dir is one of cargo's folders; case aside on
// Windows, where C:\Users\Me and c:\users\me are the same folder.
func inCargoBin(dir string) bool {
	for _, d := range cargoBins() {
		if filepath.Clean(dir) == filepath.Clean(d) || rtkOnWindows && strings.EqualFold(filepath.Clean(dir), filepath.Clean(d)) {
			return true
		}
	}
	return false
}

// cargoFor is the cargo that upgrades the rtk in cargo's folder dir: the one
// on magpie's PATH, by name, else the one rustup put in that same folder
// beside rtk, by its path. A magpie started before Rust was installed, or
// with a PATH without cargo's folder (rustup adds it for terminals opened
// after), found no cargo by name, and the rtk cargo installed had no
// upgrade at all: its Upgrade button did nothing (新v学员, 10-27). "" when
// there is no cargo in either.
func cargoFor(dir string) string {
	if _, err := exec.LookPath("cargo"); err == nil {
		return "cargo"
	}
	name := "cargo"
	if rtkOnWindows {
		name = "cargo.exe"
	}
	if p := filepath.Join(dir, name); exists(p) {
		return p
	}
	return ""
}

// isCargo says whether installer c is cargo, by name or by its path.
func isCargo(c []string) bool {
	if len(c) == 0 {
		return false
	}
	name := c[0][strings.LastIndexAny(c[0], `/\`)+1:]
	return strings.TrimSuffix(strings.ToLower(name), ".exe") == "cargo"
}

// rtkNoUpgrade is why the rtk at bin has no upgrade, for the page to say
// when a newer release is out (RTKView.NoUpgrade): "cargo" when cargo
// installed it but magpie finds no cargo to run, "" otherwise.
func rtkNoUpgrade(bin string) string {
	if inCargoBin(filepath.Dir(bin)) && cargoFor(filepath.Dir(bin)) == "" {
		return "cargo"
	}
	return ""
}

// rtkWindowsRelease is the upgrader rtkUpgrader gives a Windows rtk no
// package manager installed (#2006): magpie replaces it with rtk.exe out of
// rtk's own Windows release. The action names only the binary to replace:
// the release is RTKLatest's when the upgrade runs, since naming it here
// would have ReadRTK (a page draw) wait on RTKLatest's lock while it asks
// GitHub.
const rtkWindowsRelease = "rtk-windows-release"

// rtkDownloadBase is where rtk's releases are downloaded from; a var so
// tests can point it at a fake release.
var rtkDownloadBase = "https://github.com/rtk-ai/rtk/releases/download"

// rtkDownloadMax is the largest file magpie reads from a release: rtk's
// Windows zip is a few MB.
const rtkDownloadMax = 200 << 20

// rtkArch is the architecture rtk's Windows release is chosen for: magpie's
// own, which Windows on arm64 runs emulated when it is amd64. A var so a
// test runs the amd64 branch on any machine.
var rtkArch = runtime.GOARCH

// installRTKWindowsRelease replaces bin with rtk.exe out of rtk's Windows
// release v version (#2006). The zip and the checksums.txt it is checked
// against both come from rtk's own release (source.DoOfficial), so a
// download that doesn't match is a real failure and nothing is replaced.
func installRTKWindowsRelease(version, bin string) error {
	const asset = "rtk-x86_64-pc-windows-msvc.zip"
	base := rtkDownloadBase + "/v" + version + "/"
	sums, err := rtkFetch(base + "checksums.txt")
	if err != nil {
		return fmt.Errorf("couldn't read RTK %s's checksums: %w", version, err)
	}
	want := rtkChecksum(string(sums), asset)
	if want == "" {
		return fmt.Errorf("RTK %s's checksums don't list %s", version, asset)
	}
	z, err := rtkFetch(base + asset)
	if err != nil {
		return fmt.Errorf("couldn't download RTK %s: %w", version, err)
	}
	if got := sha256.Sum256(z); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("the RTK %s download doesn't match its checksum from rtk-ai/rtk; %s left as it was", version, filepath.Base(bin))
	}
	exe, err := rtkExeFromZip(z)
	if err != nil {
		return fmt.Errorf("couldn't read %s from RTK %s: %w", asset, version, err)
	}
	return replaceRTKBinary(bin, exe)
}

// rtkFetch is a release file from its official address (source.DoOfficial:
// the file's checksum must come from the same trusted place as the file).
func rtkFetch(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "magpie")
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := source.DoOfficial(c, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, rtkDownloadMax+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > rtkDownloadMax {
		return nil, fmt.Errorf("%s: too large", url)
	}
	return b, nil
}

// rtkChecksum is the SHA-256 checksums.txt gives for name, "" when it
// doesn't name it. The lines are "sha256  name", as sha256sum writes them.
func rtkChecksum(sums, name string) string {
	for _, l := range strings.FieldsFunc(sums, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if f := strings.Fields(l); len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// rtkExeFromZip is rtk.exe out of rtk's Windows release zip (beside it,
// LICENSE and LICENSE-ENGINE in v1.0.1). Only a member named rtk.exe is
// taken: another exe in it is not rtk.
func rtkExeFromZip(z []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		return nil, err
	}
	var pick *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Base(f.Name), "rtk.exe") {
			pick = f
			break
		}
	}
	if pick == nil {
		return nil, errors.New("it has no rtk.exe")
	}
	if pick.UncompressedSize64 > rtkDownloadMax {
		return nil, errors.New("its rtk.exe is too large")
	}
	rc, err := pick.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, rtkDownloadMax))
}

// replaceRTKBinary puts b in bin's place, written beside it first and with
// the old one moved aside rather than removed: on Windows an rtk.exe an
// agent is running refuses to be replaced outright (steady.Rename waits it
// out), and a replacement that fails puts the user's rtk back, so it is
// never lost (#2006).
func replaceRTKBinary(bin string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(bin), "."+filepath.Base(bin)+".*")
	if err != nil {
		return fmt.Errorf("couldn't write the new rtk beside %s: %w", bin, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("couldn't write the new rtk beside %s: %w", bin, err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("couldn't write the new rtk beside %s: %w", bin, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("couldn't write the new rtk beside %s: %w", bin, err)
	}
	// the old binary renamed, not removed: a rename that fails leaves it.
	// One an agent was running when it was moved aside can't be removed
	// until it exits, so the ones earlier upgrades left are removed now
	removeOldRTKs(bin)
	aside := bin + ".old-" + strconv.Itoa(os.Getpid())
	if err := steady.Rename(bin, aside); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("couldn't move the rtk at %s aside to replace it: %w", bin, err)
	}
	if err := steady.Rename(tmpName, bin); err != nil {
		os.Remove(tmpName)
		if rerr := steady.Rename(aside, bin); rerr != nil {
			return fmt.Errorf("couldn't put the new rtk in %s (%v), and couldn't put the old one back from %s: %w", bin, err, aside, rerr)
		}
		return fmt.Errorf("couldn't put the new rtk in %s: %w", bin, err)
	}
	os.Remove(aside)
	return nil
}

// rtkOnWindows is whether rtk is upgraded as Windows has it; a var so a
// test can ask a fake winget on any platform.
var rtkOnWindows = runtime.GOOS == "windows"

// wingetRecords is where winget's records say it put rtk-ai.rtk: the
// InstallLocation of its entries among Windows' installed programs, which
// winget writes for a portable package like rtk's, wherever --location
// put it. ok is false when they can't be read (not Windows). A var for
// tests.
var wingetRecords = rtkWingetLocations

// rtkWingetOwned is whether winget installed the rtk at a path, as last
// asked: asking can run winget, which takes seconds, and the Library page
// reads rtk's card each time it is drawn.
var rtkWingetOwned = struct {
	sync.Mutex
	m map[string]rtkOwned
}{m: map[string]rtkOwned{}}

type rtkOwned struct {
	yes, known bool
	next       time.Time
}

// wingetOwns says whether winget installed the rtk at bin (real, with links
// followed); no when winget couldn't say.
func wingetOwns(bin, real string) bool {
	yes, _ := wingetOwnsRTK(bin, real)
	return yes
}

// wingetDisowns says whether winget said it didn't install the rtk at bin:
// winget couldn't say (it timed out, failed or couldn't run) is no answer, and an
// rtk that may be winget's isn't taken over (#2006).
func wingetDisowns(bin, real string) bool {
	yes, known := wingetOwnsRTK(bin, real)
	return known && !yes
}

// wingetOwnsRTK says whether winget installed the rtk at bin (real, with
// links followed), and whether that is known, remembered for ten minutes.
func wingetOwnsRTK(bin, real string) (yes, known bool) {
	key := strings.ToLower(real)
	rtkWingetOwned.Lock()
	defer rtkWingetOwned.Unlock()
	if o, ok := rtkWingetOwned.m[key]; ok && time.Now().Before(o.next) {
		return o.yes, o.known
	}
	yes, known = askWingetOwns(bin, real)
	rtkWingetOwned.m[key] = rtkOwned{yes: yes, known: known, next: time.Now().Add(10 * time.Minute)}
	return yes, known
}

// askWingetOwns asks winget's records whether the rtk at bin is the one it
// installed: the folder its record names holds it, so an rtk.exe put
// somewhere else by hand isn't taken for winget's. When there is no record
// to read, winget list --id rtk-ai.rtk is asked, briefly, whether winget
// has rtk installed at all. known is false when neither could say: winget
// is there but timed out or couldn't be run.
func askWingetOwns(bin, real string) (yes, known bool) {
	if locs, ok := wingetRecords(); ok && len(locs) > 0 {
		for _, l := range locs {
			if inFolder(real, l) || inFolder(bin, l) {
				return true, true
			}
			// real has its links followed, so the record's folder is too:
			// a folder reached through a link or junction (macOS's /var is
			// /private/var) is the same folder
			if lr, err := filepath.EvalSymlinks(l); err == nil && lr != l && inFolder(real, lr) {
				return true, true
			}
		}
		return false, true
	}
	if _, err := exec.LookPath("winget"); err != nil {
		return false, true // no winget, so not winget's
	}
	ctx, cancel := context.WithTimeout(context.Background(), wingetListTimeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, "winget", "list", "--id", "rtk-ai.rtk", "--exact", "--accept-source-agreements", "--disable-interactivity")
	cmd.Stdin = nil
	out, err := cmd.Output()
	// it lists the package by its id in any language, and says it has none
	// with exit code 0x8A150014; any other failure (out of time, its
	// sources unreachable) said nothing
	if err == nil {
		return strings.Contains(string(out), "rtk-ai.rtk"), true
	}
	var ee *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &ee) && uint32(ee.ExitCode()) == wingetNotFound {
		return false, true
	}
	return false, false
}

// wingetListTimeout is how long winget list is given; a var for tests.
var wingetListTimeout = 15 * time.Second

// inFolder says whether path is folder dir or inside it, case aside, as
// Windows has paths.
func inFolder(path, dir string) bool {
	if dir == "" {
		return false
	}
	p, d := strings.ToLower(filepath.Clean(path)), strings.ToLower(filepath.Clean(dir))
	return p == d || strings.HasPrefix(p, strings.TrimRight(d, `\/`)+string(filepath.Separator))
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
		if rtkNoUpgrade(bin) == "cargo" {
			return nil, fmt.Errorf("the rtk at %s was installed with cargo, but magpie can't find cargo — install Rust from https://rustup.rs, or run %s in a terminal", bin, rtkCargoCmd)
		}
		return nil, fmt.Errorf("magpie can't tell how the rtk at %s was installed — update it the way you installed it", bin)
	}
	before := rtkVersion()
	none := false
	if c[0] == rtkWindowsRelease {
		// no package manager and no installer to run: magpie downloads
		// rtk's own Windows release and replaces the binary itself (#2006)
		version := RTKLatest()
		if version == "" {
			return nil, fmt.Errorf("magpie can't tell which RTK release to install for %s — check the network and try again", bin)
		}
		if err := installRTKWindowsRelease(version, c[1]); err != nil {
			return nil, err
		}
	} else {
		var env []string
		if c[0] == "sh" {
			env = []string{"RTK_INSTALL_DIR=" + c[3]}
			c = c[:3]
		}
		timeout := 10 * time.Minute
		if isCargo(c) {
			timeout = 30 * time.Minute // it builds rtk
		}
		run := func() error {
			_, err := runRTKInstaller(c, timeout, rtkAdminFolder(c, bin), env...)
			return err
		}
		if isCargo(c) {
			run = func() error { return runCargoRTK(c, bin, timeout) }
		}
		// brew upgrade updates Homebrew first, for its newest rtk
		// winget having no newer rtk than this one yet is no failure
		if err := run(); err != nil {
			var ie *installError
			if none = errors.As(err, &ie) && c[0] == "winget" && uint32(ie.code) == wingetNoUpgrade; !none {
				return nil, err
			}
		}
	}
	v := ReadRTK()
	// an upgrade through a package manager leaves it with the newest it
	// has; the release path above has no package manager to record for
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

// runCargoRTK runs cargo's upgrade c of the rtk at bin. On Windows cargo
// can't write over an rtk.exe that is running (os error 5), and one is
// whenever an agent's command runs through RTK — an rtk cargo test runs as
// long as the tests do — so its upgrade failed once the build was done.
// Windows does let a running rtk.exe be renamed: when cargo is refused, the
// rtk.exe is moved aside, a copy of it put in its place for the agents to
// run meanwhile, and cargo run again, which builds nothing again (its build
// is kept in a folder of magpie's, CARGO_TARGET_DIR, removed after) and only
// installs over the copy.
func runCargoRTK(c []string, bin string, timeout time.Duration) error {
	var env []string
	if dir, err := os.MkdirTemp("", "magpie-rtk-cargo-"); err == nil {
		defer os.RemoveAll(dir)
		env = append(env, "CARGO_TARGET_DIR="+dir)
	}
	removeOldRTKs(bin)
	out, err := runPlain(c, timeout, env...)
	if err == nil || !rtkOnWindows || !cargoInUse(out) {
		return err
	}
	aside, merr := copyRTKAside(bin)
	if merr != nil {
		return err
	}
	if _, err := runPlain(c, timeout, env...); err != nil {
		return err
	}
	os.Remove(aside) // still running: removed by the next upgrade
	return nil
}

// cargoInUse says whether cargo install failed because the rtk it was to
// replace is running: it couldn't move the new one over it, refused with
// os error 5 (access denied) or 32 (in use). cargo prints the system's
// own words before the code, in its language: only the code is read.
func cargoInUse(out string) bool {
	return strings.Contains(out, "failed to move") &&
		(strings.Contains(out, "(os error 5)") || strings.Contains(out, "(os error 32)"))
}

// copyRTKAside moves the rtk at bin aside, to bin.old-<pid>, and puts a copy
// of it back at bin: for an install to write over the copy while what runs
// the old one goes on running it. Should the copy fail, the rtk is moved
// back.
func copyRTKAside(bin string) (string, error) {
	aside := bin + ".old-" + strconv.Itoa(os.Getpid())
	b, err := os.ReadFile(bin)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(bin)
	if err != nil {
		return "", err
	}
	if err := steady.Rename(bin, aside); err != nil {
		return "", err
	}
	if err := os.WriteFile(bin, b, st.Mode().Perm()); err != nil {
		os.Remove(bin)
		if rerr := steady.Rename(aside, bin); rerr != nil {
			return "", fmt.Errorf("couldn't put the rtk at %s back from %s: %w", bin, aside, rerr)
		}
		return "", err
	}
	return aside, nil
}

// removeOldRTKs removes the rtks earlier upgrades moved aside from bin's
// folder: one an agent was running when it was moved can't be removed
// until it exits.
func removeOldRTKs(bin string) {
	ents, err := os.ReadDir(filepath.Dir(bin))
	if err != nil {
		return
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), filepath.Base(bin)+".old-") {
			os.Remove(filepath.Join(filepath.Dir(bin), e.Name()))
		}
	}
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
const (
	wingetNoUpgrade uint32 = 0x8A15002B // APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE
	wingetNotFound  uint32 = 0x8A150014 // APPINSTALLER_CLI_ERROR_NO_APPLICATIONS_FOUND
)

var wingetSays = map[uint32]string{
	wingetNoUpgrade:  "winget has no newer RTK than this one yet",
	wingetNotFound:   "winget finds no rtk-ai.rtk package",
	wingetNeedsAdmin: "winget needs an administrator for this RTK, installed for every user of this computer",
	0x8A150050:       "winget can't tell which version of RTK is installed, so it won't upgrade it",
	0x8A150101:       "RTK is in use — close the agents running it and try again",
	0x8A150111:       "RTK is in use — close the agents running it and try again",
	0x8A150052:       "winget couldn't put the new rtk.exe in place — if an agent is running rtk, close it and try again",
	0x8A150107:       "winget couldn't reach the network",
	0x8A150008:       "winget couldn't download RTK",
	0x8A150011:       "the download didn't match winget's checksum",
	0x8A150045:       "winget couldn't open its package source",
	0x8A150046:       "winget's source agreements weren't accepted",
	0x8A15003A:       "a policy on this machine blocks winget",
	0x8A15010F:       "a policy on this machine blocks the install",
	0x8A15010C:       "the install was cancelled",
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
	if isCargo(c) {
		tool = "cargo" // by name, not the path it was run by
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
	if isCargo(c) {
		// what went wrong is cargo's first error; its last lines only say
		// the build failed and where its files were left (a missing
		// linker, "error calling dlltool", came three lines before them)
		if s := cargoSays(out); s != "" {
			ie.msg += ": " + s
		}
		if said := cargoError(out); said != "" {
			ie.msg += " — " + said
		}
		return ie
	}
	said := installerSaid(out, 3)
	// what went wrong is Homebrew's first Error: line; its last lines can
	// be the support-tier notice it ends with
	if tool == "brew" {
		if e := brewError(out); e != "" {
			said = e
		}
	}
	if said != "" {
		ie.msg += " — " + said
	}
	return ie
}

// brewError is the first "Error:" line Homebrew printed, and the line
// after it that says what to do, when there is one.
func brewError(out string) string {
	ls := strings.Split(strings.ToValidUTF8(ansiSeq.ReplaceAllString(out, ""), ""), "\n")
	for i, l := range ls {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "Error:") {
			continue
		}
		// "Error: No developer tools installed.", then "Install the
		// Command Line Tools:" and the command, a blank line between
		var then []string
		for _, n := range ls[i+1:] {
			n = strings.TrimSpace(n)
			if n == "" && len(then) == 0 {
				continue
			}
			if n == "" || strings.HasPrefix(n, "Error:") || len(then) == 2 {
				break
			}
			then = append(then, n)
		}
		return strings.Join(append([]string{l}, then...), " ")
	}
	return ""
}

var (
	ansiSeq = lazyre.New(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// a line that is only a spinner frame or a progress bar (winget's
	// ██████▒▒▒ 1.20 MB / 3.40 MB, Homebrew's ###, curl's %)
	progressLine = lazyre.New(`^(?:[-\\|/]|[█▓▒░#=>.\s]*\d*(?:\.\d+)?\s*(?:[KMG]i?B\s*/\s*\d+(?:\.\d+)?\s*[KMG]i?B|%)?)$`)
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

// cargoSays is what a failed cargo install of rtk printed, said plainly with
// what to do, for the failures a user meets; "" for any other.
func cargoSays(out string) string {
	low := strings.ToLower(out)
	switch {
	case cargoInUse(out):
		return "rtk is in use — close the agents running it, and the commands they run through RTK, then try again"
	case strings.Contains(low, "link.exe") && strings.Contains(low, "not found"):
		return "Rust can't find Visual Studio's C++ linker (link.exe) to build RTK — install Visual Studio Build Tools with \"Desktop development with C++\", then try again"
	case strings.Contains(low, "linker") && strings.Contains(low, "not found"),
		strings.Contains(low, "dlltool") && strings.Contains(low, "not found"):
		return "Rust's toolchain is missing a tool it needs to build RTK — install the C/C++ build tools it uses, then try again"
	case strings.Contains(low, "failed to fetch into"), strings.Contains(low, "failed to clone"),
		strings.Contains(low, "network failure"), strings.Contains(low, "could not resolve host"),
		strings.Contains(low, "couldn't resolve host"):
		return "cargo couldn't download RTK's source from GitHub — check the network or proxy, then try again"
	}
	return ""
}

// cargoError is the first error cargo printed, with the cause it gave on
// the line after "Caused by:"; the last lines that say something when it
// printed no error line.
func cargoError(out string) string {
	out = strings.ToValidUTF8(ansiSeq.ReplaceAllString(out, ""), "")
	lines := strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' })
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "error") {
			continue
		}
		if i+2 < len(lines) && strings.TrimSpace(lines[i+1]) == "Caused by:" {
			l += " " + strings.TrimSpace(lines[i+2])
		}
		if r := []rune(l); len(r) > 400 {
			l = string(r[:400]) + "…"
		}
		return l
	}
	return installerSaid(out, 3)
}
