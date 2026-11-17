package pii

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/redact"
)

const externalPrefix = "external/"

// externalLaunch holds what can change the results of the helper.
type externalLaunch struct {
	Command         string
	Args            []string
	Dir             string
	Weights         string
	WeightsStamp    string
	WeightsRevision string
	PackageRevision string
}

func newExternalModel(id, identity string, launch externalLaunch) *externalModel {
	m := &externalModel{id: id, identity: identity, launch: launch}
	path := filepath.Join(appdir.Cache(), "external-privacy", "found-"+identity+".bin")
	m.backend = &backend{
		turn: make(chan struct{}, 1), waiting: new(atomic.Int32),
		cache: newLRU(20000), disk: new(diskState), chunk: guardChunk,
		path:     func() string { return path },
		heldBack: func() error { return m.runner.heldBack() },
	}
	m.runner.turn = m.backend.turn
	return m
}

type externalModel struct {
	id       string
	identity string
	launch   externalLaunch
	backend  *backend
	retired  atomic.Bool
	// stamped is when refreshWeights last looked at the weights (unix ns)
	stamped atomic.Int64

	mu       sync.Mutex
	name     string
	minScore float64
	runner   helperRunner
}

func (m *externalModel) ID() string           { return m.id }
func (m *externalModel) ManagesWeights() bool { return false }
func (m *externalModel) Generation() string   { return m.identity }
func (m *externalModel) Supported() bool      { return m.backend != nil }
func (m *externalModel) Size() int64          { return 0 }
func (m *externalModel) Cancel()              {}

func (m *externalModel) setMetadata(name string, minScore float64) {
	m.mu.Lock()
	m.name, m.minScore = name, minScore
	m.mu.Unlock()
}

func (m *externalModel) MinScore() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.minScore
}

func (m *externalModel) Installed() bool {
	if !m.Supported() || m.retired.Load() || m.launch.WeightsStamp == "" {
		return false
	}
	_, err := m.launch.executable()
	return err == nil
}

func (l externalLaunch) executable() (string, error) {
	if filepath.IsAbs(l.Command) {
		return exec.LookPath(l.Command)
	}
	exe, err := proc.Look(l.Command)
	if err != nil {
		return "", err
	}
	return filepath.Abs(exe)
}

// unavailable is why m can't run: its plugin is off or gone (an id
// registered by none), or its launcher or weights aren't there.
func (m *externalModel) unavailable() error {
	m.mu.Lock()
	name := m.name
	m.mu.Unlock()
	if name == "" {
		return errors.New("the plugin's local masking model is unavailable: turn its plugin on in Plugins, or pick another model in Settings → Privacy")
	}
	return fmt.Errorf("the local masking model %s is unavailable: check the path in its plugin's options (%s), and that its launcher is installed", name, m.launch.Weights)
}

func (m *externalModel) Download(func()) error { return m.unavailable() }

func (m *externalModel) Find(ctx context.Context, texts []string) ([][]redact.Found, error) {
	run := m.runner.currentRun()
	if !m.Installed() {
		return nil, m.unavailable()
	}
	return m.backend.findWith(ctx, texts, m.classifier(run))
}

func (m *externalModel) Now() Status {
	installed := m.Installed()
	m.mu.Lock()
	name := m.name
	m.mu.Unlock()
	s := Status{Model: m.id, Name: name, Supported: m.Supported(), Installed: installed}
	if !installed {
		s.Failed = m.unavailable().Error()
		return s
	}
	m.runner.mu.Lock()
	m.runner.fillStatusLocked(&s)
	m.runner.mu.Unlock()
	s.Waiting = int(m.backend.waiting.Load())
	return s
}

func (m *externalModel) Warm() {
	if m.Installed() {
		run := m.runner.currentRun()
		m.runner.warm(run, m.classifier(run))
	}
}

func (m *externalModel) Stop() { m.runner.stop() }

// The command and model files belong to the user.
func (m *externalModel) Remove() error {
	m.Stop()
	return nil
}

func (l externalLaunch) start(stop <-chan struct{}) (*helper, error) {
	exe, err := l.executable()
	if err != nil {
		return nil, err
	}
	args := append(slices.Clone(l.Args), l.Weights)
	cmd := proc.Command(exe, args...)
	cmd.Dir = l.Dir
	return startHelper(cmd, loadTimeout, "", stop)
}

func (m *externalModel) classifier(run <-chan struct{}) func([]string) ([][]redact.Found, error) {
	return func(texts []string) ([][]redact.Found, error) {
		return m.classify(run, texts)
	}
}

func (m *externalModel) classify(run <-chan struct{}, texts []string) ([][]redact.Found, error) {
	if m.retired.Load() {
		return nil, errStopped
	}
	h, err := m.runner.load(run, m.launch.start)
	if err != nil {
		return nil, err
	}
	found, err := h.classify(texts)
	if err == nil {
		for i, spans := range found {
			for _, f := range spans {
				if f.Start < 0 || f.End > len(texts[i]) || f.Start >= f.End ||
					!utf8.ValidString(texts[i][f.Start:f.End]) || !(f.Score >= 0 && f.Score <= 1) {
					err = errors.New("invalid detection span")
					break
				}
			}
		}
	}
	// An external program can include request text in its errors.
	if err != nil {
		err = errors.New("the external masking model returned an error or an invalid response")
	}
	if err := m.runner.complete(run, h, err); err != nil {
		return nil, err
	}
	// Cache writes are best effort, as with the built-in models.
	_ = os.MkdirAll(filepath.Dir(m.backend.path()), 0o700)
	return found, nil
}
