package plugin

// Bun kept up to date, as the plugins are: every six hours magpie asks
// GitHub for Bun's newest release and, once it has been out two days (a
// release with a bad bug is usually followed by a fix within them),
// downloads it, checks its checksum, tries it and switches the plugin
// host to it, a reply streaming through the old host finishing first.
// BunVersion is the floor: magpie never runs an older Bun. The Bun it ran
// before stays on disk, and a new one the host won't start on is set
// aside for the one before, never tried again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/update"
)

// bunSettle is how long a Bun release is out before magpie takes it.
const bunSettle = 48 * time.Hour

// bunState is what magpie keeps about the Bun it runs, in bun/state.json.
type bunState struct {
	// Current is the Bun the host runs on, "" for BunVersion.
	Current string `json:"current,omitempty"`
	// Previous is the one it ran on before, kept to fall back on.
	Previous string `json:"previous,omitempty"`
	// Bad are versions that failed their try or the host's start.
	Bad     []string  `json:"bad,omitempty"`
	Checked time.Time `json:"checked,omitzero"`
}

func bunRoot() string { return filepath.Join(filepath.Dir(catalog.CachePath()), "bun") }

func bunDirOf(v string) string { return filepath.Join(bunRoot(), v) }

// bunExeOf is where magpie keeps Bun v: named magpie-bun, so a proxy app's
// PROCESS-NAME rule can tell the requests plugins make (and their installs)
// from any other Bun's (#1048).
func bunExeOf(v string) string { return filepath.Join(bunDirOf(v), magpieBun()) }

func magpieBun() string { return "magpie-" + bunExe() }

func bunStatePath() string { return filepath.Join(bunRoot(), "state.json") }

func readBunState() bunState {
	var s bunState
	if b, err := steady.ReadFile(bunStatePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func writeBunState(s bunState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bunRoot(), 0o755); err != nil {
		return err
	}
	return writeWhole(bunStatePath(), b)
}

func haveBun(v string) bool {
	exe := bunExeOf(v)
	if _, err := os.Stat(exe); err == nil {
		return true
	}
	// a Bun downloaded before it was named magpie-bun takes the name
	_ = os.Rename(filepath.Join(bunDirOf(v), bunExe()), exe)
	_, err := os.Stat(exe)
	return err == nil
}

// usable is whether v may be run: no older than the floor, not set aside.
func (s bunState) usable(v string) bool {
	return v != "" && !update.Newer(BunVersion, v) && !slices.Contains(s.Bad, v)
}

// BunInUse is the version of the Bun plugins run on (or will, once it is
// downloaded): the newest magpie took, else BunVersion.
func BunInUse() string {
	if b := os.Getenv("MAGPIE_BUN"); b != "" {
		bunMu.Lock()
		defer bunMu.Unlock()
		if _, ok := envBunVersion[b]; !ok {
			envBunVersion[b] = bunReported(b)
		}
		return envBunVersion[b]
	}
	bunMu.Lock()
	defer bunMu.Unlock()
	return inUseLocked()
}

func inUseLocked() string {
	s := readBunState()
	if s.usable(s.Current) && haveBun(s.Current) {
		return s.Current
	}
	return BunVersion
}

// envBunVersion is what each $MAGPIE_BUN said it is, asked once.
var envBunVersion = map[string]string{}

var bunVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// bunReported is the version a bun says it is, "" when it won't say.
func bunReported(exe string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := command(ctx, exe, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// bunLatest is Bun's newest release and when it was published (tests stub it).
var bunLatest = func(ctx context.Context) (string, time.Time, error) {
	b, err := getURL(ctx, "https://api.github.com/repos/oven-sh/bun/releases/latest", 4<<20)
	if err != nil {
		return "", time.Time{}, err
	}
	var r struct {
		Tag       string    `json:"tag_name"`
		Published time.Time `json:"published_at"`
		Draft     bool      `json:"draft"`
		Pre       bool      `json:"prerelease"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", time.Time{}, err
	}
	v := strings.TrimPrefix(r.Tag, "bun-v")
	if r.Draft || r.Pre || !bunVersionRe.MatchString(v) {
		return "", time.Time{}, fmt.Errorf("Bun's latest release is %q", r.Tag)
	}
	return v, r.Published, nil
}

// tryBun runs a downloaded Bun on what the host needs of it: it says its
// version, and runs a script with fetch, AsyncLocalStorage and the file
// system (tests stub it).
var tryBun = func(ctx context.Context, exe, v string) error {
	if got := bunReported(exe); got != v {
		return fmt.Errorf("it says it is %q", got)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	script := `const { AsyncLocalStorage } = require("node:async_hooks"); const fs = require("node:fs"); const s = new AsyncLocalStorage();` +
		`s.run(1, async () => { await Promise.resolve(); if (s.getStore() !== 1 || typeof fetch !== "function" || !fs.existsSync(process.execPath)) process.exit(3); console.log("ok") })`
	out, err := command(ctx, exe, "-e", script).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("it printed %q", strings.TrimSpace(string(out)))
	}
	return nil
}

// CheckBun takes Bun's newest release when it is newer than the one in
// use, out two days and not set aside: downloaded, checked, tried, then
// switched to, the host restarted gracefully. It says the version taken,
// "" when none was.
func CheckBun(ctx context.Context) (string, error) {
	if os.Getenv("MAGPIE_BUN") != "" {
		return "", nil
	}
	latest, published, err := bunLatest(ctx)
	if err != nil {
		return "", err
	}
	bunMu.Lock()
	s := readBunState()
	s.Checked = time.Now().UTC().Truncate(time.Second)
	use := inUseLocked()
	if !update.Newer(latest, use) || !s.usable(latest) || time.Since(published) < bunSettle {
		err := writeBunState(s)
		bunMu.Unlock()
		return "", err
	}
	bunMu.Unlock()

	exe := bunExeOf(latest)
	if !haveBun(latest) {
		if err := downloadBun(ctx, latest, exe); err != nil {
			return "", fmt.Errorf("downloading Bun %s: %w", latest, err)
		}
	}
	if err := tryBun(ctx, exe, latest); err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		setAside(latest, fmt.Sprintf("its try failed: %s", err))
		return "", fmt.Errorf("Bun %s: %w", latest, err)
	}
	bunMu.Lock()
	s = readBunState()
	if use != BunVersion || haveBun(BunVersion) {
		s.Previous = use
	}
	s.Current = latest
	s.Checked = time.Now().UTC().Truncate(time.Second)
	err = writeBunState(s)
	if err == nil {
		pruneBuns(s)
	}
	bunMu.Unlock()
	if err != nil {
		return "", err
	}
	log.Printf("plugins: Bun %s → %s", use, latest)
	if Running() {
		Restart()
	}
	return latest, nil
}

// setAside marks v as not to be run again and goes back to the Bun before
// it, removing it from disk.
func setAside(v, why string) {
	log.Printf("plugins: Bun %s set aside, %s", v, why)
	bunMu.Lock()
	defer bunMu.Unlock()
	s := readBunState()
	if !slices.Contains(s.Bad, v) {
		s.Bad = append(s.Bad, v)
	}
	if s.Current == v {
		s.Current, s.Previous = s.Previous, ""
	}
	_ = writeBunState(s)
	_ = os.RemoveAll(bunDirOf(v))
}

// fallBack is the Bun to start the host on when it wouldn't start on the
// one in use: the one before, when there is one to go back to.
func fallBack() (string, bool) {
	bunMu.Lock()
	defer bunMu.Unlock()
	s := readBunState()
	if s.Current == "" || !s.usable(s.Current) {
		return "", false
	}
	prev := s.Previous
	if prev == "" {
		prev = BunVersion
	}
	if prev == s.Current || !s.usable(prev) || !haveBun(prev) {
		return "", false
	}
	return bunExeOf(prev), true
}

// pruneBuns removes the Buns neither in use nor kept to fall back on.
func pruneBuns(s bunState) {
	keep := map[string]bool{s.Current: true, s.Previous: true}
	if s.Previous == "" {
		keep[BunVersion] = true
	}
	es, _ := os.ReadDir(bunRoot())
	for _, e := range es {
		if e.IsDir() && bunVersionRe.MatchString(e.Name()) && !keep[e.Name()] {
			_ = os.RemoveAll(filepath.Join(bunRoot(), e.Name()))
		}
	}
}

// KeepBunUpdated looks for a newer Bun a little after magpie starts, then
// every bunEvery, while there are plugins to run on it.
func KeepBunUpdated(ctx context.Context) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if len(Load().Plugins) > 0 && HasBun() {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			if _, err := CheckBun(cctx); err != nil {
				log.Printf("plugins: looking for a newer Bun: %s", err)
			}
			cancel()
		}
		t.Reset(bunEvery)
	}
}
