package update

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFixture is a download folder holding exe (magpie-windows-amd64.exe,
// as the site names it) and the place it installs to, each with its
// version; what Settle asks of the system answers from them.
type installFixture struct {
	downloads, exe, to, profiles string
	versions                     map[string]uint64
	placed                       []string // folders installCLI was asked to put on PATH
	portable                     string
	record                       string   // the folder chosen, as HKCU\Software\magpie keeps it
	asked                        []string // what AskFolder was offered
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	root := t.TempDir()
	f := &installFixture{
		downloads: filepath.Join(root, "Downloads"),
		to:        filepath.Join(root, "Local", "Programs", "magpie", "magpie.exe"),
		profiles:  filepath.Join(root, "Roaming"),
		versions:  map[string]uint64{},
	}
	f.exe = filepath.Join(f.downloads, "magpie-windows-amd64.exe")
	if err := os.MkdirAll(f.downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.exe, []byte("MZ new download"), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := []any{installTarget, downloadFolder, fileVersion, portableData, webViewHome, installCLI}
	wasRead, wasWrite, wasAsk := recordedDir, recordDir, AskFolder
	t.Cleanup(func() {
		recordedDir, recordDir, AskFolder = wasRead, wasWrite, wasAsk
		installTarget = saved[0].(func() string)
		downloadFolder = saved[1].(func(string) bool)
		fileVersion = saved[2].(func(string) uint64)
		portableData = saved[3].(func() string)
		webViewHome = saved[4].(func(string) string)
		installCLI = saved[5].(func(string, string) error)
	})
	installTarget = func() string { return f.to }
	downloadFolder = func(dir string) bool { return samePath(dir, f.downloads) }
	fileVersion = func(p string) uint64 {
		for k, v := range f.versions {
			if samePath(k, p) {
				return v
			}
		}
		return 0
	}
	portableData = func() string { return f.portable }
	webViewHome = func(exe string) string { return filepath.Join(f.profiles, filepath.Base(exe)) }
	installCLI = func(exe, dir string) error {
		f.placed = append(f.placed, dir)
		return nil
	}
	recordedDir = func() string { return f.record }
	recordDir = func(dir string) error { f.record = dir; return nil }
	AskFolder = nil
	return f
}

// ask has AskFolder answer dir, after the check it is given accepts it,
// and note what it was offered.
func (f *installFixture) ask(t *testing.T, dir string) {
	t.Helper()
	AskFolder = func(suggest string, check func(string) error) string {
		f.asked = append(f.asked, suggest)
		if dir == "" {
			return ""
		}
		if err := check(dir); err != nil {
			t.Fatalf("check refused %s: %v", dir, err)
		}
		return dir
	}
}

func (f *installFixture) installed(t *testing.T, b string, version uint64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.to, []byte(b), 0o755); err != nil {
		t.Fatal(err)
	}
	f.versions[f.to] = version
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Opened from Downloads with nothing installed, the app is copied to the
// user's programs folder as magpie.exe, that folder goes on the PATH, and
// the installed one is what opens. The download itself stays: it is the
// user's file.
func TestSettleInstallsADownload(t *testing.T) {
	f := newInstallFixture(t)
	to, err := settle(f.exe)
	if err != nil {
		t.Fatal(err)
	}
	if to != f.to {
		t.Fatalf("settle opens %q, want the installed %q", to, f.to)
	}
	if got := readString(t, f.to); got != "MZ new download" {
		t.Fatalf("installed copy holds %q", got)
	}
	if got := readString(t, f.exe); got != "MZ new download" {
		t.Fatalf("the download was changed or removed: %q", got)
	}
	if len(f.placed) != 1 || !samePath(f.placed[0], filepath.Dir(f.to)) {
		t.Fatalf("PATH given %v, want the install folder once", f.placed)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(f.to), ".magpie-install-*"))
	if len(left) != 0 {
		t.Fatalf("staging files left: %v", left)
	}
}

// A copy kept anywhere the user chose, or a portable one, runs where it
// is and installs nothing; so does one run with MAGPIE_NO_INSTALL=1.
func TestSettleLeavesAChosenPlace(t *testing.T) {
	f := newInstallFixture(t)
	tools := filepath.Join(filepath.Dir(f.downloads), "Tools", "magpie-windows-amd64.exe")
	if err := os.MkdirAll(filepath.Dir(tools), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tools, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if to, err := settle(tools); to != "" || err != nil {
		t.Fatalf("a copy in a folder of the user's own opens %q (%v)", to, err)
	}
	f.portable = filepath.Join(f.downloads, "data")
	if to, err := settle(f.exe); to != "" || err != nil {
		t.Fatalf("a portable copy opens %q (%v)", to, err)
	}
	f.portable = ""
	if to, err := settle(f.to); to != "" || err != nil {
		t.Fatalf("the installed copy opens %q (%v)", to, err)
	}
	if exists(f.to) {
		t.Fatal("installed though it shouldn't have been")
	}
	t.Setenv(NoInstallEnv, "1")
	if from, to := Settle(); from != "" || to != "" {
		t.Fatalf("MAGPIE_NO_INSTALL=1 still opens %q", to)
	}
	if forwards(f.exe) {
		t.Fatal("MAGPIE_NO_INSTALL=1 still counts as forwarding")
	}
}

// Installed already: the download opens it, and only a newer version goes
// in its place. An older or unknown one leaves the installed copy, which
// updates itself.
func TestSettleKeepsTheInstalledVersion(t *testing.T) {
	for _, c := range []struct {
		name         string
		mine, theirs uint64
		want         string
		placedOnPath bool
	}{
		{"older download", 1<<32 | 100<<16, 1<<32 | 200<<16, "MZ installed", false},
		{"same version", 1<<32 | 200<<16, 1<<32 | 200<<16, "MZ installed", false},
		{"no version in the download", 0, 1<<32 | 200<<16, "MZ installed", false},
		{"no version installed", 1<<32 | 300<<16, 0, "MZ installed", false},
		{"newer download", 1<<32 | 300<<16, 1<<32 | 200<<16, "MZ new download", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newInstallFixture(t)
			f.installed(t, "MZ installed", c.theirs)
			f.versions[f.exe] = c.mine
			to, err := settle(f.exe)
			if err != nil || to != f.to {
				t.Fatalf("settle = %q, %v; want %q", to, err, f.to)
			}
			if got := readString(t, f.to); got != c.want {
				t.Fatalf("installed holds %q, want %q", got, c.want)
			}
			if len(f.placed) != 0 {
				t.Fatalf("PATH written again for an installed magpie: %v", f.placed)
			}
			if got := readString(t, f.exe); got != "MZ new download" {
				t.Fatalf("the download was changed: %q", got)
			}
		})
	}
}

// A failure to install leaves the download running where it is.
func TestSettleFailingRunsOn(t *testing.T) {
	f := newInstallFixture(t)
	// a file where the install folder should be
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(f.to)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(f.to), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	to, err := settle(f.exe)
	if to != "" || err == nil {
		t.Fatalf("settle = %q, %v; want this one to run on, with the reason", to, err)
	}
	if len(f.placed) != 0 {
		t.Fatal("PATH written for an install that failed")
	}
}

// WebView2 keeps a profile per exe name, so magpie.exe would start with
// none: the installed copy is pointed at the download's, unless it has
// one of its own.
func TestSettleKeepsTheWebViewProfile(t *testing.T) {
	f := newInstallFixture(t)
	old := filepath.Join(f.profiles, "magpie-windows-amd64.exe")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := settle(f.exe); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(filepath.Dir(f.to), "webview-profile")
	if got := strings.TrimSpace(readString(t, note)); !samePath(got, old) {
		t.Fatalf("webview note names %q, want %q", got, old)
	}

	g := newInstallFixture(t)
	for _, d := range []string{"magpie-windows-amd64.exe", "magpie.exe"} {
		if err := os.MkdirAll(filepath.Join(g.profiles, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := settle(g.exe); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(filepath.Dir(g.to), "webview-profile")) {
		t.Fatal("pointed away from magpie.exe's own profile")
	}
}

// The installed copy clears what magpie wrote beside the download: the
// magpie.com and magpie.cmd that ran it, and what updates moved aside.
// A magpie.cmd of the user's own, or one magpie wrote for another
// program there, stays.
func TestSettledClearsOnlyMagpiesFiles(t *testing.T) {
	f := newInstallFixture(t)
	dir := f.downloads
	com := filepath.Join(dir, comName)
	if err := os.WriteFile(com, comText([]byte("MZ launcher"), f.exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, shimName), []byte(shimText(f.exe)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.exe+".old", []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	// an update an older magpie downloaded beside it, never put in now
	if err := os.WriteFile(f.exe+".new", []byte("MZ newer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(installedFromEnv, f.exe)
	Settled()
	for _, p := range []string{com, filepath.Join(dir, shimName), f.exe + ".old", f.exe + ".new"} {
		if exists(p) {
			t.Fatalf("%s left", filepath.Base(p))
		}
	}
	if !exists(f.exe) {
		t.Fatal("the download itself was removed")
	}
	if os.Getenv(installedFromEnv) != "" {
		t.Fatal("passed on to what the installed magpie starts")
	}

	// the user's own, and magpie's for another program
	own := []byte("@echo off\r\nrem mine\r\n\"%~dp0magpie-windows-amd64.exe\" %*\r\n")
	if err := os.WriteFile(filepath.Join(dir, shimName), own, 0o755); err != nil {
		t.Fatal(err)
	}
	other := comText([]byte("MZ launcher"), filepath.Join(dir, "magpie-windows-amd64 (1).exe"))
	if err := os.WriteFile(com, other, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(installedFromEnv, f.exe)
	Settled()
	if b, err := os.ReadFile(filepath.Join(dir, shimName)); err != nil || !bytes.Equal(b, own) {
		t.Fatalf("the user's magpie.cmd: %q, %v", b, err)
	}
	if b, err := os.ReadFile(com); err != nil || !bytes.Equal(b, other) {
		t.Fatalf("another program's magpie.com: %v", err)
	}
}

// Forward opens the installed one with this one's arguments, waits for
// its word, and passes that word on to a restart to update waiting on
// this one.
func TestForwardWaitsForTheInstalledOne(t *testing.T) {
	waiting := filepath.Join(t.TempDir(), "started")
	t.Setenv(startedEnv, waiting)
	was := launch
	t.Cleanup(func() { launch = was })
	var gotExe string
	var gotArgs, gotEnv []string
	launch = func(exe string, args, env []string) (<-chan struct{}, func(), error) {
		gotExe, gotArgs, gotEnv = exe, args, env
		for _, e := range env {
			if m, ok := strings.CutPrefix(e, startedEnv+"="); ok {
				os.WriteFile(m, []byte("1"), 0o644)
			}
		}
		return make(chan struct{}), func() {}, nil
	}
	if err := Forward(`C:\Users\u\Downloads\magpie-windows-amd64.exe`, `C:\Users\u\AppData\Local\Programs\magpie\magpie.exe`); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotExe, `magpie.exe`) {
		t.Fatalf("opened %q", gotExe)
	}
	if strings.Join(gotArgs, " ") != strings.Join(os.Args[1:], " ") {
		t.Fatalf("args %v, want %v", gotArgs, os.Args[1:])
	}
	var from, marks int
	for _, e := range gotEnv {
		if strings.HasPrefix(e, installedFromEnv+`=C:\Users\u\Downloads\magpie-windows-amd64.exe`) {
			from++
		}
		if strings.HasPrefix(e, startedEnv+"=") {
			marks++
			if strings.HasSuffix(e, "="+waiting) {
				t.Fatal("the restart's own marker handed on, which it removes as it reads it")
			}
		}
	}
	if from != 1 || marks != 1 {
		t.Fatalf("env: %d %s, %d %s", from, installedFromEnv, marks, startedEnv)
	}
	if !exists(waiting) {
		t.Fatal("the restart waiting on this one was never told")
	}

	// one that quits without a word: this one runs on
	launch = func(string, []string, []string) (<-chan struct{}, func(), error) {
		c := make(chan struct{})
		close(c)
		return c, func() {}, nil
	}
	if err := Forward("a", "b"); err == nil {
		t.Fatal("a magpie that quit unstarted taken as opened")
	}
}

// A download that opens the installed magpie writes no magpie.com beside
// it: the terminal's magpie is the installed one's.
func TestNoCompanionBesideAForwardingDownload(t *testing.T) {
	f := newInstallFixture(t)
	f.installed(t, "MZ installed", 0)
	if forwards(f.exe) != (runtime.GOOS == "windows") {
		// forwards is Windows' alone
		t.Fatalf("forwards(%s) = %v", f.exe, forwards(f.exe))
	}
	was := comLauncher
	t.Cleanup(func() { comLauncher = was })
	comLauncher = []byte("MZ launcher")
	t.Setenv("PATH", f.downloads)
	if err := EnsureCompanion(f.exe); err != nil {
		t.Fatal(err)
	}
	// nor one that runs where it is: a download folder gets one only from
	// Add (TestNoCompanionPutBackOnTheDesktop)
	if exists(filepath.Join(f.downloads, comName)) {
		t.Fatalf("magpie.com beside the download, forwarding: %v", forwards(f.exe))
	}
}

func TestInFolder(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		path string
		want bool
	}{
		{root, true},
		{filepath.Join(root, "Temp1_magpie.zip"), true},
		{strings.ToUpper(filepath.Join(root, "x")), true},
		{filepath.Dir(root), false},
		{root + "-other", false},
	} {
		if got := inFolder(c.path, root); got != c.want {
			t.Errorf("inFolder(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	if inFolder(root, "") || inFolder(root, "relative") {
		t.Error("a folder that isn't absolute holds things")
	}
}

// The first time, the user is asked where to install, offered the
// default; the folder they choose is where magpie goes, what goes on
// PATH, and what is kept for the next download (feng_21795_78266 on
// Discord).
func TestSettleInstallsWhereTheUserChose(t *testing.T) {
	f := newInstallFixture(t)
	mine := filepath.Join(filepath.Dir(f.downloads), "D", "Apps", "magpie")
	f.ask(t, mine)
	to, err := settle(f.exe)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(mine, "magpie.exe")
	if to != want {
		t.Fatalf("settle opens %q, want %q", to, want)
	}
	if len(f.asked) != 1 || !samePath(f.asked[0], filepath.Dir(f.to)) {
		t.Fatalf("offered %q, want the default %q once", f.asked, filepath.Dir(f.to))
	}
	if got := readString(t, want); got != "MZ new download" {
		t.Fatalf("installed copy holds %q", got)
	}
	if exists(f.to) {
		t.Fatal("installed in the default folder too")
	}
	if len(f.placed) != 1 || !samePath(f.placed[0], mine) {
		t.Fatalf("PATH given %v, want %s", f.placed, mine)
	}
	if !samePath(f.record, mine) {
		t.Fatalf("kept %q, want %q", f.record, mine)
	}
	if forwards(f.exe) != (runtime.GOOS == "windows") {
		t.Fatal("the download isn't taken as opening the chosen copy")
	}

	// opened again, and again as a newer version: never asked, never
	// moved, and the newer one goes in the chosen folder
	f.versions[want] = 1 << 32
	f.versions[f.exe] = 2 << 32
	for i := 0; i < 2; i++ {
		to, err = settle(f.exe)
		if err != nil || to != want {
			t.Fatalf("again: settle = %q, %v; want %q", to, err, want)
		}
	}
	if len(f.asked) != 1 {
		t.Fatalf("asked again: %q", f.asked)
	}
	if exists(f.to) {
		t.Fatal("moved to the default folder")
	}
	if len(f.placed) != 1 {
		t.Fatalf("PATH written again: %v", f.placed)
	}
}

// Declining the dialog installs nothing: the download runs where it is.
func TestSettleDeclinedRunsHere(t *testing.T) {
	f := newInstallFixture(t)
	f.ask(t, "")
	if to, err := settle(f.exe); to != "" || err != nil {
		t.Fatalf("settle = %q, %v after declining", to, err)
	}
	if exists(f.to) || f.record != "" || len(f.placed) != 0 {
		t.Fatalf("installed after declining: %v %q %v", exists(f.to), f.record, f.placed)
	}
}

// A magpie in the default folder from before there was a choice is
// installed: it is opened with no question and no record.
func TestSettleDefaultInstallAsksNothing(t *testing.T) {
	f := newInstallFixture(t)
	f.installed(t, "MZ installed", 0)
	f.ask(t, filepath.Join(t.TempDir(), "magpie"))
	to, err := settle(f.exe)
	if err != nil || to != f.to {
		t.Fatalf("settle = %q, %v; want %q", to, err, f.to)
	}
	if len(f.asked) != 0 || f.record != "" {
		t.Fatalf("asked %q, kept %q", f.asked, f.record)
	}
}

// A chosen folder since emptied is offered again, not the default.
func TestSettleOffersTheChosenFolderAgain(t *testing.T) {
	f := newInstallFixture(t)
	gone := filepath.Join(t.TempDir(), "magpie")
	f.record = gone
	f.ask(t, filepath.Dir(f.to))
	to, err := settle(f.exe)
	if err != nil || to != f.to {
		t.Fatalf("settle = %q, %v; want %q", to, err, f.to)
	}
	if len(f.asked) != 1 || !samePath(f.asked[0], gone) {
		t.Fatalf("offered %q, want %q", f.asked, gone)
	}
	if !samePath(f.record, filepath.Dir(f.to)) {
		t.Fatalf("kept %q after the default was chosen", f.record)
	}
}

// A folder picked is used as it is when named magpie, else a magpie
// folder goes inside it.
func TestInstallDir(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct{ picked, want string }{
		{filepath.Join(root, "Apps"), filepath.Join(root, "Apps", "magpie")},
		{filepath.Join(root, "Apps", "magpie"), filepath.Join(root, "Apps", "magpie")},
		{filepath.Join(root, "Apps", "Magpie") + string(filepath.Separator), filepath.Join(root, "Apps", "Magpie")},
	} {
		if got := InstallDir(c.picked); got != c.want {
			t.Errorf("InstallDir(%q) = %q, want %q", c.picked, got, c.want)
		}
	}
}

// A download folder, or one the user can't write to (Program Files
// without an administrator), is refused with the reason; the folder it
// had to make for the check isn't left behind.
func TestInstallableRefuses(t *testing.T) {
	f := newInstallFixture(t)
	if err := Installable(f.downloads); err != ErrDownloadFolder {
		t.Fatalf("Downloads: %v", err)
	}
	if err := Installable("magpie"); err == nil {
		t.Fatal("a relative folder accepted")
	}
	tmp := t.TempDir()
	if err := Installable(filepath.Join(tmp, "new", "magpie")); err != nil {
		t.Fatalf("a folder of the user's own: %v", err)
	}
	if exists(filepath.Join(tmp, "new")) {
		t.Fatal("the folders made for the check were left")
	}
	if err := Installable(tmp); err != nil {
		t.Fatalf("a folder there already: %v", err)
	}
	if left, _ := os.ReadDir(tmp); !exists(tmp) || len(left) != 0 {
		t.Fatalf("check left %v", left)
	}
	if runtime.GOOS == "windows" {
		return // a folder's mode doesn't stop a write there; tried for real on Windows
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	if err := Installable(ro); err != ErrNeedsAdmin {
		t.Fatalf("a folder the user can't write: %v", err)
	}
	if err := Installable(filepath.Join(ro, "magpie")); err != ErrNeedsAdmin {
		t.Fatalf("a folder that can't be made: %v", err)
	}
}
