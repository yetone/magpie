// Package memembed is memory's search by meaning:
// paraphrase-multilingual-MiniLM-L12-v2 (sentence-transformers, Apache
// 2.0; Xenova's int8 ONNX of it), run in magpie's own process by
// internal/pii/guard, gives each memory a place in a space where texts
// that say the same are near, in any of 50 languages and across them,
// and memory.Search
// fuses how near each is to what was searched with its word ranking
// (memory.Semantic).
//
// It is off until switched on, since the model is a 135 MB download.
// Switched on, the files are fetched from Hugging Face at the revision
// their sha256 were pinned at; switched off, they are deleted. Memories
// are read in the background (Keep) and their vectors kept in the model's
// folder, keyed by the text read, so an edited memory is read again and a
// new revision starts afresh. The model is loaded when first needed and
// let go after idleAfter with none.
package memembed

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/memory"
	"github.com/yetone/magpie/internal/pii"
	"github.com/yetone/magpie/internal/pii/guard"
)

// rev is the repo's revision the files below were hashed at.
const rev = "2c4055b12046f11709e9df2c122e59ffbdc2f900"

type pinned struct {
	path string // in the repo; saved under its base name
	size int64
	sha  string
}

var files = []pinned{
	{"onnx/model_int8.onnx", 118054609, "d6ea442ff6a891daefed7c83b2f596fc5dc66bf697e4d006236f64f34bbcf4c8"},
	{"tokenizer.json", 17082913, "b60b6b43406a48bf3638526314f3d232d97058bc93472ff2de930d43686fa441"},
}

// base is where the files are fetched from; a variable for tests.
var base = "https://huggingface.co/Xenova/paraphrase-multilingual-MiniLM-L12-v2/resolve/"

// Root is where the model is kept, in magpie's cache: a folder per
// revision. A variable for tests.
var Root = func() string { return filepath.Join(appdir.Cache(), "paraphrase-multilingual-MiniLM-L12-v2") }

func dir() string { return filepath.Join(Root(), rev[:12]) }

// StatePath is the switch's file; a variable for tests.
var StatePath = func() string { return appdir.Join(appdir.Config(), "memory-semantic.json") }

// Size is the bytes the model downloads.
func Size() int64 {
	var n int64
	for _, f := range files {
		n += f.size
	}
	return n
}

// Installed says every file is on disk at its size.
func Installed() bool {
	for _, f := range files {
		fi, err := os.Stat(filepath.Join(dir(), filepath.Base(f.path)))
		if err != nil || fi.Size() != f.size {
			return false
		}
	}
	return true
}

type state struct {
	On bool `json:"on"`
}

var stateMu sync.Mutex

func load() (state, error) {
	var s state
	b, err := os.ReadFile(StatePath())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// On says search by meaning is switched on. A switch file that can't be
// read is off: the model isn't loaded on a guess.
func On() bool {
	s, err := load()
	return err == nil && s.On
}

// Status is what the Memory tab shows of it.
type Status struct {
	On          bool   `json:"on"`
	Installed   bool   `json:"installed"`
	Size        int64  `json:"size"`
	Downloading bool   `json:"downloading"`
	Done        int64  `json:"done"`
	Error       string `json:"error,omitempty"`
	// Read of Of are the memories read (with a vector), of all there are
	Read int `json:"read"`
	Of   int `json:"of"`
	// Reading is a background read going
	Reading bool `json:"reading"`
	// Failed is why the last read failed, "" when it worked
	Failed string `json:"failed,omitempty"`
}

// Now is its status.
func Now() Status {
	st := Status{On: On(), Installed: Installed(), Size: Size()}
	dl.mu.Lock()
	st.Downloading, st.Error = dl.cancel != nil, dl.err
	dl.mu.Unlock()
	st.Done = dl.done.Load()
	if st.Installed {
		st.Done = st.Size
	}
	st.Reading = reading.Load()
	idx.mu.Lock()
	st.Failed = idx.failed
	idx.mu.Unlock()
	if st.On && st.Installed {
		if all, _, err := memory.All(); err == nil {
			vs := vectors()
			for _, m := range all {
				if !searched(m) {
					continue
				}
				st.Of++
				if _, ok := vs.get(key(m)); ok {
					st.Read++
				}
			}
		}
	}
	return st
}

// Set switches it on, fetching the model when it isn't on disk, or off,
// stopping a download and deleting the model and what it read.
func Set(on bool) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	p := StatePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(state{On: on})
	if err := edit.WriteAtomic(p, b); err != nil {
		return err
	}
	if on {
		if !Installed() {
			dl.start()
		} else {
			Poke()
		}
		return nil
	}
	dl.stop()
	unload()
	store.mu.Lock()
	store.v = nil
	store.mu.Unlock()
	return os.RemoveAll(Root())
}

// ---------- the download ----------

type download struct {
	mu     sync.Mutex
	cancel context.CancelFunc // nil when none is under way
	err    string             // the last one's, "" when it worked
	done   atomic.Int64
}

var dl download

// start fetches the model in the background unless a download is under
// way, and reads the memories once it is on disk.
func (d *download) start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.err = cancel, ""
	d.done.Store(0)
	go func() {
		err := fetchModel(ctx)
		d.mu.Lock()
		d.cancel = nil
		if err != nil && ctx.Err() == nil {
			d.err = err.Error()
		}
		d.mu.Unlock()
		cancel()
		if err == nil {
			Poke()
		}
	}()
}

// stop ends the download under way.
func (d *download) stop() {
	d.mu.Lock()
	if d.cancel != nil {
		d.cancel()
	}
	d.mu.Unlock()
}

// fetchModel fetches the files not on disk yet, each checked against
// its sha256.
func fetchModel(ctx context.Context) error {
	if err := os.MkdirAll(dir(), 0o755); err != nil {
		return err
	}
	var at int64
	for _, f := range files {
		path := filepath.Join(dir(), filepath.Base(f.path))
		if fi, err := os.Stat(path); err != nil || fi.Size() != f.size {
			var n atomic.Int64
			stop := make(chan struct{})
			go func() {
				t := time.NewTicker(200 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-stop:
						return
					case <-t.C:
						dl.done.Store(at + n.Load())
					}
				}
			}()
			err := pii.Fetch(ctx, base+rev+"/"+f.path, path, f.size, f.sha, &n)
			close(stop)
			if err != nil {
				return fmt.Errorf("downloading %s: %w", filepath.Base(f.path), err)
			}
		}
		at += f.size
		dl.done.Store(at)
	}
	// the folders of revisions pinned before
	es, _ := os.ReadDir(Root())
	for _, e := range es {
		if e.IsDir() && e.Name() != filepath.Base(dir()) {
			os.RemoveAll(filepath.Join(Root(), e.Name()))
		}
	}
	return nil
}

// ---------- the model ----------

// idleAfter is how long the model stays loaded with nothing to read.
var idleAfter = 10 * time.Minute

// loadModel reads the model; a variable for tests, which give a fake.
var loadModel = func(dir string) (embedder, error) { return guard.LoadEmbedder(dir) }

type embedder interface {
	Embed(text string) []float32
}

var mod struct {
	mu   sync.Mutex // held while it loads
	e    embedder
	idle *time.Timer
}

// model is the model, loaded unless it is, kept for idleAfter more.
func model() (embedder, error) {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.e == nil {
		if !Installed() {
			return nil, errors.New("the model isn't downloaded")
		}
		e, err := loadModel(dir())
		if err != nil {
			return nil, err
		}
		mod.e = e
	}
	if mod.idle != nil {
		mod.idle.Stop()
	}
	mod.idle = time.AfterFunc(idleAfter, unload)
	return mod.e, nil
}

// unload lets the model go; a read under way finishes with the one it has.
func unload() {
	mod.mu.Lock()
	if mod.idle != nil {
		mod.idle.Stop()
		mod.idle = nil
	}
	mod.e = nil
	mod.mu.Unlock()
}

// ---------- what was read ----------

// searched says m is one a search can return, and is read.
func searched(m *memory.Memory) bool {
	return m.Status == memory.Active || m.Status == memory.Candidate
}

// passage is what is read of m: its text, and why when it says.
func passage(m *memory.Memory) string {
	if m.Why == "" {
		return m.Text
	}
	return m.Text + "\n" + m.Why
}

func key(m *memory.Memory) [32]byte { return sha256.Sum256([]byte(passage(m))) }

var reading atomic.Bool

var idx struct {
	mu     sync.Mutex // one read at a time
	failed string
}

// pokes wakes Keep for a read now.
var pokes = make(chan struct{}, 1)

// Poke asks Keep to read what is new now, not on its next tick.
func Poke() {
	select {
	case pokes <- struct{}{}:
	default:
	}
}

// Keep reads memories not read yet while ctx lasts: every tick and when
// poked, while switched on with the model on disk.
func Keep(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if On() && Installed() {
			if _, err := Index(ctx); err != nil && ctx.Err() == nil {
				idx.mu.Lock()
				idx.failed = err.Error()
				idx.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-pokes:
		}
	}
}

// Index reads every memory a search can return that has no vector for its
// text yet, keeps the vectors (dropping those of texts gone) and says how
// many it read. One goes at a time, in this magpie and across them.
func Index(ctx context.Context) (int, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	unlock, held := filelock.Lock(filepath.Join(dir(), "vectors.lock"), 0)
	if !held {
		return 0, nil
	}
	defer unlock()
	all, _, err := memory.All()
	if err != nil {
		return 0, err
	}
	vs := vectors()
	var todo []*memory.Memory
	keep := map[[32]byte]bool{}
	for _, m := range all {
		if !searched(m) {
			continue
		}
		k := key(m)
		keep[k] = true
		if _, ok := vs.get(k); !ok {
			todo = append(todo, m)
		}
	}
	if len(todo) == 0 {
		if vs.prune(keep) {
			return 0, vs.save()
		}
		idx.failed = ""
		return 0, nil
	}
	reading.Store(true)
	defer reading.Store(false)
	e, err := model()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range todo {
		if ctx.Err() != nil {
			break
		}
		vs.put(key(m), e.Embed(passage(m)))
		n++
		if n%50 == 0 {
			if err := vs.save(); err != nil {
				return n, err
			}
		}
	}
	vs.prune(keep)
	if err := vs.save(); err != nil {
		return n, err
	}
	idx.failed = ""
	return n, ctx.Err()
}

// similar is memory.Semantic: each memory of pool with a vector, and its
// cosine to text. A memory read since the last Index (a
// few at most) is read here, for a search right after it was saved.
func similar(text string, pool []*memory.Memory) map[string]float64 {
	if len(pool) == 0 || !On() || !Installed() {
		return nil
	}
	vs := vectors()
	var missing []*memory.Memory
	for _, m := range pool {
		if _, ok := vs.get(key(m)); !ok {
			missing = append(missing, m)
		}
	}
	if len(missing) == len(pool) && len(pool) > inline {
		// nothing read yet: words alone, while Keep reads them
		Poke()
		return nil
	}
	e, err := model()
	if err != nil {
		return nil
	}
	if len(missing) > inline {
		Poke()
		missing = missing[:inline]
	}
	for _, m := range missing {
		vs.put(key(m), e.Embed(passage(m)))
	}
	q := e.Embed(text)
	out := map[string]float64{}
	for _, m := range pool {
		v, ok := vs.get(key(m))
		if !ok {
			continue
		}
		var dot float64
		for i := range q {
			dot += float64(q[i]) * float64(v[i])
		}
		out[m.ID] = dot
	}
	return out
}

// inline is how many memories not read yet a search reads itself.
const inline = 16

func init() { memory.Semantic = similar }
