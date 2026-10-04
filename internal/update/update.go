// Package update keeps magpie current. Builds are published as GitHub
// releases of yetone/magpie-releases; usemagpie.ai/api/latest describes the
// newest one: its version, notes, and every file with its SHA-256.
//
// The macOS app replaces its own bundle: the new zip is downloaded, checked
// against its hash, unpacked, and accepted only if it is signed by the same
// team as the app already installed. A bare binary (the terminal build,
// and the desktop app on Windows and Linux) replaces itself the same way,
// minus the signature.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/source"
)

// Site is magpie's home; its /api/latest is the update feed.
const Site = "https://usemagpie.ai"

// Feed is where the newest release is described. MAGPIE_UPDATE_FEED points
// it elsewhere, for testing an update against a local server.
func Feed() string {
	if f := os.Getenv("MAGPIE_UPDATE_FEED"); f != "" {
		return f
	}
	return Site + "/api/latest"
}

// Release is one published version.
type Release struct {
	Version string           `json:"version"` // "0.2.0", no v
	Notes   string           `json:"notes"`   // markdown
	URL     string           `json:"url"`     // the release page
	Assets  map[string]Asset `json:"assets"`  // by file name
}

// Asset is one downloadable file of a release.
type Asset struct {
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// client asks the feed and downloads a release through the proxy the rest
// of magpie's requests take — Settings' Proxy, else the environment's, else
// the system's (#294) — on a transport of its own, whatever the process has
// done to http.DefaultTransport.
var client = &http.Client{Timeout: 10 * time.Minute, Transport: proxied()}

func proxied() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = netproxy.Func
	return netproxy.Dispatch(t)
}

// Latest asks the feed for the newest release, its notes in English.
func Latest(ctx context.Context) (*Release, error) { return LatestIn(ctx, "") }

// LatestIn asks the feed for the newest release, its notes in lang (see
// InLang): the app's language, which What's new follows.
func LatestIn(ctx context.Context, lang string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", withLang(Feed(), lang), nil)
	if err != nil {
		return nil, err
	}
	res, err := source.Do(client, req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("update feed: %s", res.Status)
	}
	var r Release
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("update feed: %w", err)
	}
	if parse(r.Version) == nil {
		return nil, fmt.Errorf("update feed: no version")
	}
	r.Notes = InLang(r.Notes, lang)
	return &r, nil
}

// Released reports whether v is a release version rather than a build from
// source ("dev", "0bcb2cc-dirty", git describe's "v0.1.0-3-g0bcb2cc");
// only releases update themselves.
func Released(v string) bool {
	s := parse(v)
	return s != nil && !describe.MatchString(s.pre) && !strings.Contains(s.pre, "dirty")
}

// describe matches what git describe adds after a tag: commits since, hash.
var describe = regexp.MustCompile(`^\d+-g[0-9a-f]+`)

// Newer reports whether version a comes after b. A pre-release comes before
// the release it leads up to.
func Newer(a, b string) bool {
	x, y := parse(a), parse(b)
	if x == nil || y == nil {
		return false
	}
	for i := range 3 {
		if x.n[i] != y.n[i] {
			return x.n[i] > y.n[i]
		}
	}
	switch {
	case x.pre == y.pre:
		return false
	case x.pre == "":
		return true
	case y.pre == "":
		return false
	}
	return x.pre > y.pre
}

type semver struct {
	n   [3]int
	pre string
}

func parse(v string) *semver {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	var s semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		s.n[i] = n
	}
	s.pre = pre
	return &s
}

// GUI says this binary has the desktop app in it. Off the Mac that app is a
// single binary too, and it updates from its own build, not the terminal
// one.
var GUI bool

// AppAsset and BinaryAsset name the files this machine would install.
func AppAsset() string { return "magpie-darwin-" + runtime.GOARCH + ".zip" }
func BinaryAsset() string {
	name := "magpie-cli-" + runtime.GOOS + "-" + runtime.GOARCH
	if GUI && runtime.GOOS != "darwin" {
		name = "magpie-" + runtime.GOOS + "-" + runtime.GOARCH
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Bundle is the .app the running binary lives in, or "" when it is not in
// one.
func Bundle() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	exe, err := Executable()
	if err != nil {
		return ""
	}
	// …/magpie.app/Contents/MacOS/magpie
	app := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if filepath.Ext(app) != ".app" || filepath.Base(filepath.Dir(exe)) != "MacOS" {
		return ""
	}
	return app
}

// Homebrew is whether exe was installed by Homebrew (brew install magpie),
// which keeps it in its Cellar and has to be the one to upgrade it: a
// binary replaced under it leaves brew thinking the old version is there.
func Homebrew(exe string) bool {
	return strings.Contains(filepath.ToSlash(exe), "/Cellar/magpie/")
}

// Executable is the running binary, symlinks resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Writable reports whether magpie may replace what lives in dir.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".magpie-update-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Stage downloads the app in rel and unpacks it next to the installed
// bundle, ready for Install. It returns the unpacked app.
func Stage(ctx context.Context, rel *Release, bundle string) (string, error) {
	a, ok := rel.Assets[AppAsset()]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", rel.Version, AppAsset())
	}
	dir := stageDir(filepath.Dir(bundle))
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	zip := filepath.Join(dir, AppAsset())
	if err := download(ctx, a, zip); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	out := filepath.Join(dir, "app")
	if b, err := proc.CommandContext(ctx, "ditto", "-x", "-k", zip, out).CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("unzip: %v: %s", err, b)
	}
	os.Remove(zip)
	app := filepath.Join(out, "magpie.app")
	if err := sameSigner(ctx, bundle, app); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return app, nil
}

// sameSigner accepts app only if its signature is intact and made by the
// team that signed the bundle it replaces.
func sameSigner(ctx context.Context, bundle, app string) error {
	if b, err := proc.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("the downloaded app's signature is broken: %s", strings.TrimSpace(string(b)))
	}
	want, got := team(ctx, bundle), team(ctx, app)
	if want != got {
		return fmt.Errorf("the downloaded app is signed by %q, not %q", got, want)
	}
	return nil
}

func team(ctx context.Context, app string) string {
	b, _ := proc.CommandContext(ctx, "codesign", "-dv", app).CombinedOutput()
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "TeamIdentifier="); ok && v != "not set" {
			return v
		}
	}
	return ""
}

// stageDir is where an update is unpacked: beside what it replaces, so
// installing it is a rename on one volume, or, where magpie may not write,
// in its cache, to be moved in with the administrator's password.
func stageDir(dir string) string {
	if !Writable(dir) {
		if cache, err := appdir.SystemCache(); err == nil {
			return filepath.Join(cache, "update")
		}
	}
	return filepath.Join(dir, ".magpie-update")
}

// Install swaps the staged app in for bundle. The running copy keeps going
// until it quits; the next launch is the new one. Where the folder is not
// magpie's to change the error is a permission one (NeedsAdmin), and
// InstallAsAdmin can do it instead.
func Install(staged, bundle string) error {
	old := filepath.Join(filepath.Dir(staged), "old.app")
	os.RemoveAll(old)
	if err := os.Rename(bundle, old); err != nil {
		return err
	}
	if err := os.Rename(staged, bundle); err != nil {
		os.Rename(old, bundle) // put it back
		return err
	}
	os.RemoveAll(filepath.Dir(filepath.Dir(staged))) // .magpie-update
	return nil
}

// InstallAsAdmin is Install with the administrator's password, asked for
// by the system.
func InstallAsAdmin(staged, bundle string) error {
	old := filepath.Join(filepath.Dir(staged), "old.app")
	err := asAdmin(swapScript(staged, bundle, old))
	if err == nil {
		os.RemoveAll(filepath.Dir(filepath.Dir(staged)))
	}
	return err
}

// Relaunch opens bundle again once this process (pid) has exited.
func Relaunch(bundle string) error {
	script := fmt.Sprintf(`while kill -0 %d 2>/dev/null; do sleep 0.2; done; open %q`, os.Getpid(), bundle)
	cmd := proc.Command("/bin/sh", "-c", script)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	return cmd.Start()
}

// ReplaceBinary puts the release's build for this binary where the
// running one is.
func ReplaceBinary(ctx context.Context, rel *Release) error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	staged, err := StageBinary(ctx, rel)
	if err != nil {
		return err
	}
	err = InstallBinary(staged, exe)
	if NeedsAdmin(err) && CanElevate() {
		err = InstallBinaryAsAdmin(staged, exe)
	}
	return err
}

// StageBinary downloads the release's build for this binary next to it (or
// to the cache, where magpie may not write there), ready for InstallBinary.
func StageBinary(ctx context.Context, rel *Release) (string, error) {
	a, ok := rel.Assets[BinaryAsset()]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", rel.Version, BinaryAsset())
	}
	exe, err := Executable()
	if err != nil {
		return "", err
	}
	tmp := exe + ".new"
	if !Writable(filepath.Dir(exe)) {
		dir := stageDir(filepath.Dir(exe))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		tmp = filepath.Join(dir, filepath.Base(exe)+".new")
	}
	if err := download(ctx, a, tmp); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// InstallBinary swaps a staged binary in for exe, the running one, which
// keeps going until it exits.
func InstallBinary(staged, exe string) error {
	var old string
	if runtime.GOOS == "windows" {
		// A running .exe cannot be overwritten, but it can be moved aside.
		// What the last update moved aside may still be running too (a
		// `magpie serve` started before it), and can't be removed or
		// replaced then: this one goes beside it, under a name of its own.
		// The download is kept, for another try.
		RemoveOld(exe)
		var err error
		if old, err = moveAside(exe); err != nil {
			return err
		}
	}
	if err := os.Rename(staged, exe); err != nil {
		if old != "" {
			if restoreErr := os.Rename(old, exe); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("couldn't restore %s from %s: %w", filepath.Base(exe), old, restoreErr))
			}
		} else if !NeedsAdmin(err) { // kept for InstallBinaryAsAdmin
			os.Remove(staged)
		}
		return err
	}
	return nil
}

// renameFile is os.Rename; tests make it fail.
var renameFile = os.Rename

// asideWaits are the pauses between tries at moving the running exe aside:
// an antivirus scanning it or a sync client (OneDrive) reading it holds it
// open, which Windows won't rename under, for a moment, and lets go.
var asideWaits = []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1500 * time.Millisecond}

// Windows' errors for a file another program holds open without letting
// it be renamed.
const (
	errSharingViolation syscall.Errno = 32
	errLockViolation    syscall.Errno = 33
)

// moveAside moves the running exe out of the way of the new version, under
// a name of its own, trying again for a few seconds while something has it
// open. What it says when it can't is the reason and what to do: the error
// is shown as is in the version row. On success it returns the actual name,
// so a failed installation can put the running exe back.
func moveAside(exe string) (string, error) {
	var err error
	for i := 0; ; i++ {
		old := oldName(exe)
		if err = renameFile(exe, old); err == nil {
			return old, nil
		}
		if errors.Is(err, fs.ErrNotExist) || i == len(asideWaits) {
			break
		}
		if i == 0 {
			os.Chmod(exe, 0o755) // a read-only exe: the attribute off
		}
		time.Sleep(asideWaits[i])
	}
	why := err.Error()
	var le *os.LinkError
	if errors.As(err, &le) {
		why = le.Err.Error() // the paths are known; the reason is what's new
	}
	var hint string
	switch {
	case errors.Is(err, errSharingViolation), errors.Is(err, errLockViolation):
		hint = "another program has it open (often an antivirus or OneDrive): let magpie through it and restart to update again, or download the new version and put it in place of this one"
	case errors.Is(err, fs.ErrPermission):
		hint = "Windows doesn't let magpie change files in " + filepath.Dir(exe) + " (an antivirus' folder protection, or the folder's permissions): let magpie through, or download the new version and put it in place of this one"
	default:
		hint = "download the new version and put it in place of this one"
	}
	return "", fmt.Errorf("couldn't move %s aside to put the new version in: %s; %s", filepath.Base(exe), strings.TrimRight(why, ". 。"), hint)
}

// oldName is where a running exe is moved aside to: exe.old, or when
// that is still there (in use), exe.old-2, exe.old-3…
func oldName(exe string) string {
	name := exe + ".old"
	for n := 2; ; n++ {
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s.old-%d", exe, n)
	}
}

// RemoveOld removes what earlier updates moved aside of exe, those no
// longer running.
func RemoveOld(exe string) {
	os.Remove(exe + ".old")
	olds, _ := filepath.Glob(exe + ".old-*")
	for _, o := range olds {
		os.Remove(o)
	}
}

// Replaced reports whether exe is no longer the binary that was there when
// this process looked (started, from os.Stat): another magpie installed an
// update over it, and this one runs from where it was moved aside.
func Replaced(exe string, started os.FileInfo) bool {
	now, err := os.Stat(exe)
	return err == nil && started != nil && (now.Size() != started.Size() || !now.ModTime().Equal(started.ModTime()))
}

// RemoveStaleNew removes exe.new when it is exe over again: the update a
// magpie left running from before it downloaded once more.
func RemoveStaleNew(exe string) {
	staged := exe + ".new"
	a, err1 := os.Stat(exe)
	b, err2 := os.Stat(staged)
	if err1 != nil || err2 != nil || a.Size() != b.Size() {
		return
	}
	if ha, hb := fileHash(exe), fileHash(staged); ha != "" && ha == hb {
		os.Remove(staged)
	}
}

func fileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// InstallBinaryAsAdmin is InstallBinary with the administrator's password.
func InstallBinaryAsAdmin(staged, exe string) error {
	err := asAdmin("mv -f " + shellQuote(staged) + " " + shellQuote(exe))
	if err != nil && !errors.Is(err, ErrCanceled) {
		os.Remove(staged)
	}
	return err
}

// RelaunchBinary starts exe again: with its window on view when window is
// set (the window was open), else as the tray app alone, as autostart
// starts it. The new process waits for this one to exit before it takes
// the gateway's port; see AwaitPredecessor.
func RelaunchBinary(exe string, window bool, view string) error {
	cmd := proc.Command(exe, RelaunchArgs(window, view)...)
	cmd.Env = append(os.Environ(), "MAGPIE_REPLACES="+strconv.Itoa(os.Getpid()))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// RelaunchArgs is what RelaunchBinary starts magpie with: `gui [view]` for
// the window, `tray` for the tray icon alone.
func RelaunchArgs(window bool, view string) []string {
	switch {
	case !window:
		return []string{"tray"}
	case view != "":
		return []string{"gui", view}
	}
	return []string{"gui"}
}

// AwaitPredecessor blocks, for a while at most, until the magpie that
// relaunched this one has exited.
func AwaitPredecessor() {
	pid, err := strconv.Atoi(os.Getenv("MAGPIE_REPLACES"))
	os.Unsetenv("MAGPIE_REPLACES")
	if err != nil || pid <= 0 {
		return
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && alive(pid); {
		time.Sleep(100 * time.Millisecond)
	}
}

type progressKey struct{}

// WithProgress has downloads made under ctx report how far along they are;
// total is 0 while the size is unknown.
func WithProgress(ctx context.Context, f func(done, total int64)) context.Context {
	return context.WithValue(ctx, progressKey{}, f)
}

// download fetches a to path and checks its hash. A connection that drops
// part way — GitHub from some networks — gets two more tries.
func download(ctx context.Context, a Asset, path string) error {
	if a.SHA256 == "" {
		return errors.New("the release lists no checksum for " + filepath.Base(path))
	}
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(time.Duration(try) * 2 * time.Second):
			}
		}
		if err = fetch(ctx, a, path); err == nil || ctx.Err() != nil {
			break
		}
	}
	return err
}

func fetch(ctx context.Context, a Asset, path string) error {
	// through a mirror when one is given (mirror.go): the hash checked
	// below is still the feed's
	u := mirrored(mirrorOf(ctx), a.URL)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	res, err := source.Do(client, req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		if u != a.URL {
			return fmt.Errorf("download %s through the mirror %s: %s", filepath.Base(path), strings.TrimSuffix(u, a.URL), res.Status)
		}
		return fmt.Errorf("download %s: %s", filepath.Base(path), res.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	var body io.Reader = res.Body
	if report, ok := ctx.Value(progressKey{}).(func(done, total int64)); ok {
		total := res.ContentLength
		if total <= 0 {
			total = a.Size
		}
		body = &counter{r: res.Body, total: max(total, 0), report: report}
		report(0, max(total, 0))
	}
	_, err = io.Copy(io.MultiWriter(f, h), body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), a.SHA256) {
		err = fmt.Errorf("%s does not match its checksum", filepath.Base(path))
		if u != a.URL {
			err = fmt.Errorf("%s from the mirror %s does not match its checksum from %s; not installed", filepath.Base(path), strings.TrimSuffix(u, a.URL), Site)
		}
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// counter reports bytes as they are read.
type counter struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	c.report(c.done, c.total)
	return n, err
}

// Alive reports whether the process pid is still running.
func Alive(pid int) bool { return alive(pid) }
