package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// coeo91 on Discord: "更新行为不对，他会在点击更新按钮后下载上一次或最近一次获取到的
// 最新版本，而不是实际的最新版本". A click that updates acts on what the feed
// says at the click, not on what the last check found: these tests run a
// check that finds 0.1.11, publish 0.1.12, and click.

// freshFeed is a fake update feed naming latest, whose asset counts its
// downloads. hold, while set, keeps the feed's answers back until it is
// closed.
type freshFeed struct {
	srv       *httptest.Server
	latest    atomic.Value
	downloads atomic.Int32
	asked     atomic.Int32 // the feed's answers
	mu        sync.Mutex
	hold      chan struct{}
}

func newFreshFeed(t *testing.T) *freshFeed {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	f := &freshFeed{}
	f.latest.Store("0.1.11")
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			f.downloads.Add(1)
			w.Write(body)
			return
		}
		f.mu.Lock()
		hold := f.hold
		f.mu.Unlock()
		if hold != nil {
			<-hold
		}
		f.asked.Add(1)
		v := f.latest.Load().(string)
		json.NewEncoder(w).Encode(update.Release{Version: v, URL: "https://example.invalid/v" + v, Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	t.Cleanup(f.srv.Close)
	t.Setenv("MAGPIE_UPDATE_FEED", f.srv.URL)
	old := Version
	Version = "0.1.10"
	t.Cleanup(func() { Version = old })
	return f
}

// freshUpdater is updates for the test: replacing the test's binary when
// exe is set, and never restarting (a handover stands in as under way).
func freshUpdater(t *testing.T, exe bool) *updater {
	t.Helper()
	u := &updater{}
	if exe {
		p, err := update.Executable()
		if err != nil {
			t.Fatal(err)
		}
		u.exe = p
		u.self, _ = os.Stat(p)
		t.Cleanup(func() { os.Remove(p + ".new") })
	}
	old := updates
	updates = u
	t.Cleanup(func() { updates = old })
	if pending.Load() == nil {
		pending.Store(&handover{})
		t.Cleanup(func() { pending.Store(nil) })
	}
	return u
}

// downloadsOnClick turns Settings' Download updates automatically off,
// in a HOME of the test's own (newFreshFeed's, or sandboxHome's).
func downloadsOnClick(t *testing.T) {
	t.Helper()
	s := settings.Load()
	s.NoAutoDownload = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// settled waits for u's check or download to end.
func settled(t *testing.T, u *updater) updateJSON {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if !u.asking() {
			if j := u.json(); j.State != "downloading" {
				return j
			}
		}
	}
	t.Fatalf("still %+v", u.json())
	return updateJSON{}
}

type openedURLs struct {
	quitCount
	mu   sync.Mutex
	urls []string
}

func (o *openedURLs) OpenURL(u string) error {
	o.mu.Lock()
	o.urls = append(o.urls, u)
	o.mu.Unlock()
	return nil
}

func postInstall(t *testing.T, w Windows) updateJSON {
	t.Helper()
	mux := http.NewServeMux()
	updateRoutes(mux, w)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/update/install", strings.NewReader("{}")))
	var j updateJSON
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &j) != nil {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	return j
}

// With Settings' Download updates automatically off, 0.1.11 found by a
// check and 0.1.12 out since: a click on Download update downloads
// 0.1.12, not the version the last check found, and Restart to update
// then goes into it with nothing downloaded again.
func TestRestartClickTakesTheNewest(t *testing.T) {
	f := newFreshFeed(t)
	downloadsOnClick(t)
	u := freshUpdater(t, true)
	u.check()
	if j := u.json(); j.State != "found" || j.Latest != "0.1.11" {
		t.Fatalf("the check: %+v", j)
	}
	f.latest.Store("0.1.12")
	postInstall(t, &quitCount{})
	if j := settled(t, u); j.State != "ready" || j.Latest != "0.1.12" || f.downloads.Load() != 1 {
		t.Fatalf("Download update: %+v, %d downloads", j, f.downloads.Load())
	}
	if j := postInstall(t, &quitCount{}); j.State != "ready" || j.Latest != "0.1.12" || f.downloads.Load() != 1 {
		t.Fatalf("the newest downloaded: %+v, %d downloads", j, f.downloads.Load())
	}
}

// A restart with nothing downloaded waiting (another magpie put the
// update in) asks the feed first; one slower than the restart's wait for
// it has the route say it is checking, and the answer, once in, is
// downloaded.
func TestRestartWaitsForTheFeed(t *testing.T) {
	f := newFreshFeed(t)
	u := freshUpdater(t, true)
	oldTicks, oldTick := recheckTicks, recheckTick
	recheckTicks, recheckTick = 3, time.Millisecond
	t.Cleanup(func() { recheckTicks, recheckTick = oldTicks, oldTick })
	u.mu.Lock()
	u.state, u.latest = "ready", &update.Release{Version: "0.1.11"}
	u.mu.Unlock()
	downloadsOnClick(t)
	f.latest.Store("0.1.12")
	hold := make(chan struct{})
	f.mu.Lock()
	f.hold = hold
	f.mu.Unlock()
	j := postInstall(t, &quitCount{})
	f.mu.Lock()
	f.hold = nil
	f.mu.Unlock()
	close(hold)
	if j.State != "checking" {
		t.Fatalf("restarted before the feed answered: %+v", j)
	}
	if j := settled(t, u); j.State != "ready" || j.Latest != "0.1.12" || f.downloads.Load() != 1 {
		t.Fatalf("the answer: %+v, %d downloads", j, f.downloads.Load())
	}
}

// A magpie that can't replace itself sends a click to the release page:
// the newest's, not the one an old check found.
func TestAvailableOpensTheNewestPage(t *testing.T) {
	f := newFreshFeed(t)
	u := freshUpdater(t, false)
	u.check()
	if j := u.json(); j.State != "available" || j.Latest != "0.1.11" {
		t.Fatalf("the check: %+v", j)
	}
	f.latest.Store("0.1.12")
	w := &openedURLs{}
	// a check a moment ago: the page it found is opened, the feed not asked
	postInstall(t, w)
	u.mu.Lock()
	u.asked = time.Now().Add(-2 * time.Minute)
	u.mu.Unlock()
	postInstall(t, w)
	w.mu.Lock()
	defer w.mu.Unlock()
	if want := []string{"https://example.invalid/v0.1.11", "https://example.invalid/v0.1.12"}; strings.Join(w.urls, " ") != strings.Join(want, " ") {
		t.Fatalf("opened %q, want %q", w.urls, want)
	}
}

// canlee886 on X: with downloads automatic, the version downloaded ahead
// of time is what Restart to update goes into, at once: magpie releases
// many times a day, so asking the feed at the click found a newer one
// nearly every time and downloaded it whole while the user waited. The
// page's click, the tray's, the restart Settings' AutoRestart waits for
// and the dialog that shows the update all leave the one waiting, with
// nothing downloaded again; the clock's next check is what takes the
// newer one (here, the new version's first).
func TestRestartTakesTheDownloadWaiting(t *testing.T) {
	busy := &fakeBusy{}
	u := freshUpdater(t, true)
	quick(t, busy, u)
	waitRecheck = (*updater).recheck
	f := newFreshFeed(t)
	u.checkByClock()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || f.downloads.Load() != 1 {
		t.Fatalf("the clock's check: %+v, %d downloads", j, f.downloads.Load())
	}
	f.latest.Store("0.1.12")
	asked := f.asked.Load()
	unchanged := func(what string) {
		t.Helper()
		if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || f.downloads.Load() != 1 || u.staged == "" {
			t.Fatalf("%s: %+v, %d downloads", what, j, f.downloads.Load())
		}
	}

	// the page's Restart to update: no wait on the feed, nothing downloaded
	if j := postInstall(t, &quitCount{}); j.State != "ready" || j.Latest != "0.1.11" {
		t.Fatalf("the page's click: %+v", j)
	}
	unchanged("the page's click")
	if n := f.asked.Load(); n != asked {
		t.Fatalf("the click asked the feed %d times", n-asked)
	}

	// the tray's, at once into the one waiting
	into := make(chan string, 4)
	restart := func() bool {
		u.mu.Lock()
		into <- u.latest.Version
		u.mu.Unlock()
		return true
	}
	u.restartAsked(restart)
	select {
	case v := <-into:
		if v != "0.1.11" {
			t.Fatalf("the tray restarted into %s", v)
		}
	default:
		t.Fatalf("the tray didn't restart at once: %+v", u.json())
	}
	unchanged("the tray's click")

	// AutoRestart's wait, once the gateway is idle
	u.waitIdle(restart, false)
	select {
	case v := <-into:
		if v != "0.1.11" {
			t.Fatalf("AutoRestart restarted into %s", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("AutoRestart never restarted: %+v", u.json())
	}
	unchanged("AutoRestart")

	// the dialog, its feed stale: asked again, the one waiting named
	u.mu.Lock()
	u.asked = time.Now().Add(-time.Hour)
	u.mu.Unlock()
	u.freshen()
	if n := f.asked.Load(); n == asked {
		t.Fatal("the dialog didn't ask the stale feed again")
	}
	unchanged("the dialog")

	// a check asked for in Settings still takes the newest
	u.check()
	if j := settled(t, u); j.State != "ready" || j.Latest != "0.1.12" || f.downloads.Load() != 2 {
		t.Fatalf("Settings' Check: %+v, %d downloads", j, f.downloads.Load())
	}
}

// elis_pina on X, on Windows: the header's Update downloaded to 100% and
// then downloaded again, over and over. The pill restarts once a click's
// download is in; that restart asked the feed, magpie had released again
// while the download came in, so it downloaded the newer one, and the
// restart after that asked again. Here each restart finds a newer release
// out: every one goes into the download the click made, and the feed is
// not asked.
func TestRestartAfterItsDownloadDoesNotAskAgain(t *testing.T) {
	f := newFreshFeed(t)
	downloadsOnClick(t)
	u := freshUpdater(t, true)
	u.check()
	u.download() // the pill's "found": a click downloads, then restarts
	if j := settled(t, u); j.State != "ready" || j.Latest != "0.1.11" || f.downloads.Load() != 1 {
		t.Fatalf("Download update: %+v, %d downloads", j, f.downloads.Load())
	}
	// released while the download came in, and again after each restart
	for i, v := range []string{"0.1.12", "0.1.13", "0.1.14"} {
		f.latest.Store(v)
		asked := f.asked.Load()
		j := postInstall(t, &quitCount{})
		if j.State != "ready" || j.Latest != "0.1.11" || f.downloads.Load() != 1 || f.asked.Load() != asked {
			t.Fatalf("restart %d, %s out: %+v, %d downloads, the feed asked %d times", i+1, v, j, f.downloads.Load(), f.asked.Load()-asked)
		}
	}
}

// sakamotokurome on X: "重启能不能跟更新分开，我本来下载一个新版本了，点击restart，
// 结果它又跑去下载了". With Settings' Download updates automatically off,
// 0.1.11 was downloaded on a click; by the time Restart to update was
// clicked 0.1.12 was out, and the click downloaded 0.1.12 whole instead of
// putting in what was on disk. A version downloaded and waiting is what
// every restart goes into, on a click's download too: the page's Restart
// to update, the tray's and the wait for an idle gateway, with the feed
// not asked and nothing downloaded again. The new version's first check
// is what finds 0.1.12.
func TestRestartPutsInTheClickedDownload(t *testing.T) {
	busy := &fakeBusy{}
	u := freshUpdater(t, true)
	quick(t, busy, u)
	waitRecheck = (*updater).recheck
	f := newFreshFeed(t)
	downloadsOnClick(t)
	u.check()
	if j := u.json(); j.State != "found" || j.Latest != "0.1.11" {
		t.Fatalf("the check: %+v", j)
	}
	u.download()
	if j := settled(t, u); j.State != "ready" || j.Latest != "0.1.11" || f.downloads.Load() != 1 {
		t.Fatalf("Download update: %+v, %d downloads", j, f.downloads.Load())
	}
	f.latest.Store("0.1.12")
	asked := f.asked.Load()
	unchanged := func(what string) {
		t.Helper()
		if j := settled(t, u); j.State != "ready" || j.Latest != "0.1.11" || f.downloads.Load() != 1 || u.staged == "" || f.asked.Load() != asked {
			t.Fatalf("%s: %+v, %d downloads, the feed asked %d times", what, j, f.downloads.Load(), f.asked.Load()-asked)
		}
	}

	// the page's Restart to update restarts at once into 0.1.11
	if j := postInstall(t, &quitCount{}); j.State != "ready" || j.Latest != "0.1.11" {
		t.Fatalf("the page's click: %+v", j)
	}
	unchanged("the page's click")

	// the tray's
	into := make(chan string, 4)
	restart := func() bool {
		u.mu.Lock()
		into <- u.latest.Version
		u.mu.Unlock()
		return true
	}
	u.restartAsked(restart)
	select {
	case v := <-into:
		if v != "0.1.11" {
			t.Fatalf("the tray restarted into %s", v)
		}
	default:
		t.Fatalf("the tray didn't restart at once: %+v", u.json())
	}
	unchanged("the tray's click")

	// a click's restart that waited for the gateway to be idle
	u.waitIdle(restart, true)
	select {
	case v := <-into:
		if v != "0.1.11" {
			t.Fatalf("the wait restarted into %s", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the wait never restarted: %+v", u.json())
	}
	unchanged("the wait")

	// the dialog that shows the update, its feed stale, names 0.1.11 still
	u.mu.Lock()
	u.asked = time.Now().Add(-time.Hour)
	u.mu.Unlock()
	u.freshen()
	asked = f.asked.Load()
	unchanged("the dialog")
}
