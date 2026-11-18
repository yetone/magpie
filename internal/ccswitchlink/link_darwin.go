package ccswitchlink

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// bundleID is CC Switch's (its tauri.conf.json "identifier").
const bundleID = "com.ccswitch.desktop"

// lsregister edits Launch Services' list of apps; -f registers a bundle
// again from its Info.plist as it is now.
const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

// run runs a command to its end; tests stand in for it.
var run = func(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := proc.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// executable is this program's path; tests stand in for it.
var executable = os.Executable

// forward has Launch Services open the link with CC Switch by its bundle
// id, which fails when no app has that id. Then magpie's own bundle is
// registered again: its Info.plist claims magpie:// only since 03314b25a,
// so Launch Services forgets the ccswitch:// claim it remembered.
func forward(link string) error {
	if err := run("open", "-b", bundleID, link); err != nil {
		return fmt.Errorf("%w (%v)", ErrNotInstalled, err)
	}
	if b := bundle(); b != "" {
		_ = run(lsregister, "-f", b)
	}
	return nil
}

// bundle is the .app this executable is in, "" outside one.
func bundle() string {
	exe, err := executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || filepath.Ext(app) != ".app" {
		return ""
	}
	return app
}
