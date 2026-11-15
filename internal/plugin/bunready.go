package plugin

// Bun while it is being downloaded (#6735). magpie downloads its Bun the
// first time a plugin is asked for, and a download on a slow or blocked
// link can take minutes, or stall a minute at a time and go on. A request
// for a plugin's provider used to wait on it, holding the host's lock, so
// every other request waited behind it: zero bytes, nothing in the log,
// and a routing group never went on to its next member.
//
// The download now runs in the background, once at a time for each Bun.
// The host waits for it a short while (bunReadyWait from when it began),
// then each call for the host ends with a BunNotReadyError that says how
// far it is, or why it failed, and the gateway goes on to the next member.
// A failed download is said at once for bunRetryGap, then tried again by
// the next call. Installs (Bun) wait for it as long as their context lets.
//
// When the download fails, a Bun on PATH is taken if it is one magpie
// would run itself: no older than BunVersion, not set aside, passing the
// try a new release gets. magpie's checksummed build stays the one used
// whenever it can be had; the user's own Bun is better than no plugins.

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// BunNotReadyError is a call for the plugin host while the Bun it runs on
// is still downloading, or after its download failed.
type BunNotReadyError struct {
	Version string
	// For is how long the download has gone on, while it goes on.
	For time.Duration
	// Err is why the download failed; nil while it goes on.
	Err error
	// ctx is the caller's context's error, when it ended the wait.
	ctx error
}

func (e *BunNotReadyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("Bun runtime not ready: downloading Bun %s failed: %v", e.Version, e.Err)
	}
	d := e.For.Round(time.Second)
	if d == 0 {
		d = e.For.Round(100 * time.Millisecond)
	}
	return fmt.Sprintf("Bun runtime not ready: downloading Bun %s (%s so far); plugins run once it is done", e.Version, d)
}

func (e *BunNotReadyError) Unwrap() []error {
	var out []error
	for _, err := range []error{e.Err, e.ctx} {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}

var (
	// bunReadyWait is how long the host waits on a download from when it
	// began before calls for it are told Bun isn't ready. A var for tests.
	bunReadyWait = 10 * time.Second
	// bunRetryGap is how long a failed download is said to the host's
	// callers before one of them tries it again.
	bunRetryGap = time.Minute
	// bunSayGap throttles the log line of calls told Bun isn't ready.
	bunSayGap = 30 * time.Second
)

// pathBuns are the Buns on this machine to fall back on: the one on PATH,
// and the one Bun's installer puts in ~/.bun/bin, which an app started
// from the Dock or Finder doesn't have on PATH. A var for tests.
var pathBuns = func() []string {
	var out []string
	if p, err := exec.LookPath(bunExe()); err == nil {
		out = append(out, p)
	}
	if h, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(h, ".bun", "bin", bunExe()))
	}
	return out
}

// bunFetch is one download of a Bun, run in the background.
type bunFetch struct {
	v, exe  string
	started time.Time
	cancel  context.CancelFunc
	done    chan struct{}
	// set before done is closed
	got   string // the Bun the fetch gave
	err   error
	ended time.Time
}

var bunFetches struct {
	sync.Mutex
	last  map[string]*bunFetch // by where the Bun goes
	going map[*bunFetch]bool
	wg    sync.WaitGroup
	said  time.Time
}

// bunAtHand is the Bun plugins run on and whether it is on disk.
func bunAtHand() (v, exe string, ok bool) {
	bunMu.Lock()
	defer bunMu.Unlock()
	v = inUseLocked()
	exe = bunExeOf(v)
	return v, exe, haveBun(v)
}

// fetchBun is the download of Bun v to exe: the one going, else the one
// that failed less than bunRetryGap ago unless again, else a new one.
func fetchBun(v, exe string, again bool) *bunFetch {
	bunFetches.Lock()
	defer bunFetches.Unlock()
	if f := bunFetches.last[exe]; f != nil {
		select {
		case <-f.done:
			if f.err != nil && !again && time.Since(f.ended) < bunRetryGap {
				return f
			}
		default:
			return f
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &bunFetch{v: v, exe: exe, started: time.Now(), cancel: cancel, done: make(chan struct{})}
	if bunFetches.last == nil {
		bunFetches.last = map[string]*bunFetch{}
	}
	if bunFetches.going == nil {
		bunFetches.going = map[*bunFetch]bool{}
	}
	bunFetches.last[exe] = f
	bunFetches.going[f] = true
	bunFetches.wg.Add(1)
	go f.run(ctx)
	return f
}

func (f *bunFetch) run(ctx context.Context) {
	defer bunFetches.wg.Done()
	defer f.cancel()
	log.Printf("plugins: Bun runtime not ready: downloading Bun %s to %s", f.v, f.exe)
	got, err := f.exe, downloadBun(ctx, f.v, f.exe)
	if err != nil && ctx.Err() == nil {
		log.Printf("plugins: downloading Bun %s failed after %s: %v", f.v, time.Since(f.started).Round(time.Second), err)
		if p, ok := bunFromPath(ctx); ok {
			got, err = p, nil
		}
	} else if err == nil {
		log.Printf("plugins: Bun %s downloaded in %s", f.v, time.Since(f.started).Round(time.Second))
	}
	bunFetches.Lock()
	delete(bunFetches.going, f)
	bunFetches.Unlock()
	f.got, f.err, f.ended = got, err, time.Now()
	close(f.done)
}

// notReady is the error a caller is given while f goes on or after it
// failed; cause is the caller's context's error, if it ended the wait.
func (f *bunFetch) notReady(cause error) *BunNotReadyError {
	e := &BunNotReadyError{Version: f.v, ctx: cause}
	select {
	case <-f.done:
		e.Err = f.err
	default:
		e.For = time.Since(f.started)
	}
	return e
}

// hostBun is the Bun to start the host on. While it is downloading, the
// wait is bounded by ctx and by bunReadyWait from when the download began;
// past them the caller is told Bun isn't ready, and the download goes on.
func hostBun(ctx context.Context) (string, error) {
	if b := os.Getenv("MAGPIE_BUN"); b != "" {
		return b, nil
	}
	v, exe, ok := bunAtHand()
	if ok {
		return exe, nil
	}
	f := fetchBun(v, exe, false)
	wait := time.NewTimer(time.Until(f.started.Add(bunReadyWait)))
	defer wait.Stop()
	select {
	case <-f.done:
		if f.err == nil {
			return f.got, nil
		}
	case <-ctx.Done():
	case <-wait.C:
	}
	e := f.notReady(ctx.Err())
	bunFetches.Lock()
	say := time.Since(bunFetches.said) >= bunSayGap
	if say {
		bunFetches.said = time.Now()
	}
	bunFetches.Unlock()
	if say {
		log.Printf("plugins: a call for the plugin host ended: %v", e)
	}
	return "", e
}

// bunNotReadyNow is why a caller who stopped waiting for the host (cause)
// got no host, when that is Bun's download: nil when Bun is at hand.
func bunNotReadyNow(cause error) error {
	if os.Getenv("MAGPIE_BUN") != "" {
		return nil
	}
	_, exe, ok := bunAtHand()
	if ok {
		return nil
	}
	bunFetches.Lock()
	f := bunFetches.last[exe]
	bunFetches.Unlock()
	if f == nil {
		return nil
	}
	return f.notReady(cause)
}

// bunFromPath is a Bun on this machine taken when magpie's own download
// failed, copied in where magpie keeps that version, as magpie-bun
// (#1048), and made the one in use when it is newer than BunVersion.
func bunFromPath(ctx context.Context) (string, bool) {
	seen := map[string]bool{}
	for _, p := range pathBuns() {
		if real, err := filepath.EvalSymlinks(p); err != nil || seen[real] {
			continue
		} else {
			seen[real] = true
		}
		rv := bunReported(p)
		bunMu.Lock()
		ok := bunVersionRe.MatchString(rv) && readBunState().usable(rv)
		bunMu.Unlock()
		if !ok {
			log.Printf("plugins: the Bun at %s (%q) isn't one magpie runs: at least %s, not set aside", p, rv, BunVersion)
			continue
		}
		if err := tryBun(ctx, p, rv); err != nil {
			log.Printf("plugins: the Bun at %s failed its try: %v", p, err)
			continue
		}
		exe := bunExeOf(rv)
		if err := copyBun(p, exe); err != nil {
			log.Printf("plugins: taking the Bun at %s: %v", p, err)
			continue
		}
		bunMu.Lock()
		s := readBunState()
		var err error
		if rv != BunVersion && s.Current != rv {
			s.Current = rv
			err = writeBunState(s)
		}
		bunMu.Unlock()
		if err != nil {
			log.Printf("plugins: taking the Bun at %s: %v", p, err)
			continue
		}
		log.Printf("plugins: running plugins on Bun %s from %s", rv, p)
		return exe, true
	}
	return "", false
}

func copyBun(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	tmp := to + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, to)
}

// endBunFetches stops the downloads going and forgets the ones done: for
// Settle, as a download writes in the test's folder.
func endBunFetches() {
	bunFetches.Lock()
	for f := range bunFetches.going {
		f.cancel()
	}
	bunFetches.last = nil
	bunFetches.Unlock()
	bunFetches.wg.Wait()
}

// BunReleaseForTest has Bun downloaded from url, the host waiting wait
// for it, and no Bun on PATH taken, until restore.
func BunReleaseForTest(url string, wait time.Duration) (restore func()) {
	oldURL, oldWait, oldPath := bunRelease, bunReadyWait, pathBuns
	bunRelease, bunReadyWait = url, wait
	pathBuns = func() []string { return nil }
	return func() {
		endBunFetches()
		bunRelease, bunReadyWait, pathBuns = oldURL, oldWait, oldPath
	}
}
