package pii

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/settings"
)

// The folder the models are kept in (MetalHqa on X: on Windows both went
// onto the C: drive). Each model has a folder of its own in it,
// privacy-filter and astrlink-guard, as in magpie's cache; magpie never
// deletes the folder the user picked, only those two.
//
// Every path to a model's files goes through dir and guardRoot, which go
// through Root: a download, a load, the size check, a delete, the found
// cache. The folder is read from the settings on each use, so a magpie
// serving the gateway beside the window follows a change made in it.

const (
	pfFolder    = "privacy-filter"
	guardFolder = "astrlink-guard"
)

// Root is the folder the models are kept in: the user's (settings
// RedactModelDir), or magpie's cache.
func Root() string {
	if d := settings.Shared().RedactModelDir; d != "" {
		return d
	}
	return appdir.Cache()
}

// Custom says the user picked the folder.
func Custom() bool { return settings.Shared().RedactModelDir != "" }

// downloadable is nil when a download may start: no move is under way,
// and the folder is there.
func downloadable() error {
	if moving() {
		return errMoving
	}
	return ensureRoot()
}

// ensureRoot is for a download: a folder the user picked that isn't there
// (its disk unplugged) is no place to make again, which would fill the
// disk the drive stands for under its mount point.
func ensureRoot() error {
	if !Custom() {
		return nil
	}
	r := Root()
	if fi, err := os.Stat(r); err != nil || !fi.IsDir() {
		return fmt.Errorf("the models folder %s isn't there: connect its disk, or choose another folder", r)
	}
	return nil
}

// A move of the models to another folder, in the background, and what it
// is at.
var mv struct {
	mu          sync.Mutex
	moving      bool
	done, total int64
	err         string
	left        string
}

// Moving is the state of the move: under way, how far, why the last one
// failed, and what it couldn't delete of the old folder.
type Moving struct {
	Moving bool   `json:"moving,omitempty"`
	Done   int64  `json:"moveDone,omitempty"`
	Total  int64  `json:"moveTotal,omitempty"`
	Error  string `json:"moveError,omitempty"`
	Left   string `json:"moveLeft,omitempty"`
}

// MoveState is the move's state.
func MoveState() Moving {
	mv.mu.Lock()
	defer mv.mu.Unlock()
	return Moving{mv.moving, mv.done, mv.total, mv.err, mv.left}
}

func moving() bool {
	mv.mu.Lock()
	defer mv.mu.Unlock()
	return mv.moving
}

var errMoving = errors.New("the models are being moved to another folder; wait for it to finish")

// writable is nil when files can be made in dir.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".magpie-write-*")
	if err != nil {
		return fmt.Errorf("can't write to %s: %w", dir, err)
	}
	_, err = f.Write([]byte{0})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	os.Remove(f.Name())
	if err != nil {
		return fmt.Errorf("can't write to %s: %w", dir, err)
	}
	return nil
}

// inside says p is q or under it, by the files they are (a link, a
// differently written path) and by their names.
func inside(p, q string) bool {
	rel, err := filepath.Rel(q, p)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		if rq, err := filepath.EvalSymlinks(q); err == nil && (rp != p || rq != q) {
			return inside(rp, rq)
		}
	}
	return false
}

// MoveTo moves the models to the folder to ("" or magpie's cache: back to
// the default), in the background; MoveState says how it goes. What it
// moves is copied and checked before the setting names the new folder and
// before anything of the old is deleted, so a move that fails, in a
// folder that fills up or on a disk that goes, leaves the models where
// they were.
func MoveTo(to string) error {
	if to == "" {
		to = appdir.Cache()
	}
	if !filepath.IsAbs(to) {
		return fmt.Errorf("the folder must be a full path, not %q", to)
	}
	to = filepath.Clean(to)
	from := Root()
	fromPF, fromGuard := dir(), guardRoot()
	if sameDir(to, from) {
		return nil
	}
	// a folder inside what is being moved is deleted with it
	for _, d := range []string{fromPF, fromGuard} {
		if inside(to, d) {
			return fmt.Errorf("%s is inside the models' own folder %s", to, d)
		}
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return fmt.Errorf("can't make %s: %w", to, err)
	}
	if err := writable(to); err != nil {
		return err
	}
	mv.mu.Lock()
	if mv.moving {
		mv.mu.Unlock()
		return errMoving
	}
	mv.moving, mv.err, mv.left, mv.done, mv.total = true, "", "", 0, 0
	mv.mu.Unlock()
	go func() {
		left, err := moveModels(to, [][2]string{{fromPF, filepath.Join(to, pfFolder)}, {fromGuard, filepath.Join(to, guardFolder)}})
		mv.mu.Lock()
		mv.moving, mv.left = false, left
		if err != nil {
			mv.err = err.Error()
		}
		mv.mu.Unlock()
	}()
	return nil
}

func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// moveModels moves each pair's old folder to its new one, then saves to as
// the folder. left names what of the old folders couldn't be deleted.
func moveModels(to string, pairs [][2]string) (left string, err error) {
	// nothing is downloading into, or running from, the old folder
	Cancel()
	guardModel{}.Cancel()
	for i := 0; i < 100 && (dlBusy(&dl) || dlBusy(&gdl)); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	Stop()
	guardModel{}.Stop()
	pf.saveCache()
	guardBackend.saveCache()

	var total int64
	for _, p := range pairs {
		total += treeSize(p[0])
	}
	mv.mu.Lock()
	mv.total = total
	mv.mu.Unlock()

	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	var moved [][2]string // the pairs whose old folder is still to delete
	for _, p := range pairs {
		old, now := p[0], p[1]
		if _, serr := os.Stat(old); serr != nil {
			continue
		}
		// the same disk: one rename, nothing copied
		if _, serr := os.Lstat(now); os.IsNotExist(serr) {
			if os.Rename(old, now) == nil {
				o, n := old, now
				undo = append(undo, func() { os.Rename(n, o) })
				mv.mu.Lock()
				mv.done += treeSize(now)
				mv.mu.Unlock()
				continue
			}
		}
		made, cerr := copyVerified(old, now, &mv.done, &mv.mu)
		n := now
		undo = append(undo, func() {
			for i := len(made) - 1; i >= 0; i-- {
				os.Remove(made[i])
			}
			os.Remove(n) // empty, if it was made for this
		})
		if cerr != nil {
			rollback()
			return "", fmt.Errorf("moving to %s: %w", to, cerr)
		}
		moved = append(moved, p)
	}
	save := to
	if sameDir(to, appdir.Cache()) {
		save = ""
	}
	if serr := settings.SetRedactModelDir(save); serr != nil {
		rollback()
		return "", fmt.Errorf("saving the folder: %w", serr)
	}
	// the new folder is the one in use; the old one goes only now
	Stop()
	guardModel{}.Stop()
	var bad []string
	for _, p := range moved {
		if rerr := os.RemoveAll(p[0]); rerr != nil {
			bad = append(bad, p[0])
		}
	}
	return strings.Join(bad, ", "), nil
}

func dlBusy(d *dlState) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel != nil
}

func treeSize(root string) int64 {
	var n int64
	filepath.WalkDir(root, func(_ string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if fi, err := e.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// copyVerified copies the files under from into to, each through a
// .part file that is flushed, read back and hashed against what was read
// of the source before it takes its name. made lists the files created
// that weren't there before, for undoing a copy that stopped. A file
// already in to with the same size and hash is left as it is.
func copyVerified(from, to string, done *int64, mu *sync.Mutex) (made []string, err error) {
	err = filepath.WalkDir(from, func(p string, e fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if e.IsDir() {
			if _, serr := os.Stat(dst); os.IsNotExist(serr) {
				if merr := os.MkdirAll(dst, 0o755); merr != nil {
					return merr
				}
				made = append(made, dst)
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		_, existed := os.Stat(dst)
		if err := copyOne(p, dst, done, mu); err != nil {
			return err
		}
		if existed != nil {
			made = append(made, dst)
		}
		return nil
	})
	return made, err
}

func copyOne(src, dst string, done *int64, mu *sync.Mutex) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	part := dst + ".part-move"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), progress{in, done, mu})
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	// read back what was written, from the disk, not what was sent
	got, err := fileSHA(part)
	if err == nil && got != hex.EncodeToString(h.Sum(nil)) {
		err = fmt.Errorf("%s was copied wrong", filepath.Base(src))
	}
	if err == nil {
		if pi, serr := os.Stat(part); serr != nil || pi.Size() != fi.Size() {
			err = fmt.Errorf("%s was copied short", filepath.Base(src))
		}
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Rename(part, dst); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}

type progress struct {
	r    io.Reader
	done *int64
	mu   *sync.Mutex
}

func (p progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.mu.Lock()
	*p.done += int64(n)
	p.mu.Unlock()
	return n, err
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
