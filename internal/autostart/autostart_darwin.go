package autostart

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"

	"github.com/yetone/magpie/internal/edit"
)

const label = "com.yetone.magpie"

func record() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// launchd loads no job that names neither its own label nor a program to
// run: a record a write left short of either sits in LaunchAgents, is
// refused, and magpie never opens at login however long the file is there.
var ourLabel = regexp.MustCompile(`<key>Label</key>\s*<string>` + regexp.QuoteMeta(label) + `</string>`)

func enabled() bool {
	b, err := os.ReadFile(record())
	return err == nil && ourLabel.Match(b) && program.Match(b)
}

// a launch agent the system loads at the next login: the app itself, run
// once, not kept alive — quitting magpie quits it until then
func enable(exe string) error {
	p := record()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(p, plist(exe))
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

// with a path in the first string: an empty one names nothing to run, and
// launchd refuses the job
var program = regexp.MustCompile(`<key>ProgramArguments</key>\s*<array><string>([^<]+)</string>`)

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
	return edit.WriteAtomic(p, want)
}

func disable() error {
	if err := os.Remove(record()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
