package appdir

// Moving magpie's folders to another disk (CHENGJIAHEGOXIS on X: from the
// Mac's own disk to an external SSD; buhaodeshiyehao on X: personal data
// kept on another disk). The folder magpie keeps its files in (Config) and
// the one for what it can fetch again (Cache) go into a folder named
// "magpie" in the one the user picks, as config and cache, and where each
// was is left a link to it: a symlink, or on Windows a junction when the
// user may not make symlinks (#973). Nothing else changes: every path magpie
// reads is the one it always was, and so is every path magpie has written
// into an agent's config, a skill link or an MCP server's command, all of
// which keep working through the link.
//
// A move is never made under a running magpie: the app asks for it
// (AskMove), quits, and the magpie it starts again makes it (MovePending,
// from the cli's startUp, once the one it replaces has exited and before
// anything is read or written). `magpie data move` makes it at once when no
// magpie answers at the gateway's address. Within one disk it is a rename;
// across disks a copy, and what was here is deleted only once the link is
// in place. A copy that fails (the disk full, a file unreadable) is deleted
// and everything stays where it was.
//
// Moving back (AskMove("")) copies the files home and takes the link away.
//
// The other disk unplugged is not a new magpie: a link that leads nowhere
// is Unreachable, and main stops with that error rather than start an
// empty setup in its place, which would read as everything lost and then
// be written over by whatever the user did next (#989). Connecting the
// disk again is the fix; the files never left it.
//
// A portable magpie isn't moved: its data folder is already wherever the
// user puts magpie.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// MoveName is the folder made in the one the user picks: config and cache
// go into it.
const MoveName = "magpie"

const (
	moveAsked  = "move-to.json"     // in Config: a move made at the next start
	moveResult = "move-result.json" // in Config: why the last one wasn't made
)

// Place is where magpie's folders are.
type Place struct {
	Config   string `json:"config"`             // where magpie reaches its files (~/.config/magpie)
	Cache    string `json:"cache"`              // and its cache (~/.cache/magpie)
	ConfigAt string `json:"configAt,omitempty"` // the folder Config links to, "" when it is no link
	CacheAt  string `json:"cacheAt,omitempty"`
	// Moved is the folder they were moved into (Dir(ConfigAt)), "" when
	// they are where magpie keeps them by default
	Moved    string `json:"moved,omitempty"`
	Portable bool   `json:"portable,omitempty"` // a portable magpie, never moved
	// Pending is the folder a move asked for goes into at the next start,
	// "home" for back to the default; "" when none is asked for
	Pending string `json:"pending,omitempty"`
	Error   string `json:"error,omitempty"` // why the last move wasn't made
}

// Where says where magpie's folders are.
func Where() Place {
	p := Place{Config: Config(), Cache: Cache(), Portable: Portable() != ""}
	if t, ok := linkOf(p.Config); ok {
		p.ConfigAt = t
		p.Moved = filepath.Dir(t)
	}
	if t, ok := linkOf(p.Cache); ok {
		p.CacheAt = t
	}
	if a, err := readAsked(); err == nil && a != nil {
		p.Pending = a.To
		if p.Pending == "" {
			p.Pending = "home"
		}
	}
	if b, err := os.ReadFile(filepath.Join(p.Config, moveResult)); err == nil {
		var r struct{ Error string }
		if json.Unmarshal(b, &r) == nil {
			p.Error = r.Error
		}
	}
	return p
}

type asked struct {
	To string    `json:"to"` // the folder config and cache go into; "" is home
	At time.Time `json:"at"`
}

func readAsked() (*asked, error) {
	b, err := os.ReadFile(filepath.Join(Config(), moveAsked))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a asked
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", moveAsked, err)
	}
	return &a, nil
}

// Target is the folder magpie's folders go into when the user picks dir,
// checked: an absolute folder that is there and can be written in, whose
// "magpie" folder is empty or not there yet (or is where they are now),
// and that is neither in magpie's folders nor holds them. "" is home.
func Target(dir string) (string, error) {
	if Portable() != "" {
		return "", errors.New("this magpie is portable: its files are in the data folder beside it, which moves with it")
	}
	if dir == "" {
		if _, ok := linkOf(Config()); !ok {
			if _, ok := linkOf(Cache()); !ok {
				return "", errors.New("magpie's files are in their default folder already")
			}
		}
		return "", nil
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%s is not an absolute path", dir)
	}
	dir = filepath.Clean(dir)
	fi, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", dir)
	}
	if err := canWrite(dir); err != nil {
		return "", err
	}
	root := filepath.Join(dir, MoveName)
	if cur, ok := linkOf(Config()); ok && same(filepath.Dir(cur), root) {
		return "", fmt.Errorf("magpie's files are in %s already", root)
	}
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	d := real(dir)
	for _, at := range []string{Config(), Cache()} {
		if a := real(at); same(a, d) || within(d, a) {
			return "", fmt.Errorf("%s is in magpie's folder %s", dir, at)
		}
	}
	if err := emptyOrNone(root); err != nil {
		return "", err
	}
	return root, nil
}

// canWrite says whether a file can be made in dir.
func canWrite(dir string) error {
	f, err := os.CreateTemp(dir, ".magpie-write-*")
	if err != nil {
		return fmt.Errorf("can't write in %s: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// emptyOrNone is nil when dir isn't there or is an empty folder.
func emptyOrNone(dir string) error {
	es, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(es) > 0 {
		return fmt.Errorf("%s has files in it already: pick another folder, or empty it", dir)
	}
	return nil
}

// AskMove has magpie's folders moved into the folder named "magpie" in
// dir, or home for "", when magpie next starts. It checks the folder now,
// so the user hears what is wrong with it before magpie quits.
func AskMove(dir string) (string, error) {
	root, err := Target(dir)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(asked{To: root, At: time.Now()})
	if err := os.WriteFile(filepath.Join(Config(), moveAsked), b, 0o600); err != nil {
		return "", err
	}
	os.Remove(filepath.Join(Config(), moveResult))
	return root, nil
}

// MoveAsked says whether a move is asked for, to be made at the next start.
func MoveAsked() bool {
	_, err := os.Stat(filepath.Join(Config(), moveAsked))
	return err == nil
}

// CancelMove forgets a move asked for.
func CancelMove() error {
	err := os.Remove(filepath.Join(Config(), moveAsked))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// MovePending makes the move AskMove asked for, if one was. It is made
// once: the request is taken away first, so a move that fails, or a magpie
// that dies in it, isn't tried at every start. Why it failed is kept for
// Settings to say. The caller makes sure no other magpie is running.
func MovePending() (bool, error) {
	a, err := readAsked()
	if a == nil && err == nil {
		return false, nil
	}
	os.Remove(filepath.Join(Config(), moveAsked))
	if err == nil {
		err = Move(a.To)
	}
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		os.WriteFile(filepath.Join(Config(), moveResult), b, 0o600)
		log.Printf("appdir: magpie's folders weren't moved: %v", err)
		return false, err
	}
	os.Remove(filepath.Join(Config(), moveResult))
	log.Printf("appdir: magpie's folders are in %s now", Where().Moved)
	return true, nil
}

// Move moves magpie's folders into root (root/config and root/cache), or
// home for "", now. Nothing of magpie's may be running.
func Move(root string) error {
	if Portable() != "" {
		return errors.New("this magpie is portable: its files are in the data folder beside it, which moves with it")
	}
	cfg, cache := Config(), Cache()
	prev, _ := linkOf(cfg) // the folder on the other disk, for a move home
	cfgTo, cacheTo := cfg, cache
	if root != "" {
		cfgTo, cacheTo = filepath.Join(root, "config"), filepath.Join(root, "cache")
	}
	if err := moveOne(cfg, cfgTo); err != nil {
		return err
	}
	if err := moveOne(cache, cacheTo); err != nil {
		return fmt.Errorf("magpie's files moved, but its cache didn't: %w", err)
	}
	if root == "" && prev != "" {
		// the folder they were in on the other disk, if nothing else is in it
		os.Remove(filepath.Dir(prev))
	}
	return nil
}

// moveOne moves the folder magpie reaches at at so that it is at to: to
// another disk with a link left at at, or home (to == at), the link
// replaced by the folder.
func moveOne(at, to string) error {
	cur, linked := linkOf(at)
	if !linked {
		cur = at
	}
	if same(cur, to) {
		return nil
	}
	fi, err := os.Stat(cur)
	there := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if there && !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", cur)
	}
	if linked && !there {
		return &UnreachableError{Link: at, Target: cur}
	}
	if to == at { // home: the link replaced by its folder, renamed or copied here
		if err := os.Remove(at); err != nil { // the link only
			return err
		}
		if rename(cur, at) == nil {
			return nil
		}
		if err := dirLink(cur, at); err != nil {
			return fmt.Errorf("can't link %s to %s again: %w", at, cur, err)
		}
		tmp := at + ".moving"
		os.RemoveAll(tmp) // a copy a magpie that died in this left; the folder itself is still there
		if err := copyTree(cur, tmp); err != nil {
			os.RemoveAll(tmp)
			return err
		}
		if err := os.Remove(at); err != nil { // the link only
			os.RemoveAll(tmp)
			return err
		}
		if err := os.Rename(tmp, at); err != nil {
			dirLink(cur, at)
			os.RemoveAll(tmp)
			return err
		}
		return os.RemoveAll(cur)
	}
	if err := emptyOrNone(to); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if !there { // nothing yet (no cache made): an empty folder there
		if err := os.MkdirAll(to, 0o700); err != nil {
			return err
		}
		if linked {
			os.Remove(at)
		} else {
			os.MkdirAll(filepath.Dir(at), 0o755)
		}
		return dirLink(to, at)
	}
	os.Remove(to) // an empty folder, which a rename can't go over on every system
	renamed := rename(cur, to) == nil
	if !renamed {
		if err := copyTree(cur, to); err != nil {
			os.RemoveAll(to)
			return err
		}
	}
	// what is at at now: a link to cur, or (not linked, copied) the folder
	// itself, put aside until the link is made
	aside := ""
	switch {
	case linked:
		if err := os.Remove(at); err != nil {
			undoMove(renamed, cur, to)
			return err
		}
	case !renamed:
		aside = at + ".moved"
		os.RemoveAll(aside)
		if err := os.Rename(at, aside); err != nil {
			os.RemoveAll(to)
			return err
		}
	}
	if err := dirLink(to, at); err != nil {
		switch {
		case linked:
			undoMove(renamed, cur, to)
			dirLink(cur, at)
		case renamed:
			os.Rename(to, at)
		default:
			os.Rename(aside, at)
			os.RemoveAll(to)
		}
		return fmt.Errorf("can't link %s to %s: %w", at, to, err)
	}
	switch {
	case aside != "":
		os.RemoveAll(aside)
	case linked && !renamed:
		os.RemoveAll(cur)
	}
	return nil
}

// rename moves a folder within one disk; across disks it fails and the
// folder is copied (a test has it fail to copy on one disk).
var rename = os.Rename

// undoMove puts a folder renamed from cur to to back, or deletes the copy.
func undoMove(renamed bool, cur, to string) {
	if renamed {
		os.Rename(to, cur)
	} else {
		os.RemoveAll(to)
	}
}

// copyTree copies the folder from into to, which isn't there: folders,
// files with their modes and times, links as links. Anything else (a
// socket a process listened on) is left.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(t, dst)
		case !d.Type().IsRegular():
			return nil
		}
		if err := copyFile(p, dst, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chtimes(dst, info.ModTime(), info.ModTime())
	})
}

func copyFile(from, to string, mode fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// UnreachableError is one of magpie's folders moved to a folder that isn't
// there now: the disk it is on isn't connected.
type UnreachableError struct {
	Link, Target string
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("magpie's files were moved to %s, which isn't there now: connect the disk it is on, then open magpie again (%s links to it)", e.Target, e.Link)
}

// Unreachable is an UnreachableError when magpie's folder, or its cache,
// is a link to a folder that isn't there; nil otherwise. main stops on it
// before anything is read or written: magpie started on an empty folder in
// its place would look like a setup lost.
func Unreachable() error {
	for _, at := range []string{Config(), Cache()} {
		t, ok := linkOf(at)
		if !ok {
			continue
		}
		if _, err := os.Stat(t); errors.Is(err, fs.ErrNotExist) {
			return &UnreachableError{Link: at, Target: t}
		}
	}
	return nil
}

// linkOf is the folder p links to, absolute, when p is a link (a symlink,
// or a junction on Windows, which Go reports as irregular).
func linkOf(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", false
	}
	if fi.Mode()&fs.ModeSymlink == 0 && !(runtime.GOOS == "windows" && fi.Mode()&fs.ModeIrregular != 0) {
		return "", false
	}
	t, err := os.Readlink(p)
	if err != nil {
		return "", false
	}
	if runtime.GOOS == "windows" {
		t = strings.TrimPrefix(t, `\??\`)
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(p), t)
	}
	return filepath.Clean(t), true
}

// same says whether a and b name one folder.
func same(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
