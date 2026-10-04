package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// StaleCLI is the path of a `magpie` command that is a copied file behind
// the app, or "" when there's nothing to say. install.sh links the command
// to magpie.app's binary so it follows every GUI update for free; a command
// that arrived as a copy — an older installer, a hand cp, or the standalone
// magpie-cli build — keeps running its own build once the app moves on, and
// a command behind the app is a stale build: one that predates the WebDAV
// short-write check (#531) truncated a remote backup on `magpie webdav now`,
// wedging the sync.
//
// A copy is only worth mentioning when it actually trails the app, so this
// reads the copy's version and stays quiet when it is at or ahead of
// appVersion, or isn't a release at all (a build from source, or one that
// won't run — none of those is a stale release to nag about). Reading the
// version runs the old binary, and that isn't read-only: magpie's start-up
// runs its migrations before `version` answers. So only `magpie update`,
// which the user started, asks this — the app's window uses CopiedCLI and
// its advice can be dismissed instead. magpie doesn't rewrite the user's
// PATH — the fix is theirs: re-run install.sh, or link it by hand, which is
// what the advice says. Only on the Mac, where the app and the command are
// two places; on Linux they are one file.
func StaleCLI(appVersion string) string {
	cli := CopiedCLI()
	if cli == "" {
		return ""
	}
	old := cliVersion(cli)
	if !Released(old) || !Newer(appVersion, old) {
		return "" // a source build, an unreadable copy, or one at/above the app
	}
	return cli
}

// CopiedCLI is the command's path when it's a plain copied file rather than
// install.sh's link, or "" when it's absent, a link, or not a regular file.
// A bare Lstat — nothing is run, nothing is written — so the app's window
// can ask on every launch. It can't tell a stale copy from a current one
// (that needs the copy's version, which only `magpie update` may read), so
// what the window says off it is advice with a dismiss, not a warning that
// knows. Only on the Mac; on Linux the app and the command are one file.
func CopiedCLI() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	bin := appdir.Getenv("MAGPIE_BIN_DIR")
	if bin == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return ""
		}
		bin = filepath.Join(home, ".local", "bin")
	}
	cli := filepath.Join(bin, "magpie")
	st, err := os.Lstat(cli)
	if err != nil || !st.Mode().IsRegular() {
		return "" // absent, the installer's link, or nothing to point at
	}
	return cli
}

// cliVersion asks the binary at path what version it is, and is a var so
// tests need not build or run one. The command prints "magpie <version>".
// The wait is short: it runs while the app is starting to decide whether to
// say anything, so a copy that hangs must not stall it. Running the old
// binary isn't purely reading — magpie's start-up runs its migrations first
// — but that's the only way to tell a stale release from a current one, and
// it's reached only for a file already known to be a copy.
var cliVersion = func(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := proc.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "magpie"))
}
