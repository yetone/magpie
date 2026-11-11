package library

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// fakeRTKRelease serves a fake rtk Windows release at the URL rtkDownloadBase
// is set to: the zip holds exe as rtk.exe, and checksums.txt gives its
// SHA-256 — a wrong one when bad, so the check can be made to fail (#2006).
func fakeRTKRelease(t *testing.T, exe string, bad bool) string {
	t.Helper()
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, err := zw.Create("rtk.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(exe)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zb.Bytes())
	line := hex.EncodeToString(sum[:])
	if bad {
		line = strings.Repeat("0", 64)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			fmt.Fprintf(w, "%s  rtk-x86_64-pc-windows-msvc.zip\n", line)
			return
		}
		w.Write(zb.Bytes())
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// #2006: a standalone rtk.exe on Windows — rtk's own release put in
// %USERPROFILE%\.local\bin, not winget's and not cargo's — had no upgrade
// command, so the Upgrade button was disabled. It now gets one; an rtk
// winget's records say winget installed is still not taken over.
func TestRTKWindowsStandaloneUpgrader(t *testing.T) {
	h := sandbox(t)
	arch := rtkArch
	rtkArch = "amd64"
	t.Cleanup(func() { rtkArch = arch })
	bin := filepath.Join(h, ".local", "bin", "rtk.exe")
	write(t, bin, "rtk")
	win, rec := rtkOnWindows, wingetRecords
	rtkOnWindows = true
	wingetRecords = func() ([]string, bool) { return []string{filepath.Join(h, "winget")}, true }
	forgetWingetOwned()
	t.Cleanup(func() {
		rtkOnWindows, wingetRecords = win, rec
		forgetWingetOwned()
	})

	if c := rtkUpgrader(bin); len(c) != 2 || c[0] != rtkWindowsRelease || c[1] != bin {
		t.Fatalf("standalone: upgrader %q; want %s … %s", c, rtkWindowsRelease, bin)
	}

	// winget's records say it is winget's: magpie doesn't take it over
	forgetWingetOwned()
	wingetRecords = func() ([]string, bool) { return []string{filepath.Dir(bin)}, true }
	if c := rtkUpgrader(bin); c != nil {
		t.Fatalf("winget's own: upgrader %q; want none", c)
	}

	// magpie on arm64: rtk's Windows release has no build for it
	forgetWingetOwned()
	wingetRecords = func() ([]string, bool) { return []string{filepath.Join(h, "winget")}, true }
	rtkArch = "arm64"
	if c := rtkUpgrader(bin); c != nil {
		t.Fatalf("arm64: upgrader %q; want none", c)
	}
}

// #2006: with no record of winget's to read, the standalone action needs
// winget to say it has no rtk-ai.rtk, or no winget at all. A winget that
// couldn't say (it timed out) is no answer: the rtk may be winget's, and
// magpie doesn't take it over.
func TestRTKWindowsStandaloneAsksWinget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	h := sandbox(t)
	bin := filepath.Join(h, ".local", "bin", "rtk.exe")
	write(t, bin, "rtk")
	arch := rtkArch
	rtkArch = "amd64"
	t.Cleanup(func() { rtkArch = arch })
	none := func() ([]string, bool) { return nil, false }
	t.Setenv("PATH", filepath.Join(h, "empty"))

	// no winget on the PATH: nothing of winget's to take over
	win, rec := rtkOnWindows, wingetRecords
	rtkOnWindows, wingetRecords = true, none
	forgetWingetOwned()
	t.Cleanup(func() {
		rtkOnWindows, wingetRecords = win, rec
		forgetWingetOwned()
	})
	if c := rtkUpgrader(bin); len(c) != 2 || c[0] != rtkWindowsRelease {
		t.Fatalf("no winget: upgrader %q; want %s", c, rtkWindowsRelease)
	}

	// winget answers, and lists no rtk-ai.rtk
	wingetOn(t, h, wingetUnlisted, 0, none)
	if c := rtkUpgrader(bin); len(c) != 2 || c[0] != rtkWindowsRelease {
		t.Fatalf("unlisted: upgrader %q; want %s", c, rtkWindowsRelease)
	}

	// winget lists it: winget upgrades it
	wingetOn(t, h, wingetListed, 0, none)
	if c := rtkUpgrader(bin); len(c) == 0 || c[0] != "winget" {
		t.Fatalf("listed: upgrader %q; want winget upgrade", c)
	}

	// winget fails some other way (its sources unreachable): no answer
	wingetOn(t, h, "", 1, none)
	if c := rtkUpgrader(bin); c != nil {
		t.Fatalf("winget failed: upgrader %q; want none", c)
	}

	// winget runs out of time: no answer either
	testenv.Program(t, filepath.Join(h, "tools", "winget"), "#!/bin/sh\nexec sleep 5\n")
	old := wingetListTimeout
	wingetListTimeout = 200 * time.Millisecond
	t.Cleanup(func() { wingetListTimeout = old })
	forgetWingetOwned()
	if c := rtkUpgrader(bin); c != nil {
		t.Fatalf("hung winget: upgrader %q; want none", c)
	}
}

// #2006: only a member named rtk.exe is taken out of the release zip;
// another exe in it (rtk-proxy's) is not rtk.
func TestRTKWindowsReleaseZipNeedsRTKExe(t *testing.T) {
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for _, n := range []string{"LICENSE", "rtk-proxy.exe"} {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(n))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if b, err := rtkExeFromZip(zb.Bytes()); err == nil {
		t.Fatalf("took %q from a zip with no rtk.exe", b)
	}
}

// #2006: UpgradeRTK replaces a standalone Windows rtk with rtk.exe out of
// rtk's own Windows release, and what it then reads is the new version.
func TestRTKWindowsReleaseReplacesBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake rtk is a shell script")
	}
	h := sandbox(t)
	latestServer(t, "v1.0.1")
	dir := filepath.Join(h, ".local", "bin")
	bin := filepath.Join(dir, "rtk")
	write(t, filepath.Join(dir, "VERSION"), "0.9.0")
	testenv.Program(t, bin, strings.ReplaceAll(versionedRTK, "VERSION", filepath.Join(dir, "VERSION")))
	t.Setenv("PATH", dir)
	// the action rtkUpgrader gives the standalone rtk.exe: this runnable rtk
	// stands in for one, since a fake here is a shell script (rtkUpgrader's
	// own branch is judged above)
	up := upgraderOf
	upgraderOf = func(string) []string { return []string{rtkWindowsRelease, bin} }
	t.Cleanup(func() { upgraderOf = up })

	const newRTK = "#!/bin/sh\ncase \"$1\" in\n--version) echo 'rtk 1.0.1'; exit 0 ;;\ngain) echo '{\"summary\":{\"total_commands\":0}}' ;;\nesac\nexit 2\n"
	old := rtkDownloadBase
	rtkDownloadBase = fakeRTKRelease(t, newRTK, false)
	t.Cleanup(func() { rtkDownloadBase = old })

	v, err := UpgradeRTK()
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != "1.0.1" {
		t.Fatalf("version %q; want 1.0.1", v.Version)
	}
	if got := read(t, bin); got != newRTK {
		t.Fatalf("binary not replaced:\n got %q\nwant %q", got, newRTK)
	}
	// the rtk and the fake's VERSION file, nothing written beside them
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("left files behind in %s: %v", dir, ents)
	}
}

// #2006: a download that doesn't match rtk's own checksums is refused, and
// the rtk that was there is left untouched — no half-written file either.
func TestRTKWindowsReleaseChecksumMismatch(t *testing.T) {
	h := sandbox(t)
	dir := filepath.Join(h, ".local", "bin")
	bin := filepath.Join(dir, "rtk.exe")
	write(t, bin, "the rtk that was here")
	old := rtkDownloadBase
	rtkDownloadBase = fakeRTKRelease(t, "the new rtk", true)
	t.Cleanup(func() { rtkDownloadBase = old })

	err := installRTKWindowsRelease("1.0.1", bin)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("mismatch: %v; want a checksum error", err)
	}
	if got := read(t, bin); got != "the rtk that was here" {
		t.Fatalf("binary changed to %q", got)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("left files behind in %s: %v", dir, ents)
	}
}

// #2006: the Upgrade button shows this string, so it must read as what the
// upgrade does — not the internal action name and the user's binary path.
func TestRTKWindowsReleaseShown(t *testing.T) {
	bin := `C:\x\rtk.exe`
	got := shown([]string{rtkWindowsRelease, bin})
	if got != "rtk's own Windows release" {
		t.Fatalf("shown %q; want %q", got, "rtk's own Windows release")
	}
	if strings.Contains(got, rtkWindowsRelease) || strings.Contains(got, bin) {
		t.Fatalf("shown %q leaks the action or the path", got)
	}
}

// #2006: UpgradeRTK hands this channel to magpie's own download-and-replace
// instead of running the action as a command (it is not one). This is the
// wiring the two tests above leave untested on Windows: the successful one
// skips there. A download that doesn't match rtk's checksums is enough —
// its error is the install's, not the installer's.
func TestRTKWindowsReleaseUpgradeRunsTheInstall(t *testing.T) {
	h := sandbox(t)
	latestServer(t, "v1.0.1")
	// rtk is found on the PATH first (rtkPath), which is how a hand-placed
	// .local\bin\rtk.exe on it is found on the machine of the report
	const script = "#!/bin/sh\nexit 2\n"
	testenv.Program(t, filepath.Join(h, "bin", "rtk"), script)
	write(t, filepath.Join(h, "bin", "rtk.exe"), "rtk")
	bin := rtkPath()
	if bin == "" {
		t.Fatal("no rtk on the sandbox's PATH")
	}
	up := upgraderOf
	upgraderOf = func(string) []string { return []string{rtkWindowsRelease, bin} }
	old := rtkDownloadBase
	rtkDownloadBase = fakeRTKRelease(t, "the new rtk", true)
	ver := rtkVersion
	rtkVersion = func() string { return "0.9.0" }
	t.Cleanup(func() {
		upgraderOf, rtkDownloadBase, rtkVersion = up, old, ver
	})

	_, err := UpgradeRTK()
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("upgrade through the release channel: %v; want the checksum error", err)
	}
	// rtkPath finds the runnable rtk here, rtk.exe on Windows
	want := script
	if filepath.Base(bin) == "rtk.exe" {
		want = "rtk"
	}
	if got := read(t, bin); got != want {
		t.Fatalf("the rtk changed to %q", got)
	}
}

// netube99 on X: an rtk.exe in winget's own folder (…\WinGet\Packages\
// rtk-ai.rtk_…, or its link in WinGet\Links) that winget has no record of
// got `winget upgrade`, which failed with 0x8A150014 "winget finds no
// rtk-ai.rtk package". It is upgraded from rtk's own release now, over the
// file the link points to; one winget does list still goes to winget.
func TestRTKWingetFolderWithoutRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	h := sandbox(t)
	arch, base := rtkArch, rtkDownloadBase
	rtkArch = "amd64"
	t.Cleanup(func() { rtkArch, rtkDownloadBase = arch, base })
	latestServer(t, "v0.51.0")
	pkg := filepath.Join(h, "AppData", "Local", "Microsoft", "WinGet", "Packages", "rtk-ai.rtk_x")
	real := filepath.Join(pkg, "rtk")
	ver := filepath.Join(pkg, "VERSION")
	testenv.Program(t, real, strings.ReplaceAll(versionedRTK, "VERSION", ver))
	write(t, ver, "0.50.0")
	links := filepath.Join(h, "AppData", "Local", "Microsoft", "WinGet", "Links")
	os.MkdirAll(links, 0o755)
	link := filepath.Join(links, "rtk")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", links+string(os.PathListSeparator)+filepath.Join(h, "tools"))
	newRTK := strings.ReplaceAll(versionedRTK, `"VERSION"`, `"`+ver+`"`)
	newRTK = strings.ReplaceAll(newRTK, "$(/bin/cat \""+ver+"\")", "0.51.0")
	rtkDownloadBase = fakeRTKRelease(t, newRTK, false)

	// winget has records, none of them this folder
	wingetOn(t, h, wingetUnlisted, 0, func() ([]string, bool) { return []string{filepath.Join(h, "elsewhere")}, true })
	if c := rtkUpgrader(link); len(c) != 2 || c[0] != rtkWindowsRelease || c[1] != real {
		t.Fatalf("no record: upgrader %q; want %s %s", c, rtkWindowsRelease, real)
	}
	v, err := UpgradeRTK()
	if err != nil || v.Version != "0.51.0" {
		t.Fatalf("%+v, %v", v, err)
	}

	// winget has it: winget upgrades it
	wingetOn(t, h, wingetListed, 0, func() ([]string, bool) { return []string{pkg}, true })
	if c := rtkUpgrader(link); len(c) == 0 || c[0] != "winget" {
		t.Fatalf("recorded: upgrader %q; want winget upgrade", c)
	}
}
