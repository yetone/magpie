package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
)

// noApps fails when any path under dir is named like an app: LaunchServices
// registers such a folder as another magpie, and Spotlight lists it after
// it is deleted (#3965).
func noApps(t *testing.T, dir string) {
	t.Helper()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(d.Name(), ".app") {
			t.Errorf("%s is named like an app", p)
		}
		return nil
	})
}

// stubUnregister records what LaunchServices is told to forget, instead of
// telling it.
func stubUnregister(t *testing.T) *[]string {
	t.Helper()
	var got []string
	was := unregister
	unregister = func(p string) { got = append(got, p) }
	t.Cleanup(func() { unregister = was })
	return &got
}

// Install moves the old app aside and the staged one in under names that
// aren't an app's, then removes the staging folder and nothing beside it.
func TestInstallStagesNoApp(t *testing.T) {
	apps := filepath.Join(t.TempDir(), "Applications")
	bundle := filepath.Join(apps, "magpie.app")
	app(t, bundle, "old")
	app(t, filepath.Join(apps, "Other.app"), "other")
	staged := filepath.Join(stageDir(apps), stagedName)
	app(t, staged, "new")
	noApps(t, filepath.Join(apps, ".magpie-update"))

	if err := Install(staged, bundle); err != nil {
		t.Fatal(err)
	}
	if version(t, bundle) != "new" {
		t.Error("the new app isn't in place")
	}
	if _, err := os.Stat(filepath.Join(apps, "Other.app")); err != nil {
		t.Errorf("Install removed an app beside magpie: %v", err)
	}
	if _, err := os.Stat(filepath.Join(apps, ".magpie-update")); !os.IsNotExist(err) {
		t.Errorf(".magpie-update is left: %v", err)
	}
}

// InstallAsAdmin moves the old app aside to a name that isn't an app's, on
// the app's own volume (a move to the cache is a copy and a delete, and
// the magpie running from it is then refused every program it starts,
// #4983), and removes there only the copies nothing runs from.
func TestInstallAsAdminAsideIsNoApp(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osascript")
	}
	defer func(r func(string, ...string) ([]byte, error)) { run = r }(run)
	var script string
	run = func(name string, args ...string) ([]byte, error) {
		script = args[len(args)-1]
		return nil, nil
	}
	apps := filepath.Join(t.TempDir(), "Applications")
	bundle := filepath.Join(apps, "magpie.app")
	unused, inUse := filepath.Join(retiredDir(bundle), "1-unused"), filepath.Join(retiredDir(bundle), "2-inuse")
	for _, p := range []string{unused, inUse} {
		os.MkdirAll(filepath.Join(p, "Contents", "MacOS"), 0o755)
	}
	was := runFrom
	runFrom = func(p string) bool { return p == inUse }
	defer func() { runFrom = was }()
	cache := filepath.Join(t.TempDir(), "update")
	if err := InstallAsAdmin(filepath.Join(cache, stagedName), bundle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "mv '"+bundle+"' '"+retiredDir(bundle)+string(filepath.Separator)) {
		t.Errorf("the swap doesn't move the app aside beside it, to %s: %s", retiredDir(bundle), script)
	}
	if strings.Count(script, cache) != 1 { // the staged app's only
		t.Errorf("the swap moves the app to the cache: %s", script)
	}
	if !strings.Contains(script, appleString(shellQuote(unused))) || strings.Contains(script, appleString(shellQuote(inUse))) {
		t.Errorf("the swap doesn't remove just the unused copy: %s", script)
	}
	if strings.Count(script, ".app") != strings.Count(script, bundle) {
		t.Errorf("the swap names another app: %s", script)
	}
}

// fakeApp makes an ad-hoc signed app bundle at path, with a bundle id of
// its own so LaunchServices never mixes it up with magpie.
func fakeApp(t *testing.T, path, v string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.example.magpie-update-test</string>
<key>CFBundleExecutable</key><string>magpie</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>` + v + `</string>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(path, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "Contents", "MacOS", "magpie"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("codesign", "-s", "-", "--force", path).CombinedOutput(); err != nil {
		t.Fatalf("codesign: %v: %s", err, b)
	}
}

// Stage unpacks the release's magpie.app (the zip's one top folder, as
// the release builds it) to a name that isn't an app's, tells
// LaunchServices to forget the name it was unpacked under, and Install
// puts it in place.
func TestStageUnpacksNoApp(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ditto and codesign")
	}
	forgot := stubUnregister(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	fakeApp(t, filepath.Join(src, "magpie.app"), "2.0")
	zip := filepath.Join(root, "a.zip")
	cmd := exec.Command("ditto", "-c", "-k", "--keepParent", "magpie.app", zip)
	cmd.Dir = src
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v: %s", err, b)
	}
	body, err := os.ReadFile(zip)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	apps := filepath.Join(root, "Applications")
	bundle := filepath.Join(apps, "magpie.app")
	fakeApp(t, bundle, "1.0")
	rel := &Release{Version: "2.0", Assets: map[string]Asset{AppAsset(): {URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}}}
	staged, err := Stage(context.Background(), rel, bundle)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(apps, ".magpie-update")
	if staged != filepath.Join(stage, stagedName) {
		t.Errorf("staged at %s", staged)
	}
	noApps(t, stage)
	if want := filepath.Join(stage, "unzip", "magpie.app"); !slices.Contains(*forgot, want) {
		t.Errorf("LaunchServices was told to forget %q, want %s", *forgot, want)
	}
	if err := Install(staged, bundle); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")); !strings.Contains(string(b), "<string>2.0</string>") {
		t.Error("the new app isn't in place")
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf(".magpie-update is left: %v", err)
	}
}

// ForgetOldStaging tells LaunchServices to forget where earlier updates
// unpacked the new app and moved the old one aside, beside the app and in
// the cache, once.
func TestForgetOldStaging(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchServices")
	}
	forgot := stubUnregister(t)
	marker := filepath.Join(appdir.Cache(), forgotMarker)
	os.Remove(marker)
	t.Cleanup(func() { os.Remove(marker) })
	cache, err := appdir.SystemCache()
	if err != nil {
		t.Fatal(err)
	}
	ForgetOldStaging("/Applications/magpie.app")
	want := []string{
		"/Applications/.magpie-update/app/magpie.app",
		"/Applications/.magpie-update/old.app",
		filepath.Join(cache, "update", "app", "magpie.app"),
		filepath.Join(cache, "update", "old.app"),
	}
	if !slices.Equal(*forgot, want) {
		t.Errorf("forgot %q, want %q", *forgot, want)
	}
	*forgot = nil
	ForgetOldStaging("/Applications/magpie.app")
	if len(*forgot) != 0 {
		t.Errorf("forgot %q again", *forgot)
	}
}
