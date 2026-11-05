// Package pii runs Settings → Privacy's local model, which finds what the
// masking rules can't (internal/redact's Model): a name or an address in
// free text, a password with nothing around it to say so.
//
// It runs one of two models, the user's pick (Choose, Models):
// AstrLink Guard (astrlink.go), a 39 MB int8 token classifier for Chinese
// and English, run in this process by internal/pii/guard, the default for
// a user who never had the other; or Privacy Filter, below, which masks
// many more ordinary values in code (go.sum hashes, product names; #3578).
//
// Privacy Filter is OpenAI's (Apache-2.0), 1.6 GB as the q8 GGUF
// LocalAI made of it, run by pf-serve (native/privacy-filter), a program
// built from privacy-filter.cpp on ggml. Neither ships with magpie: both
// are downloaded when the user turns the model on, each checked against
// the sha256 pinned here, and the setting (settings.RedactModel) is saved
// only once both are. pf-serve is started on the first request that needs
// it, on the GPU where there is one (Metal, Vulkan) and else the CPU, and
// is ended after a while with none, which lets its memory go.
//
// A process of its own, not linked in: 1.7 GB the gateway would otherwise
// hold for good, and a GPU driver's crash that would end the gateway with
// it. One that dies is started again; one that dies on the GPU, doesn't
// load there in gpuLoadTimeout, or hangs there twice in a row, is started
// on the CPU until the GPU is tried again (gpuRetryAfter), with no restart
// of magpie. One that keeps failing holds requests back at once, not
// after a load each, until it is tried again (retryAfter).
package pii

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/pii/guard"
	"github.com/yetone/magpie/internal/redact"
)

// The model, at a revision of LocalAI's repo, so the file is always the
// one hashed here.
var (
	modelURL  = "https://huggingface.co/LocalAI-io/privacy-filter-GGUF/resolve/935d86882f4b5dc6e0eef05aae48ae5ba0a82a7a/privacy-filter-q8.gguf"
	modelSize = int64(1637572192)
	modelSHA  = "80efc1803eda7c095a79741d2008c07e2e0a57b01bac8825fbeb448fd097998c"
)

// pf-serve, as the privacy-filter workflow built it at serveTag: a zip for
// each system, by its sha256. A system with none can't run the model.
// oldTags are the tags pinned before, newest first: one of theirs on disk
// is run until serveTag's is downloaded (upgrade).
var (
	serveTag  = "pf-v3"
	oldTags   = []string{"pf-v2", "pf-v1"}
	serveBase = "https://github.com/yetone/magpie/releases/download/"
	serveSHA  = map[string]string{
		"darwin-amd64":  "5d9277d6852b7e3dcc30d102f43c21bf54a5c3199e0f9f998d965ec7a30ea6d7",
		"darwin-arm64":  "b45951751bb26633a5ca3827fbf3718f10dfd5c313da6b4f6ba786755353cb4b",
		"linux-amd64":   "b2b231209d8806648ec8dfe2d7b1a06c4cc273d1de4d50325e7a1638e63c7cc4",
		"linux-arm64":   "9812356fcb311c351c094aa9a587da3d54166382699b59e9f1c92ab08abae792",
		"windows-amd64": "8fd8f7295743197311d13ed34aa9fb7be891f4e2586dfda6c4a3335e6cef52f3",
	}
)

// Supported says pf-serve is built for this system.
func Supported() bool { return serveSHA[target()] != "" }

// Size is how much turning the model on downloads.
func Size() int64 { return modelSize }

// dir is where the model and pf-serve are kept: magpie's cache, since
// either can be downloaded again, and never the settings' folder, which a
// sync carries to the user's other computers.
var dir = func() string { return filepath.Join(appdir.Cache(), "privacy-filter") }

func modelPath() string { return filepath.Join(dir(), modelName()) }

// modelName is the model's file name in dir, ASCII alone: pf-serve is
// given this, not modelPath (startOne).
func modelName() string { return "privacy-filter-q8-" + modelSHA[:12] + ".gguf" }

func serveDir() string { return tagDir(serveTag) }

func tagDir(tag string) string { return filepath.Join(dir(), tag+"-"+target()) }

// tagDirs are where tag's pf-serve may be on disk: tagDir, and under
// Rosetta also the Intel one's, which magpie fetched there before it
// fetched Apple silicon's (target).
func tagDirs(tag string) []string {
	ds := []string{tagDir(tag)}
	if osArch != target() {
		ds = append(ds, filepath.Join(dir(), tag+"-"+osArch))
	}
	return ds
}

// exe is a program of pf-serve's: serveTag's, or, until that is
// downloaded, the newest of oldTags' on disk.
func exe(name string) string { return exeIn(binDir(), name) }

func exeIn(d, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(d, name)
}

func binDir() string {
	for _, tag := range append([]string{serveTag}, oldTags...) {
		for _, d := range tagDirs(tag) {
			if _, err := os.Stat(exeIn(d, "pf-serve")); err == nil {
				return d
			}
		}
	}
	return serveDir()
}

// Installed says the model and pf-serve are on disk, as downloaded and
// checked: the model the size it was hashed at.
func Installed() bool {
	fi, err := os.Stat(modelPath())
	if err != nil || fi.Size() != modelSize {
		return false
	}
	_, err = os.Stat(exe("pf-serve"))
	return err == nil
}

// ErrNotInstalled is what a request gets when the model is on in the
// settings and isn't on disk (magpie's cache was cleared).
var ErrNotInstalled = errors.New("the local masking model isn't downloaded; download it again in Settings → Privacy, or turn it off")

// Status is what Settings → Privacy shows of the model.
type Status struct {
	Model     string `json:"model"`
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Size      int64  `json:"size"`
	// a download under way, or the last one's error
	Downloading bool   `json:"downloading,omitempty"`
	Done        int64  `json:"done,omitempty"`
	Total       int64  `json:"total,omitempty"`
	Error       string `json:"error,omitempty"`
	// pf-serve, while it runs: where the model is, and how long it took
	// to start
	Running bool   `json:"running,omitempty"`
	Device  string `json:"device,omitempty"`
	// Via is the runtime AstrLink Guard runs on, "CoreML" on a Mac where
	// it does (Device then the one its plan puts most of the model on, or
	// ""); "" for magpie's own Go code on the CPU
	Via      string `json:"via,omitempty"`
	GPU      bool   `json:"gpu,omitempty"`
	LoadMS   int    `json:"loadMs,omitempty"`
	GPUError string `json:"gpuError,omitempty"`
	// why the last request got no answer
	Failed string `json:"failed,omitempty"`
	// pf-serve is loading the model, and how many requests wait for it
	// (or for the one it is on)
	Loading bool `json:"loading,omitempty"`
	Waiting int  `json:"waiting,omitempty"`
	// Rosetta: AstrLink Guard runs in an Intel magpie under Rosetta,
	// in Go and translated, several times slower than in Apple silicon's
	// build, which has CoreML too
	Rosetta bool `json:"rosetta,omitempty"`
}

// Now is the model's status.
func Now() Status {
	s := Status{Model: PrivacyFilter, Supported: Supported(), Installed: Installed(), Size: modelSize}
	dl.fill(&s)
	srv.mu.Lock()
	if srv.h != nil {
		s.Running, s.Device, s.GPU, s.LoadMS, s.GPUError = true, srv.h.hello.Device, srv.h.hello.GPU, srv.h.hello.MS, srv.h.hello.GPUError
	}
	s.Failed, s.Loading = srv.failed, srv.loading
	if s.GPUError == "" {
		s.GPUError = srv.gpuLost
	}
	srv.mu.Unlock()
	s.Waiting = int(waiting.Load())
	return s
}

// Remove ends pf-serve and deletes the model and pf-serve.
func Remove() error {
	Cancel()
	Stop()
	dl.mu.Lock()
	dl.err = ""
	dl.mu.Unlock()
	return os.RemoveAll(dir())
}

// The local models Settings → Privacy offers, by the id the settings keep
// (settings.RedactModelName).
const (
	PrivacyFilter = "privacy-filter"
	AstrLinkGuard = "astrlink-guard"
)

// Model is one local model.
type Model interface {
	ID() string
	Supported() bool
	Installed() bool
	// Size is how much turning it on downloads.
	Size() int64
	// MinScore is its default threshold: masking drops what it is less
	// sure of, unless the user set another.
	MinScore() float64
	Find(ctx context.Context, texts []string) ([][]redact.Found, error)
	Now() Status
	// Download fetches it in the background and calls done once it is on
	// disk; Cancel stops that.
	Download(done func()) error
	Cancel()
	// Warm loads it in the background, where it is on disk.
	Warm()
	// Stop lets it go; Remove stops it and deletes it.
	Stop()
	Remove() error
}

// Models are the local models, Privacy Filter first.
func Models() []Model { return []Model{pfModel{}, guardModel{}} }

// Floor is the lowest threshold a user can set (settings.RedactModelScore):
// what either model sends is at least this sure. pf-serve's default
// threshold is 0.5, and AstrLink Guard's Find keeps what is (guard.Floor).
const Floor = guard.Floor

// Threshold is model m's threshold of those the user set (by model id,
// settings.RedactModelScore), or its own where none is, or one is out of
// [Floor, 1).
func Threshold(set map[string]float64, m Model) float64 {
	if v, ok := set[m.ID()]; ok && v >= Floor && v < 1 {
		return v
	}
	return m.MinScore()
}

// Of is the model id names; AstrLink Guard's for an id there's none of.
func Of(id string) Model {
	if id == PrivacyFilter {
		return pfModel{}
	}
	return guardModel{}
}

// Choose is the model masking uses: the one the user picked (name);
// with none picked, AstrLink Guard, a 38 MB download that runs on every
// CPU and, measured on code (docs/subsystems), masks far fewer ordinary
// values. Only while the model is on (on) and Privacy Filter is on disk
// but AstrLink Guard isn't is it Privacy Filter, so a user who had it on
// before there was a choice isn't held back while AstrLink Guard
// downloads (masking.picked starts that download).
func Choose(name string, on bool) string {
	switch name {
	case PrivacyFilter, AstrLinkGuard:
		return name
	}
	if on && !guardInstalled() && Installed() {
		return PrivacyFilter
	}
	return AstrLinkGuard
}

// pfModel is Privacy Filter as a Model.
type pfModel struct{}

func (pfModel) ID() string                 { return PrivacyFilter }
func (pfModel) Supported() bool            { return Supported() }
func (pfModel) Installed() bool            { return Installed() }
func (pfModel) Size() int64                { return Size() }
func (pfModel) MinScore() float64          { return 0.5 }
func (pfModel) Now() Status                { return Now() }
func (pfModel) Download(done func()) error { return Download(done) }
func (pfModel) Cancel()                    { Cancel() }
func (pfModel) Warm()                      { Warm() }
func (pfModel) Stop()                      { Stop() }
func (pfModel) Remove() error              { return Remove() }
func (pfModel) Find(ctx context.Context, texts []string) ([][]redact.Found, error) {
	return FindContext(ctx, texts)
}

// idleAfter is how long pf-serve runs with no request before it is ended.
var idleAfter = 10 * time.Minute
