package autostart

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
)

const label = "com.yetone.magpie"

func record() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func enabled() bool {
	_, err := os.Stat(record())
	return err == nil
}

// a launch agent the system loads at the next login: the app itself, run
// once, not kept alive — quitting magpie quits it until then
func enable(exe string) error {
	p := record()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, plist(exe), 0o644)
}

// plist is the launch agent for exe. AbandonProcessGroup: launchd kills
// whatever a job started once the job exits, and a restart to update is
// the job quitting with the shell that opens the new version still
// waiting for it to go — without the key, a magpie opened at login never
// came back from an update.
func plist(exe string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>%s</string></array>
	<key>RunAtLoad</key><true/>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>ProcessType</key><string>Interactive</string>
	<key>AbandonProcessGroup</key><true/>
</dict>
</plist>
`, label, html.EscapeString(exe), Arg))
}

var program = regexp.MustCompile(`<key>ProgramArguments</key>\s*<array><string>([^<]*)</string>`)

// refresh writes a launch agent an older magpie wrote over as this one
// would, for the program it names: the path stays the one the user turned
// it on for, whichever copy of magpie is running now.
func refresh() error {
	p := record()
	b, err := os.ReadFile(p)
	if err != nil {
		return nil // off: stays off
	}
	m := program.FindSubmatch(b)
	if m == nil {
		return nil // not one magpie wrote
	}
	want := plist(html.UnescapeString(string(m[1])))
	if bytes.Equal(b, want) {
		return nil
	}
	return os.WriteFile(p, want, 0o644)
}

func disable() error {
	if err := os.Remove(record()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
