package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// fakeBusy stands in for the gateway's Busy in these tests.
type fakeBusy struct {
	mu sync.Mutex
	b  gateway.Busy
}

func (f *fakeBusy) set(b gateway.Busy) {
	f.mu.Lock()
	f.b = b
	f.mu.Unlock()
}

func (f *fakeBusy) get() gateway.Busy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.b
}

// quick has u's waits run in milliseconds, against busy, with no feed asked.
func quick(t *testing.T, busy *fakeBusy, u *updater) {
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1/") // never the real one
	oldBusy, oldSettle, oldMost, oldTick, oldRecheck := gatewayBusy, idleSettle, waitMost, waitTick, waitRecheck
	gatewayBusy, idleSettle, waitMost, waitTick = busy.get, 80*time.Millisecond, time.Minute, 5*time.Millisecond
	waitRecheck = func(*updater, bool) {}
	oldAt, oldOpen := usedAt.Swap(0), usedOpen.Swap(false) // no window used
	t.Cleanup(func() {
		u.cancelWait()
		u.loops.Wait()
		gatewayBusy, idleSettle, waitMost, waitTick, waitRecheck = oldBusy, oldSettle, oldMost, oldTick, oldRecheck
		usedAt.Store(oldAt)
		usedOpen.Store(oldOpen)
	})
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal(what)
}

type quitCount struct {
	Windows
	quits atomic.Int32
}

func (q *quitCount) Quit()           { q.quits.Add(1) }
func (q *quitCount) MainShown() bool { return false }

// #577: Restart to update with the gateway streaming a reply, or a Claude
// Code turn waiting on its tool results, doesn't restart: it says what is in
// flight and waits; Cancel calls the wait off.
func TestRestartToUpdateWaitsForTheGateway(t *testing.T) {
	busy := &fakeBusy{}
	busy.set(gateway.Busy{Requests: 2, Tools: 1, Last: time.Now()})
	old := updates
	// nothing staged: were it to restart, there would be nothing to put in
	updates = &updater{state: "ready", latest: &update.Release{Version: "0.1.999"}}
	t.Cleanup(func() { updates = old })
	quick(t, busy, updates)
	w := &quitCount{}
	mux := http.NewServeMux()
	updateRoutes(mux, w)
	post := func(body string) updateJSON {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/update/install", strings.NewReader(body)))
		var j updateJSON
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &j) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return j
	}
	j := post(`{}`)
	if !j.Waiting || j.Busy == nil || j.Busy.Requests != 2 || j.Busy.Tools != 1 || j.State != "ready" {
		t.Fatalf("restart with the gateway busy: %+v", j)
	}
	time.Sleep(50 * time.Millisecond)
	if w.quits.Load() != 0 {
		t.Fatal("quit with requests in flight")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/update", nil))
	if !strings.Contains(rec.Body.String(), `"waiting":true`) || !strings.Contains(rec.Body.String(), `"busy":{"requests":2,"tools":1}`) {
		t.Fatalf("the state: %s", rec.Body)
	}
	if j := post(`{"when":"cancel"}`); j.Waiting {
		t.Fatalf("cancelled: %+v", j)
	}
	if w.quits.Load() != 0 {
		t.Fatal("quit")
	}
}

// The wait ends in the restart once nothing is in flight, no turn waits on
// tools and the last request ended idleSettle ago — not in the gap of a
// tool chain, between one request and the next.
func TestRestartWhenIdle(t *testing.T) {
	busy := &fakeBusy{}
	busy.set(gateway.Busy{Requests: 1})
	u := &updater{state: "ready", latest: &update.Release{Version: "0.1.999"}}
	quick(t, busy, u)
	var told atomic.Int32
	u.onWait = func() { told.Add(1) }
	var restarts atomic.Int32
	u.waitIdle(func() bool { restarts.Add(1); return true }, true)
	time.Sleep(40 * time.Millisecond)
	if restarts.Load() != 0 {
		t.Fatal("restarted while streaming")
	}
	// the reply ended asking for a tool, whose result Claude Code waits on
	busy.set(gateway.Busy{Tools: 1, Last: time.Now()})
	time.Sleep(40 * time.Millisecond)
	if restarts.Load() != 0 {
		t.Fatal("restarted with a turn waiting on its tool results")
	}
	// the result came, the turn's last reply ended just now
	busy.set(gateway.Busy{Last: time.Now()})
	time.Sleep(30 * time.Millisecond)
	if restarts.Load() != 0 {
		t.Fatal("restarted in a tool chain's gap")
	}
	eventually(t, "never restarted once idle", func() bool { return restarts.Load() == 1 })
	if waiting, _ := u.waitingFor(); waiting {
		t.Fatal("still waiting")
	}
	eventually(t, "the tray was not told", func() bool { return told.Load() >= 2 })
	time.Sleep(30 * time.Millisecond)
	if restarts.Load() != 1 {
		t.Fatalf("%d restarts", restarts.Load())
	}

	// a wait that runs out leaves the update for a later restart or quit
	u.loops.Wait()
	waitMost = 40 * time.Millisecond
	busy.set(gateway.Busy{Requests: 1})
	u.waitIdle(func() bool { restarts.Add(1); return true }, true)
	eventually(t, "never gave up", func() bool { return u.json().GaveUp })
	if j := u.json(); j.Waiting || restarts.Load() != 1 || j.Busy == nil {
		t.Fatalf("gave up: %+v, %d restarts", j, restarts.Load())
	}
}

// Versions out while a restart waits collapse into the newest: the restart
// is into the last one, downloaded once the gateway is idle, not into each,
// for a click's restart with downloads on a click (with them automatic the
// one waiting is restarted into: TestRestartTakesTheDownloadWaiting).
func TestRestartWhenIdleIntoTheNewest(t *testing.T) {
	sandboxHome(t)
	busy := &fakeBusy{}
	u := &updater{}
	quick(t, busy, u)
	waitRecheck = (*updater).recheck
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var latest atomic.Value
	latest.Store("0.1.11")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			w.Write(body)
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: latest.Load().(string), Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	old := Version
	Version = "0.1.10"
	defer func() { Version = old }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u.exe = exe
	u.self, _ = os.Stat(exe)
	defer os.Remove(exe + ".new")
	u.check()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" {
		t.Fatalf("%+v", j)
	}
	downloadsOnClick(t)
	busy.set(gateway.Busy{Requests: 1})
	into := make(chan string, 4)
	u.waitIdle(func() bool {
		u.mu.Lock()
		into <- u.latest.Version
		u.mu.Unlock()
		return true
	}, true)
	latest.Store("0.1.12")
	time.Sleep(30 * time.Millisecond)
	latest.Store("0.1.13")
	busy.set(gateway.Busy{})
	select {
	case v := <-into:
		if v != "0.1.13" {
			t.Fatalf("restarted into %s", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("never restarted: %+v", u.json())
	}
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.13" {
		t.Fatalf("%+v", j)
	}
}

// The tray's item, while a restart waits, restarts at once and says what
// that cuts short.
func TestTrayRestartNow(t *testing.T) {
	for _, c := range []struct {
		lang string
		b    gateway.Busy
		want string
	}{
		{"en", gateway.Busy{Requests: 2, Tools: 1}, "Restart Now to Update (3 in flight)"},
		{"zh", gateway.Busy{Requests: 1}, "立即重启以更新（1 个进行中）"},
		{"en", gateway.Busy{}, "Restart Now to Update"},
		{"zh", gateway.Busy{}, "立即重启以更新"},
	} {
		if got := trayRestartNow(c.lang, c.b); got != c.want {
			t.Errorf("%s %+v: %q, want %q", c.lang, c.b, got, c.want)
		}
	}
}
