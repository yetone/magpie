package autostart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// what magpie up to now wrote for Open at login
const olderPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>com.yetone.magpie</string>
	<key>ProgramArguments</key>
	<array><string>/Applications/Tom &amp; Jerry/magpie.app/Contents/MacOS/magpie</string><string>tray</string></array>
	<key>RunAtLoad</key><true/>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`

// launchd kills what a job started once the job exits, so the shell a
// restart to update leaves to open the new version died with a magpie
// opened at login: the launch agent lets them go.
func TestLaunchAgentLetsTheRelaunchLive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(record())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "<key>AbandonProcessGroup</key><true/>") {
		t.Fatalf("the launch agent doesn't abandon its process group:\n%s", b)
	}
	if out, err := exec.Command("plutil", "-lint", record()).CombinedOutput(); err != nil {
		t.Fatalf("not a valid plist: %s", out)
	}
}

// An older magpie's launch agent is written again as this one writes it,
// for the program it named; one that's off stays off.
func TestRefreshOlderLaunchAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := Refresh(); err != nil || Enabled() {
		t.Fatalf("Refresh turned Open at login on: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "Library")); !os.IsNotExist(err) {
		t.Fatal("Refresh made the LaunchAgents folder for one that's off")
	}

	p := record()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(olderPlist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Refresh(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	want := plist("/Applications/Tom & Jerry/magpie.app/Contents/MacOS/magpie")
	if string(b) != string(want) {
		t.Fatalf("not rewritten as this version writes it:\n%s\nwant:\n%s", b, want)
	}

	// one magpie didn't write is left as it is
	other := "<plist><dict><key>Label</key><string>x</string></dict></plist>"
	os.WriteFile(p, []byte(other), 0o644)
	if err := Refresh(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != other {
		t.Fatalf("a launch agent magpie didn't write was changed:\n%s", b)
	}
}
