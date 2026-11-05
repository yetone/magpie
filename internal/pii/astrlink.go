package pii

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/pii/guard"
	"github.com/yetone/magpie/internal/redact"
)

// AstrLink Guard (QuantumNous/astrlink-guard, Apache-2.0) is the second
// local model: a 37 MB int8 token classifier for Chinese and English,
// run in magpie's own process by internal/pii/guard, on the CPU of every
// system and through CoreML on a Mac, with nothing native to download. Its files are fetched from
// Hugging Face at the revision their sha256 were pinned at (the
// publisher's own listing), and are loaded on the first request that
// needs them and let go after idleAfter with none.

// guardRev is the repo's revision (v0.1.3) the files below were hashed at.
var guardRev = "7216b6a3b897ef903d4b1ba367ae6fb95b0fa5e4"

type pinned struct {
	path string // in the repo; saved under its base name
	size int64
	sha  string
}

var guardFiles = []pinned{
	{"int8/model_int8.onnx", 330781, "0a070d3113615e33ba3b84d6bd486f663a377c04c4b056e7381732179f51832a"},
	{"int8/model_int8.onnx_data", 37124608, "0ba34216ceead01e0ae2dfd8e46d52b67e7aee80b04f722d9b758613a91c0067"},
	{"int8/tokenizer.json", 1502881, "0cf3b235ef125658015a6c8026a5fe6f28088bf825101a990ec18a10200feef4"},
}

var guardBase = "https://huggingface.co/QuantumNous/astrlink-guard/resolve/"

func guardURL(f pinned) string { return guardBase + guardRev + "/" + f.path }

// guardRoot is where AstrLink Guard's files are kept, in magpie's cache
// beside Privacy Filter's folder: a folder per revision, and the found
// file (cache.go).
var guardRoot = func() string { return filepath.Join(appdir.Cache(), "astrlink-guard") }

func guardDir() string { return filepath.Join(guardRoot(), guardRev[:12]) }

func guardSize() int64 {
	var n int64
	for _, f := range guardFiles {
		n += f.size
	}
	return n
}

// guardInstalled says every file is on disk at its size.
func guardInstalled() bool {
	for _, f := range guardFiles {
		fi, err := os.Stat(filepath.Join(guardDir(), filepath.Base(f.path)))
		if err != nil || fi.Size() != f.size {
			return false
		}
	}
	return true
}

var gdl dlState

// guardInstall fetches the files not on disk yet, each checked against
// its sha256; done counts the bytes of all three.
func guardInstall(ctx context.Context) error {
	if err := os.MkdirAll(guardDir(), 0o755); err != nil {
		return err
	}
	var base int64
	for _, f := range guardFiles {
		path := filepath.Join(guardDir(), filepath.Base(f.path))
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
						gdl.done.Store(base + n.Load())
					}
				}
			}()
			err := fetch(ctx, guardURL(f), path, f.size, f.sha, &n)
			close(stop)
			if err != nil {
				return fmt.Errorf("downloading %s: %w", filepath.Base(f.path), err)
			}
		}
		base += f.size
		gdl.done.Store(base)
	}
	removeOldGuard()
	return nil
}

// removeOldGuard deletes the folders of revisions pinned before.
func removeOldGuard() {
	es, _ := os.ReadDir(guardRoot())
	for _, e := range es {
		if e.IsDir() && e.Name() != filepath.Base(guardDir()) {
			os.RemoveAll(filepath.Join(guardRoot(), e.Name()))
		}
	}
}

// gsrv is AstrLink Guard, loaded, and why it failed; mu is never held
// while it loads or reads.
var gsrv struct {
	mu      sync.Mutex
	m       spanFinder
	gen     int // counts the unloads
	loadMS  int
	loading bool
	idle    *time.Timer
	failed  string
	lastErr error
	fails   int
	retryAt time.Time
}

// spanFinder is the model, loaded (*guard.Model).
type spanFinder interface {
	Find(text string) []guard.Span
}

// backender is a model that says what reads it (guard.Model.Backend).
type backender interface {
	Backend() (via, device string, err error)
}

// guardLoad reads the model; a var for tests.
var guardLoad = func(dir string) (spanFinder, error) {
	m, err := guard.Load(dir)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// guardTurn is held by each request the model reads, guard.Cores() of
// them at once. They share those cores (guard.Find takes them as it reads
// each window), so a short request goes beside a long one's chunk on a
// core of its own instead of after it, and requests at once never read
// on more cores than one did. One at a time, concurrent agent requests
// queued behind each other (yyhhyyyyyy on Discord, 10-28). Through
// CoreML they meet at its one model, which reads as fast for one
// request as for eight.
var guardTurn = make(chan struct{}, guard.Cores())

// guardLoading is held while the model loads, so requests read at once
// load it once.
var guardLoading sync.Mutex

var guardBackend = &backend{
	turn:     guardTurn,
	waiting:  new(atomic.Int32),
	cache:    newLRU(20000),
	disk:     new(diskState),
	path:     func() string { return filepath.Join(guardRoot(), "found-"+guardRev[:12]+".bin") },
	classify: guardClassify,
	heldBack: guardHeldBack,
	chunk:    guardChunk,
}

// guardClassify is what the model finds in texts, loaded if it isn't.
// The caller holds the backend's turn.
func guardClassify(texts []string) ([][]redact.Found, error) {
	out, err := guardClassifyTurn(texts)
	gsrv.mu.Lock()
	gsrv.failed = ""
	switch {
	case err == nil:
		gsrv.lastErr, gsrv.fails, gsrv.retryAt = nil, 0, time.Time{}
	case err == ErrNotInstalled:
		// not a model that failed: tried again once it is downloaded
		gsrv.failed = err.Error()
	default:
		gsrv.failed, gsrv.lastErr = err.Error(), err
		gsrv.fails++
		gsrv.retryAt = time.Now().Add(retryAfter(gsrv.fails))
	}
	gsrv.mu.Unlock()
	return out, err
}

func guardClassifyTurn(texts []string) ([][]redact.Found, error) {
	gsrv.mu.Lock()
	m, gen := gsrv.m, gsrv.gen
	gsrv.mu.Unlock()
	if m == nil {
		var err error
		if m, gen, err = guardLoaded(); err != nil {
			return nil, err
		}
	}
	out := make([][]redact.Found, len(texts))
	for i, t := range texts {
		for _, s := range m.Find(t) {
			// the score as the cache file keeps it, so a restart doesn't
			// move it over or under the threshold
			out[i] = append(out[i], redact.Found{Start: s.Start, End: s.End, Label: guardLabel(s.Label),
				Score: float64(float32(s.Score))})
		}
	}
	gsrv.mu.Lock()
	if gsrv.gen == gen && gsrv.m != nil { // not stopped meanwhile
		if gsrv.idle != nil {
			gsrv.idle.Stop()
		}
		gsrv.idle = time.AfterFunc(idleAfter, guardIdle)
	}
	gsrv.mu.Unlock()
	return out, nil
}

// guardLoaded is the model, loaded by this request unless another
// loaded it meanwhile, and the gen it was loaded in.
func guardLoaded() (spanFinder, int, error) {
	guardLoading.Lock()
	defer guardLoading.Unlock()
	gsrv.mu.Lock()
	m, gen := gsrv.m, gsrv.gen
	if m == nil {
		gsrv.loading = true
	}
	gsrv.mu.Unlock()
	if m != nil {
		return m, gen, nil
	}
	if !guardInstalled() {
		gsrv.mu.Lock()
		gsrv.loading = false
		gsrv.mu.Unlock()
		return nil, gen, ErrNotInstalled
	}
	t := time.Now()
	m, err := guardLoad(guardDir())
	gsrv.mu.Lock()
	gsrv.loading = false
	if err == nil && gsrv.gen == gen {
		gsrv.m, gsrv.loadMS = m, int(time.Since(t)/time.Millisecond)
	}
	gsrv.mu.Unlock()
	if err != nil {
		return nil, gen, fmt.Errorf("the local masking model didn't load: %w", err)
	}
	return m, gen, nil
}

// guardIdle lets the model go after idleAfter with no request, unless
// one is on it now (it sets the timer again when done): it takes every
// turn there is, and none if one is held.
func guardIdle() {
	n := 0
	defer func() {
		for range n {
			<-guardTurn
		}
	}()
	for n < cap(guardTurn) {
		select {
		case guardTurn <- struct{}{}:
			n++
		default:
			return
		}
	}
	guardUnload()
}

func guardUnload() {
	gsrv.mu.Lock()
	if gsrv.idle != nil {
		gsrv.idle.Stop()
		gsrv.idle = nil
	}
	gsrv.m = nil
	gsrv.gen++
	gsrv.mu.Unlock()
}

// guardLabels names the model's labels as Privacy Filter does, which is
// what redact's Model reads: url, ip_address and private_date are left
// out there as they are for Privacy Filter (an address the rules don't
// mask is in a request for the vendor to read).
var guardLabels = map[string]string{
	"private_person":  "private_person",
	"private_address": "private_address",
	"phone":           "private_phone",
	"email":           "private_email",
	"account":         "account_number",
	"payment_card":    "account_number",
	"common_secret":   "secret",
	"url":             "private_url",
	"ip_address":      "ip_address",
	"private_date":    "private_date",
}

func guardLabel(l string) string {
	if n, ok := guardLabels[l]; ok {
		return n
	}
	return l
}

func guardHeldBack() error {
	gsrv.mu.Lock()
	defer gsrv.mu.Unlock()
	if gsrv.lastErr == nil || !time.Now().Before(gsrv.retryAt) {
		return nil
	}
	return fmt.Errorf("%w; it is tried again in %s, and until then a request it hasn't read is held back at once. To send requests masked by the rules alone, turn the model off in Settings → Privacy",
		gsrv.lastErr, time.Until(gsrv.retryAt).Round(time.Second))
}

// guardModel is AstrLink Guard as a Model.
type guardModel struct{}

func (guardModel) ID() string { return AstrLinkGuard }

func (guardModel) Supported() bool { return true }

func (guardModel) Installed() bool { return guardInstalled() }

func (guardModel) Size() int64 { return guardSize() }

func (guardModel) MinScore() float64 { return guard.MinScore }

func (guardModel) Find(ctx context.Context, texts []string) ([][]redact.Found, error) {
	return guardBackend.find(ctx, texts)
}

func (guardModel) Now() Status {
	s := Status{Model: AstrLinkGuard, Supported: true, Installed: guardInstalled(), Size: guardSize(), Rosetta: translated()}
	gdl.fill(&s)
	gsrv.mu.Lock()
	if gsrv.m != nil {
		s.Running, s.Device, s.LoadMS = true, "CPU", gsrv.loadMS
		if b, ok := gsrv.m.(backender); ok {
			var err error
			if s.Via, s.Device, err = b.Backend(); err != nil {
				s.GPUError = err.Error()
			}
		}
	}
	s.Failed, s.Loading = gsrv.failed, gsrv.loading
	gsrv.mu.Unlock()
	s.Waiting = int(guardBackend.waiting.Load())
	return s
}

func (guardModel) Download(done func()) error { return gdl.run(guardSize(), guardInstall, done) }

func (guardModel) Cancel() { gdl.stop() }

func (guardModel) Warm() {
	if guardInstalled() {
		go func() {
			guardBackend.turn <- struct{}{}
			defer func() { <-guardBackend.turn }()
			classifySafely(guardClassify, []string{"warm"})
		}()
	}
}

// Stop lets the model go at once; a request reading it finishes with the
// model it has.
func (guardModel) Stop() {
	guardUnload()
	gsrv.mu.Lock()
	gsrv.lastErr, gsrv.fails, gsrv.retryAt = nil, 0, time.Time{}
	gsrv.mu.Unlock()
}

func (g guardModel) Remove() error {
	g.Cancel()
	g.Stop()
	gdl.mu.Lock()
	gdl.err = ""
	gdl.mu.Unlock()
	return os.RemoveAll(guardRoot())
}
