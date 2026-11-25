package update

// A new version that this computer won't run (#894): Windows 11's Smart
// App Control, or another application control policy, refuses magpie's
// unsigned builds it doesn't know yet, and an update that put one in place
// left nothing that starts. The download is tried before it goes in
// (canStart), the restart into it waits for it to say it is up
// (AwaitPredecessor writes MAGPIE_STARTED's file), and a version refused
// either way is put back and noted (NoteBlocked), so the automatic checks
// don't download it again; a check asked for tries it once more.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// BlockedError is a new version that couldn't be started on this computer.
// Nothing of it is left in place: the version running is still the one
// installed.
type BlockedError struct {
	Err error
}

func (e *BlockedError) Error() string {
	if e.Policy() {
		return "Windows didn't let the new version start (" + e.Err.Error() + "): Smart App Control or another application control policy blocks magpie's unsigned builds; download it from " + Site + " and run it yourself, or turn Smart App Control off, then check for updates again"
	}
	return "the new version couldn't start: " + e.Err.Error() + "; download it from " + Site + " and run it yourself"
}

func (e *BlockedError) Unwrap() error { return e.Err }

// Policy is whether Windows' code integrity refused the program: Smart App
// Control, App Control for Business (WDAC), AppLocker or a software
// restriction policy, which the user can't let through from magpie.
func (e *BlockedError) Policy() bool { return policyRefused(e.Err) }

// errAccessDisabledByPolicy is AppLocker's and software restriction
// policies' refusal; ERROR_SYSTEM_INTEGRITY_* (4550…4599) are code
// integrity's, Smart App Control's among them ("An Application Control
// policy has blocked this file", ERROR_SYSTEM_INTEGRITY_POLICY_VIOLATION).
const (
	errAccessDisabledByPolicy syscall.Errno = 1260
	errIntegrityPolicy        syscall.Errno = 4551
)

func policyRefused(err error) bool {
	var n syscall.Errno
	if !errors.As(err, &n) {
		return false
	}
	return n == errAccessDisabledByPolicy || n >= 4550 && n < 4600
}

// canStart is whether the program at path can be started here, which is
// tried before it is put in place: Windows starts it suspended and ends
// it at once, so none of it runs; elsewhere nothing refuses a build that
// downloaded whole. A var for tests.
var canStart = trialStart

// startWait is how long a restarted magpie has to say it is up.
var startWait = 20 * time.Second

// startedEnv names the file a relaunched magpie writes once it runs
// (AwaitPredecessor), for the one that started it to know.
const startedEnv = "MAGPIE_STARTED"

// launch starts a program with args and env, detached from this one; the
// channel is closed when it exits, and kill ends it. A var for tests.
var launch = func(exe string, args, env []string) (<-chan struct{}, func(), error) {
	cmd := proc.Command(exe, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	return exited, func() { cmd.Process.Kill() }, nil
}

// startChecked starts the new version at exe and waits for it to say it
// is up. When it can't be started, quits first, or says nothing in time,
// it is ended and old — this running version, moved aside for it — goes
// back in its place.
func startChecked(exe, old string, args, env []string) error {
	marker := markerPath("magpie-started-"+strconv.Itoa(os.Getpid()), exe)
	defer os.Remove(marker)
	exited, kill, err := launch(exe, args, append(env, startedEnv+"="+marker))
	if err == nil {
		err = awaitStarted(marker, exited, kill)
	}
	if err == nil {
		return nil
	}
	blocked := &BlockedError{Err: err}
	if rerr := putBack(exe, old); rerr != nil {
		return errors.Join(blocked, rerr)
	}
	return blocked
}

// markerPath is a new, empty file, named name and a random suffix, that a
// magpie started from exe writes and this one reads: in the temp folder,
// else beside exe, a folder this update has just written into. A TEMP
// naming a folder that isn't there (a RAM disk not mounted, a drive taken
// out) leaves no temp folder, and a started version that couldn't write
// its word there was ended at startWait and the update called refused,
// every time (reproduced with TEMP=R:\Temp on the Windows box; one way to
// 柒拾's "didn't start within 20s" on Discord). The file is made here,
// in the folder that took it, and left for the started magpie to write
// over: awaitStarted waits for it to hold something.
//
// The temp folder can be shared (/tmp on Linux), so the file is never at
// a name someone else can guess and make first: os.CreateTemp makes it
// new (O_EXCL, never through a link) and the user's own (0600). A fixed
// "magpie-started-<pid>" there could be another user's link, and writing
// it emptied and then wrote over whatever file of this user's the link
// named; or their file, already holding a word, said a new version that
// never ran was up.
func markerPath(name, exe string) string {
	dirs := []string{os.TempDir(), filepath.Dir(exe)}
	for i, dir := range dirs {
		f, err := os.CreateTemp(dir, name+"-*")
		if err != nil {
			if i == 0 {
				log.Printf("update: the temp folder %s can't be written (%v); the new version says it is up beside %s", dir, err, filepath.Base(exe))
			}
			continue
		}
		f.Close()
		return f.Name()
	}
	return filepath.Join(dirs[0], name)
}

// awaitStarted waits for marker to be written, the started program's
// exit (exited closing) or startWait, whichever comes first.
func awaitStarted(marker string, exited <-chan struct{}, kill func()) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(startWait)
	for {
		if said(marker) {
			return nil
		}
		select {
		case <-exited:
			if said(marker) {
				return nil // it said so and then went on as another
			}
			return errors.New("it quit as soon as it started")
		case <-deadline:
			kill()
			// its file let go of, to be put back over
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
			}
			return fmt.Errorf("it didn't start within %s", startWait)
		case <-tick.C:
		}
	}
}

// said is whether the started magpie wrote its word (its pid) in marker.
func said(marker string) bool {
	fi, err := os.Stat(marker)
	return err == nil && fi.Size() > 0
}

// putBack puts old in exe's place, the new version that was there out of
// the way (removed, else kept as exe.blocked).
func putBack(exe, old string) error {
	var err error
	for i := 0; ; i++ {
		if err = os.Remove(exe); err == nil || os.IsNotExist(err) {
			break
		}
		if err = os.Rename(exe, exe+".blocked"); err == nil {
			break
		}
		if i == len(asideWaits) {
			return fmt.Errorf("couldn't take the new version out of %s: %w", filepath.Base(exe), err)
		}
		time.Sleep(asideWaits[i])
	}
	if err := os.Rename(old, exe); err != nil {
		return fmt.Errorf("couldn't put %s back from %s: %w", filepath.Base(exe), old, err)
	}
	return nil
}

// reportStarted writes the file the magpie that started this one waits
// for, once.
func reportStarted() {
	p := os.Getenv(startedEnv)
	if p == "" {
		return
	}
	os.Unsetenv(startedEnv)
	if err := os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		log.Printf("update: couldn't tell the magpie that started this one it is up: %v", err)
	}
}

// Blocked is a version this computer refused to start, and why.
type Blocked struct {
	Version string    `json:"version"`
	Error   string    `json:"error"`
	Policy  bool      `json:"policy,omitempty"` // application control (Smart App Control)
	At      time.Time `json:"at"`
}

// blockedFile is this computer's own note, never synced.
func blockedFile() string { return filepath.Join(appdir.Config(), "update-blocked.json") }

// NoteBlocked notes that version couldn't start here.
func NoteBlocked(version string, err error) Blocked {
	b := Blocked{Version: version, Error: err.Error(), At: time.Now()}
	var be *BlockedError
	if errors.As(err, &be) {
		b.Error, b.Policy = be.Err.Error(), be.Policy()
	}
	if data, merr := json.Marshal(b); merr == nil {
		os.MkdirAll(filepath.Dir(blockedFile()), 0o755)
		os.WriteFile(blockedFile(), data, 0o644)
	}
	return b
}

// ReadBlocked is the version noted as refused here, while it is newer
// than current; once current has caught up (put in by hand), the note
// goes.
func ReadBlocked(current string) *Blocked {
	data, err := os.ReadFile(blockedFile())
	if err != nil {
		return nil
	}
	var b Blocked
	if json.Unmarshal(data, &b) != nil || b.Version == "" || !Newer(b.Version, current) {
		os.Remove(blockedFile())
		return nil
	}
	return &b
}

// ClearBlocked forgets the note: the version started after all.
func ClearBlocked() { os.Remove(blockedFile()) }

// Respawn starts this program again with the arguments it was started
// with and extra (NAME=value) added to its environment, detached. The new
// process waits for this one to exit, as a relaunch does (AwaitPredecessor).
func Respawn(extra ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	env := append(os.Environ(), "MAGPIE_REPLACES="+strconv.Itoa(os.Getpid()))
	_, _, err = launch(exe, os.Args[1:], append(env, extra...))
	return err
}
