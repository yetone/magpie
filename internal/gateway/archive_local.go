package gateway

// The request archive on this computer (CyberCaius on Discord): while sync
// has no S3 bucket for it, a call is kept in magpie's own folder, at
// request-archive/<date>/<id>.json, the same file the bucket would get. It
// has a switch of its own, settings' RequestArchiveLocal, which sync never
// carries to another computer, so request bodies are written to a disk only
// where the user turned it on. The folder grows to RequestArchiveLocalMB
// (1 GiB unless set), and past that the oldest calls are removed first.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// ErrNotArchived is a name the archive on this computer has nothing at.
var ErrNotArchived = errors.New("not in the archive on this computer")

// LocalArchiveCap is how large the archive on this computer grows, in
// bytes: settings' RequestArchiveLocalMB, a GiB unless set.
func LocalArchiveCap() int64 {
	mb := settings.Shared().RequestArchiveLocalMB
	if mb <= 0 {
		mb = 1024
	}
	return int64(min(mb, 1<<20)) << 20
}

// DiskArchive is the archive in a folder on this computer: Put as a bucket
// is, and Open to read a call back.
type DiskArchive struct {
	Dir string
	// cap is the most it holds, in bytes; LocalArchiveCap when nil
	cap func() int64

	mu    sync.Mutex
	total int64 // the folder's size as last counted, and added to since
	known bool  // total has been counted
}

var (
	localMu       sync.Mutex
	localArchives = map[string]*DiskArchive{}
)

// LocalArchive is the archive in magpie's folder: request-archive under
// ~/.config/magpie, or a portable magpie's data folder.
func LocalArchive() *DiskArchive {
	dir := filepath.Join(settings.Dir(), "request-archive")
	localMu.Lock()
	defer localMu.Unlock()
	d := localArchives[dir]
	if d == nil {
		d = &DiskArchive{Dir: dir}
		localArchives[dir] = d
	}
	return d
}

// file is where name, "archive/<date>/<id>.json" as ArchiveName makes it,
// is kept; false for any other name.
func (d *DiskArchive) file(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "archive/")
	date, base, ok2 := strings.Cut(rest, "/")
	id, ok3 := strings.CutSuffix(base, ".json")
	if !ok || !ok2 || !ok3 {
		return "", false
	}
	if _, ok := ArchiveName(date, id); !ok {
		return "", false
	}
	return filepath.Join(d.Dir, date, id+".json"), true
}

// Put writes data at name: to a temporary file beside it first, renamed
// over it once whole, so a reader never sees half a call. Then the oldest
// calls are removed while the folder is over its size.
func (d *DiskArchive) Put(_ context.Context, name string, data []byte) error {
	path, ok := d.file(name)
	if !ok {
		return fmt.Errorf("%s is not a request archive's name", name)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.part")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	d.added(int64(len(data)), path)
	return nil
}

// Open is the call kept at name, as it is read, and its size;
// ErrNotArchived when there is none. The caller closes it.
func (d *DiskArchive) Open(_ context.Context, name string) (io.ReadCloser, int64, error) {
	path, ok := d.file(name)
	if !ok {
		return nil, 0, ErrNotArchived
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, ErrNotArchived
	}
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

type keptCall struct {
	path, rel string
	size      int64
}

// scan is every call in the folder, oldest first: "<date>/<HHMMSS>-…"
// sorts by when it was made. A temporary file left by a write that never
// finished, an hour old, is removed on the way.
func (d *DiskArchive) scan() []keptCall {
	var out []keptCall
	filepath.WalkDir(d.Dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		if strings.HasPrefix(e.Name(), ".") {
			if strings.HasSuffix(e.Name(), ".part") && time.Since(fi.ModTime()) > time.Hour {
				os.Remove(p)
			}
			return nil
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			return nil
		}
		rel, _ := filepath.Rel(d.Dir, p)
		out = append(out, keptCall{p, filepath.ToSlash(rel), fi.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// added counts n more bytes, written at kept, and past the folder's size
// removes the oldest calls until it is a tenth under it, so the folder is
// walked again only once that much more is written. The call just kept is
// never removed, though it alone be over the size.
func (d *DiskArchive) added(n int64, kept string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.known {
		d.total, d.known = 0, true
		for _, c := range d.scan() {
			d.total += c.size
		}
	} else {
		d.total += n
	}
	most := LocalArchiveCap()
	if d.cap != nil {
		most = d.cap()
	}
	if d.total <= most {
		return
	}
	calls := d.scan()
	var total int64
	for _, c := range calls {
		total += c.size
	}
	goal := most - most/10
	for _, c := range calls {
		if total <= goal {
			break
		}
		if c.path == kept {
			continue
		}
		if os.Remove(c.path) == nil {
			total -= c.size
			os.Remove(filepath.Dir(c.path)) // the day's folder, once it is empty
		}
	}
	d.total = total
}
