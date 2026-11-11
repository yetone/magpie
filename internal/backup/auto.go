package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Backups made by themselves (jwaitq on Discord, who got their setup back
// on Windows only from an export in Downloads): every so often the setup is
// written to a folder on this computer, the last few kept, and any of them
// can be put back from Settings › Sync and backup.
//
// What goes in is what an export without the API keys holds, by the same
// Collect(false): no key, no key-like header, no gateway key, no token, no
// sign-in (and no proxy, autoCollect). There is no passphrase to seal with
// here, so nothing that needs one is written; the file is the bundle as JSON, 0600 in a 0700 folder.
// A restore of one is the export's import (Restore, every part): a provider
// that came without a key keeps the one it has here, so going back to a
// version never takes a key away.

// AutoConfig is how backups are made by themselves. It is this computer's
// own, in autobackup.json beside the settings, never in a bundle or a sync.
// The zero value is the default, so a field the user never set follows it
// and one they set is kept as set.
type AutoConfig struct {
	Off   bool   `json:"off,omitempty"`   // turned off by the user; on unless so
	Hours int    `json:"hours,omitempty"` // between backups; 0 is DefaultHours
	Keep  int    `json:"keep,omitempty"`  // backups kept of each kind; 0 is DefaultKeep
	Dir   string `json:"dir,omitempty"`   // the folder; "" is AutoDefaultDir
	// SignIns puts the sign-ins (subscription accounts, the plugins'
	// sign-ins) in each backup too, sealed with Passphrase: the rest of an
	// automatic backup is plain JSON without credentials. Off unless the
	// user turns it on.
	SignIns    bool   `json:"signIns,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// On by default: losing the setup is what this is for, and a user who
// never found the switch is the one it saves. Once a day, deduplicated (a
// day with no change makes none), so the last week of changes is kept.
const (
	DefaultHours = 24
	DefaultKeep  = 7
)

// AutoHours and AutoKeeps are what Settings offers.
var (
	AutoHours = []int{1, 6, 24, 168}
	AutoKeeps = []int{3, 7, 14, 30}
)

const autoFormat = "magpie-auto-backup"

// Reasons a backup was made: by the schedule (or Back up now), or as what
// was here before a restore, to undo it.
const (
	ReasonAuto    = "auto"
	ReasonRestore = "restore"
)

func autoPath() string { return filepath.Join(settings.Dir(), "autobackup.json") }

// AutoDefaultDir is where backups go unless the user picked a folder. Not
// settings.Dir()/backups: that is the library's, which prunes it.
func AutoDefaultDir() string { return filepath.Join(settings.Dir(), "auto-backups") }

func (c AutoConfig) every() time.Duration {
	if c.Hours > 0 {
		return time.Duration(c.Hours) * time.Hour
	}
	return DefaultHours * time.Hour
}

func (c AutoConfig) keep() int {
	if c.Keep > 0 {
		return c.Keep
	}
	return DefaultKeep
}

func (c AutoConfig) dir() string {
	if c.Dir != "" {
		return c.Dir
	}
	return AutoDefaultDir()
}

// LoadAuto reads the setup. A file that isn't there is the defaults; one
// that can't be read or parsed is an error, never the defaults: the user may
// have turned backups off in it.
func LoadAuto() (AutoConfig, error) {
	var c AutoConfig
	data, err := os.ReadFile(autoPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(strings.TrimSpace(string(edit.TrimBOM(data)))) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(edit.TrimBOM(data), &c); err != nil {
		return c, fmt.Errorf("%s: %w", autoPath(), err)
	}
	return c, nil
}

// SaveAuto writes the setup, after checking it is one Settings offers.
func SaveAuto(c AutoConfig) error {
	if c.Hours != 0 && !slices.Contains(AutoHours, c.Hours) {
		return fmt.Errorf("backups can be made every %v hours, not %d", AutoHours, c.Hours)
	}
	if c.Keep < 0 || c.Keep > 100 {
		return fmt.Errorf("keep between 1 and 100 backups, not %d", c.Keep)
	}
	if c.Dir != "" && !filepath.IsAbs(c.Dir) {
		return fmt.Errorf("the backup folder must be a full path, not %q", c.Dir)
	}
	if c.SignIns && c.Passphrase == "" {
		return errors.New("sign-ins go in a backup sealed with a passphrase: set one")
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		return err
	}
	autoMu.Lock()
	defer autoMu.Unlock()
	autoChecked = time.Time{} // a new folder or interval is looked at anew
	if err := edit.WriteAtomic(autoPath(), append(data, '\n')); err != nil {
		return err
	}
	return os.Chmod(autoPath(), 0o600) // the passphrase is in it
}

// AutoBackup is one backup in the folder.
type AutoBackup struct {
	Name      string    `json:"name"`
	Made      time.Time `json:"made"`
	Reason    string    `json:"reason"`
	App       string    `json:"app,omitempty"`
	Size      int64     `json:"size"`
	Providers int       `json:"providers"`
	Library   bool      `json:"library"`
	// SignIns: the backup has the sign-ins in it, sealed with the
	// passphrase set when it was made.
	SignIns bool `json:"signIns,omitempty"`
	sum     string
	siSum   string
}

// autoFile is a backup as written: what it is and when, then the bundle,
// with a hash of the bundle's bytes so a file damaged after it was written
// is never offered.
type autoFile struct {
	Format    string          `json:"format"`
	Version   int             `json:"version"`
	Reason    string          `json:"reason"`
	Made      time.Time       `json:"made"`
	App       string          `json:"app,omitempty"`
	Providers int             `json:"providers"`
	Library   bool            `json:"library"`
	Sum       string          `json:"sum"`
	Bundle    json.RawMessage `json:"bundle"`
	// SignIns is the sign-ins (provider.SignIns as JSON) sealed with the
	// passphrase (SealData), never in the clear; SignInsSum tells a
	// backup's from the next one's without opening them.
	SignIns    []byte `json:"signIns,omitempty"`
	SignInsSum string `json:"signInsSum,omitempty"`
}

// autoSealed is the sign-ins an automatic backup carries, sealed.
type autoSealed struct {
	data []byte
	sum  string
}

// autoSignIns seals the sign-ins here for an automatic backup, when c
// has them go in one.
func autoSignIns(c AutoConfig) (autoSealed, error) {
	if !c.SignIns {
		return autoSealed{}, nil
	}
	s, err := provider.CollectSignIns()
	if err != nil {
		return autoSealed{}, fmt.Errorf("the sign-ins couldn't be read: %w", err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return autoSealed{}, err
	}
	sum := sha256.Sum256(raw)
	sealed, err := SealData(raw, c.Passphrase)
	if err != nil {
		return autoSealed{}, err
	}
	return autoSealed{data: sealed, sum: hex.EncodeToString(sum[:])}, nil
}

// autoName is a backup's file name; anything else in the folder (the
// temporary file one is written to, the user's own files) is never listed,
// put back or removed.
var autoName = lazyre.New(`^magpie-(\d{8}-\d{6})(?:-\d+)?-(auto|restore)\.json$`)

const autoTemp = ".magpie-backup-*.tmp"

// autoRename puts a written backup at its name: a var so a test can stop
// a write there.
var autoRename = os.Rename

var (
	autoMu      sync.Mutex
	autoChecked time.Time // when the schedule last looked and found its backup made or not needed
	autoErr     string    // why the last scheduled backup failed, "" once one goes
	// what was read of each file, by name, while its size and time stay
	autoSeen = map[string]autoSeenFile{}
)

type autoSeenFile struct {
	size int64
	mod  time.Time
	b    AutoBackup
	ok   bool
}

// bundleBytes is the bundle as a backup holds it, with what changes on
// every collect (when, by which magpie) taken out, so the same setup is the
// same bytes and makes no new backup.
func bundleBytes(b Bundle) ([]byte, string, error) {
	b.Created, b.App = time.Time{}, ""
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, "", err
	}
	s := sha256.Sum256(raw)
	return raw, hex.EncodeToString(s[:]), nil
}

// writeAuto writes b into dir as a backup made at now for reason, through
// a temporary file renamed into place, so a backup cut short (the machine
// going off mid-write) is never at a backup's name.
func writeAuto(dir, reason string, b Bundle, si autoSealed, now time.Time) (AutoBackup, error) {
	raw, sum, err := bundleBytes(b)
	if err != nil {
		return AutoBackup{}, err
	}
	f := autoFile{Format: autoFormat, Version: 1, Reason: reason, Made: now.UTC(), App: b.App,
		Providers: len(b.Providers), Library: b.Library != nil, Sum: sum, Bundle: raw,
		SignIns: si.data, SignInsSum: si.sum}
	data, err := json.Marshal(f)
	if err != nil {
		return AutoBackup{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AutoBackup{}, err
	}
	tmp, err := os.CreateTemp(dir, autoTemp)
	if err != nil {
		return AutoBackup{}, err
	}
	gone := func(err error) (AutoBackup, error) {
		tmp.Close()
		os.Remove(tmp.Name())
		return AutoBackup{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		return gone(err)
	}
	if err := tmp.Sync(); err != nil {
		return gone(err)
	}
	if err := tmp.Close(); err != nil {
		return gone(err)
	}
	os.Chmod(tmp.Name(), 0o600)
	stamp := now.UTC().Format("20060102-150405")
	name := "magpie-" + stamp + "-" + reason + ".json"
	for i := 2; ; i++ { // never over another
		if _, err := os.Lstat(filepath.Join(dir, name)); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("magpie-%s-%d-%s.json", stamp, i, reason)
	}
	if err := autoRename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		os.Remove(tmp.Name())
		return AutoBackup{}, err
	}
	return AutoBackup{Name: name, Made: f.Made, Reason: reason, App: f.App, Size: int64(len(data)),
		Providers: f.Providers, Library: f.Library, SignIns: len(si.data) > 0, sum: sum, siSum: si.sum}, nil
}

// readAuto reads one backup whole, checking its hash.
func readAuto(path string) (AutoBackup, autoFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AutoBackup{}, autoFile{}, err
	}
	var f autoFile
	if err := json.Unmarshal(data, &f); err != nil || f.Format != autoFormat {
		return AutoBackup{}, f, ErrCorrupt
	}
	if f.Version != 1 {
		return AutoBackup{}, f, errors.New("this backup was made by a newer magpie; update magpie to open it")
	}
	s := sha256.Sum256(f.Bundle)
	if hex.EncodeToString(s[:]) != f.Sum || (f.Reason != ReasonAuto && f.Reason != ReasonRestore) {
		return AutoBackup{}, f, ErrCorrupt
	}
	return AutoBackup{Name: filepath.Base(path), Made: f.Made, Reason: f.Reason, App: f.App, Size: int64(len(data)),
		Providers: f.Providers, Library: f.Library, SignIns: len(f.SignIns) > 0, sum: f.Sum, siSum: f.SignInsSum}, f, nil
}

// listAuto is the backups in dir, newest first: only files at a backup's
// name that read whole and match their hash. A folder that isn't there has
// none. Each file is read once while it stays as it was.
func listAuto(dir string) ([]AutoBackup, error) {
	es, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []AutoBackup
	for _, e := range es {
		if !e.Type().IsRegular() || !autoName.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		seen, ok := autoSeen[p]
		if !ok || seen.size != fi.Size() || !seen.mod.Equal(fi.ModTime()) {
			b, _, err := readAuto(p)
			seen = autoSeenFile{size: fi.Size(), mod: fi.ModTime(), b: b, ok: err == nil}
			if len(autoSeen) > 1000 {
				clear(autoSeen)
			}
			autoSeen[p] = seen
		}
		if seen.ok {
			out = append(out, seen.b)
		}
	}
	slices.SortFunc(out, func(a, b AutoBackup) int {
		if c := b.Made.Compare(a.Made); c != 0 {
			return c
		}
		return strings.Compare(b.Name, a.Name)
	})
	return out, nil
}

// pruneAuto removes the backups made for reason past the newest keep, and
// temporary files an earlier write left an hour or more ago.
func pruneAuto(dir, reason string, keep int, now time.Time) {
	list, err := listAuto(dir)
	if err != nil {
		return
	}
	n := 0
	for _, b := range list {
		if b.Reason != reason {
			continue
		}
		if n++; n > keep {
			os.Remove(filepath.Join(dir, b.Name))
		}
	}
	tmps, _ := filepath.Glob(filepath.Join(dir, autoTemp))
	for _, p := range tmps {
		if fi, err := os.Stat(p); err == nil && now.Sub(fi.ModTime()) > time.Hour {
			os.Remove(p)
		}
	}
}

// autoCollect is the setup as an export without keys holds it, and the
// proxies (plugin downloads' too) taken out: it may name a password, the file isn't sealed, and
// a restore keeps this computer's own proxy (KeepOwn) anyway.
func autoCollect(app string) (Bundle, error) {
	b, err := Collect(false, app)
	if err == nil && b.Settings != nil {
		s := *b.Settings
		s.Proxy, s.ProxyCustom = "", ""
		s.PluginProxy, s.PluginProxyCustom = "", ""
		s.UpdateProxy, s.UpdateProxyCustom = "", ""
		s.Proxies = nil
		b.Settings = &s
	}
	return b, err
}

// AutoTick makes a backup if one is due at now: on, the interval gone by
// since the last one (or the clock gone back before it), and the setup
// changed since the newest scheduled backup. It says the backup made, or
// nothing. A setup that can't be read whole makes none (Collect's error).
func AutoTick(now time.Time, app string) (*AutoBackup, error) {
	autoMu.Lock()
	defer autoMu.Unlock()
	c, err := LoadAuto()
	if err != nil {
		return nil, err
	}
	if c.Off {
		return nil, nil
	}
	dir := c.dir()
	list, err := listAuto(dir)
	if err != nil {
		return nil, err
	}
	var newest *AutoBackup
	if i := slices.IndexFunc(list, func(b AutoBackup) bool { return b.Reason == ReasonAuto }); i >= 0 {
		newest = &list[i]
	}
	last := autoChecked
	if newest != nil && newest.Made.After(last) {
		last = newest.Made
	}
	if !last.IsZero() && !now.Before(last) && now.Sub(last) < c.every() {
		return nil, nil
	}
	b, err := autoCollect(app)
	if err != nil {
		return nil, err
	}
	si, err := autoSignIns(c)
	if err != nil {
		return nil, err
	}
	if _, sum, err := bundleBytes(b); err == nil && newest != nil && newest.sum == sum && newest.siSum == si.sum {
		autoChecked = now // nothing changed: none made, looked at again in an interval
		return nil, nil
	}
	made, err := writeAuto(dir, ReasonAuto, b, si, now)
	if err != nil {
		return nil, err
	}
	autoChecked = now
	pruneAuto(dir, ReasonAuto, c.keep(), now)
	return &made, nil
}

// AutoNow makes a scheduled backup at once, changed or not (Back up now).
func AutoNow(app string) (AutoBackup, error) {
	autoMu.Lock()
	defer autoMu.Unlock()
	c, err := LoadAuto()
	if err != nil {
		return AutoBackup{}, err
	}
	b, err := autoCollect(app)
	if err != nil {
		return AutoBackup{}, err
	}
	si, err := autoSignIns(c)
	if err != nil {
		return AutoBackup{}, err
	}
	now := time.Now()
	made, err := writeAuto(c.dir(), ReasonAuto, b, si, now)
	if err != nil {
		return made, err
	}
	autoChecked, autoErr = now, ""
	pruneAuto(c.dir(), ReasonAuto, c.keep(), now)
	return made, nil
}

// AutoRestore puts the backup by name back, every part, as an import does.
// What is here now is backed up first (reason restore), so a restore is
// undone by restoring that one; if it can't be, nothing is put back.
func AutoRestore(name, app string) (Result, AutoBackup, error) {
	autoMu.Lock()
	defer autoMu.Unlock()
	if !autoName.MatchString(name) {
		return Result{}, AutoBackup{}, errors.New("not a backup's name")
	}
	c, err := LoadAuto()
	if err != nil {
		return Result{}, AutoBackup{}, err
	}
	dir := c.dir()
	_, f, err := readAuto(filepath.Join(dir, name))
	if err != nil {
		return Result{}, AutoBackup{}, err
	}
	var b Bundle
	if err := json.Unmarshal(f.Bundle, &b); err != nil {
		return Result{}, AutoBackup{}, ErrCorrupt
	}
	b.Created, b.App = f.Made, f.App
	locked := false // sign-ins the passphrase set now doesn't open
	if len(f.SignIns) > 0 {
		var s provider.SignIns
		raw, err := OpenData(f.SignIns, c.Passphrase)
		if err == nil && json.Unmarshal(raw, &s) == nil {
			b.SignIns = &s
		} else {
			locked = true
		}
	}
	cur, err := autoCollect(app)
	if err != nil {
		return Result{}, AutoBackup{}, fmt.Errorf("couldn't back up the setup here first, so nothing was put back: %w", err)
	}
	now := time.Now()
	si, err := autoSignIns(c)
	if err != nil {
		return Result{}, AutoBackup{}, fmt.Errorf("couldn't back up the setup here first, so nothing was put back: %w", err)
	}
	before, err := writeAuto(dir, ReasonRestore, cur, si, now)
	if err != nil {
		return Result{}, AutoBackup{}, fmt.Errorf("couldn't back up the setup here first, so nothing was put back: %w", err)
	}
	pruneAuto(dir, ReasonRestore, c.keep(), now)
	r, err := Restore(b, All)
	r.SignInsLocked = locked
	return r, before, err
}

// AutoView is what Settings shows.
type AutoView struct {
	On     bool   `json:"on"`
	Hours  int    `json:"hours"`
	Keep   int    `json:"keep"`
	Dir    string `json:"dir"`
	Custom bool   `json:"custom"` // a folder the user picked
	// Inside: the folder is in magpie's own, so a wiped disk or a
	// reinstalled system takes the backups along with what they back up
	// (two_2025. on Discord: everything is on C:, lost on a reinstall).
	// Settings says so beside the folder, for one on another disk.
	Inside  bool         `json:"inside,omitempty"`
	Error   string       `json:"error,omitempty"`
	Backups []AutoBackup `json:"backups"`
	// SignIns: backups have the sign-ins in them, sealed with the
	// passphrase set (never sent: Passphrase says one is).
	SignIns    bool `json:"signIns,omitempty"`
	Passphrase bool `json:"passphrase,omitempty"`
}

// AutoStatus is the setup and the backups in its folder.
func AutoStatus() AutoView {
	c, err := LoadAuto()
	v := AutoView{On: !c.Off, Hours: int(c.every() / time.Hour), Keep: c.keep(), Dir: c.dir(), Custom: c.Dir != "", Backups: []AutoBackup{},
		Inside: inside(c.dir(), settings.Dir()), SignIns: c.SignIns, Passphrase: c.Passphrase != ""}
	if err != nil {
		v.On, v.Error = false, err.Error()
		return v
	}
	autoMu.Lock()
	defer autoMu.Unlock()
	if v.On {
		v.Error = autoErr
	}
	list, err := listAuto(v.Dir)
	if err != nil && v.Error == "" {
		v.Error = err.Error()
	}
	if list != nil {
		v.Backups = list
	}
	return v
}

// inside says whether dir is root or a folder in it, letter case aside on
// Windows and a Mac, whose disks mostly ignore it.
func inside(dir, root string) bool {
	if dir == "" || root == "" {
		return false
	}
	dir, root = filepath.Clean(dir), filepath.Clean(root)
	if runtime.GOOS != "linux" {
		dir, root = strings.ToLower(dir), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// autoPoll is how often RunAuto looks whether a backup is due, and
// autoFirst how soon after it starts: a var for tests.
var autoPoll, autoFirst = 10 * time.Minute, 2 * time.Minute

// RunAuto makes backups on their schedule until ctx ends. A failure is
// logged once and shown in Settings, not on every try.
func RunAuto(ctx context.Context) {
	t := time.NewTimer(autoFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(autoPoll)
		_, err := AutoTick(time.Now(), "magpie")
		autoMu.Lock()
		if msg := fmt.Sprint(err); err != nil && msg != autoErr {
			log.Printf("backing up by itself: %s", msg)
			autoErr = msg
		} else if err == nil {
			autoErr = ""
		}
		autoMu.Unlock()
	}
}
