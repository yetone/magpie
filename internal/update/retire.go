package update

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// retiredName is the folder beside the app where Install moves the copy
// it replaces. It is kept while any process still runs from it: the magpie
// that installed the update until it has quit, and a `magpie web` or
// `magpie serve` started from the same app, which go on running the old
// version until they restart. On macOS 13 a process whose program file is
// gone is refused every program it starts after (syspolicyd: "Unable
// (errno: 2) to read file", "Security policy would not allow process"),
// the child ended before its first instruction: the open of the new
// version, the shell that relaunches it, and the agents and plugin hosts
// a `magpie web` starts (#2362, #4983). Outside stageDir, so a download
// starting over (Stage) never clears it.
const retiredName = ".magpie-old"

// retiredDir is the folder of the copies Install moved aside from bundle.
func retiredDir(bundle string) string {
	return filepath.Join(filepath.Dir(bundle), retiredName)
}

// retiredPath is a place of its own in retiredDir for the copy an install
// moves aside now: on the app's volume, so the move is a rename and the
// running program's file stays the same file, and not named like an app
// (#3965).
func retiredPath(bundle string) string {
	return filepath.Join(retiredDir(bundle), fmt.Sprintf("%d-%s", os.Getpid(), rand.Text()[:10]))
}

// RemoveRetired removes the copies Install moved aside from bundle that no
// process runs from any more, and their folder once it is empty. One in
// use, or that can't be told, is left for the next time; so is one the
// administrator moved, which only InstallAsAdmin's script can remove.
func RemoveRetired(bundle string) {
	for _, p := range unusedRetired(bundle) {
		if err := os.RemoveAll(p); err != nil && !errors.Is(err, os.ErrPermission) {
			log.Println("update: the app an update replaced is kept:", err)
		}
	}
	os.Remove(retiredDir(bundle)) // only when empty
}

// unusedRetired is the copies in bundle's retiredDir no process runs from.
func unusedRetired(bundle string) []string {
	if runtime.GOOS != "darwin" || bundle == "" {
		return nil
	}
	entries, err := os.ReadDir(retiredDir(bundle))
	if err != nil {
		return nil
	}
	var unused []string
	for _, e := range entries {
		if !e.IsDir() { // a link is never followed to what it names
			continue
		}
		p := filepath.Join(retiredDir(bundle), e.Name())
		if !runFrom(p) {
			unused = append(unused, p)
		}
	}
	return unused
}

// runFrom says whether a process may be running a program in app's
// Contents/MacOS: true unless lsof, asked about each of them, finds none
// open. The process table holds the path a program was started at, which
// the move aside doesn't change, so it is asked by the file itself.
var runFrom = func(app string) bool {
	dir := filepath.Join(app, "Contents", "MacOS")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false // nothing to run: a copy cut short
	}
	if err != nil {
		return true
	}
	args := []string{"-F", "p", "--"}
	for _, e := range entries {
		if e.Type().IsRegular() {
			args = append(args, filepath.Join(dir, e.Name()))
		}
	}
	if len(args) == 3 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := proc.CommandContext(ctx, "/usr/sbin/lsof", args...).Output()
	// lsof exits 1, saying nothing, when no process has any of them open;
	// anything else (one found, a time out, a warning) keeps the copy
	var exit *exec.ExitError
	return ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 || len(out) != 0
}

// retiredEvery is how often a running app removes the copies an update
// replaced (KeepRetiredClear): the one the magpie before ran from is in use
// until it has finished its requests and quit, up to about 18 minutes after
// it handed the gateway over.
const retiredEvery = 30 * time.Minute

// KeepRetiredClear removes what RemoveRetired removes for bundle now and
// every retiredEvery, until ctx ends.
func KeepRetiredClear(ctx context.Context, bundle string) {
	for {
		RemoveRetired(bundle)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retiredEvery):
		}
	}
}
