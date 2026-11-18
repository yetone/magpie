package gui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// quitSays is a window that says when it quits.
type quitSays struct {
	folderOnly
	quit chan struct{}
}

func (q quitSays) Quit() { q.quit <- struct{}{} }

// TestRestartStartsMagpieAgain (yansheng1003 on X: "请在刷新旁边增加一个重启
// 按钮"): Settings' Restart starts this magpie again with no update
// waiting, as it is, back on the tab the window was on, and the app quits
// for it. A relaunch that can't be arranged leaves it running and says so.
// magpie web runs again in its own place; a browser on the app's page
// restarts the app.
func TestRestartStartsMagpieAgain(t *testing.T) {
	sandboxHome(t)
	oldBundle, oldExe, oldRelaunch, oldHands := bundleOf, executable, relaunchBinary, handsOver
	t.Cleanup(func() {
		bundleOf, executable, relaunchBinary, handsOver = oldBundle, oldExe, oldRelaunch, oldHands
		webReexec.Store(false)
	})
	bundleOf = func() string { return "" }
	executable = func() (string, error) { return "/opt/magpie/magpie", nil }
	handsOver = func() bool { return false } // Windows': opened once this one has exited
	if s := updates.json().State; s == "ready" {
		t.Fatalf("an update is staged in the test: %q", s)
	}
	type relaunch struct {
		exe, aside, view string
		window           bool
	}
	var got []relaunch
	var fail error
	relaunchBinary = func(exe, aside string, window bool, view string) error {
		got = append(got, relaunch{exe, aside, view, window})
		return fail
	}
	post := func(w Windows, body string) *httptest.ResponseRecorder {
		t.Helper()
		mux := http.NewServeMux()
		restartRoutes(mux, w)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/restart", strings.NewReader(body)))
		return rec
	}
	quits := func(q quitSays) bool {
		select {
		case <-q.quit:
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}

	app := quitSays{quit: make(chan struct{}, 1)}
	if rec := post(app, `{"view":"settings"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body)
	}
	if len(got) != 1 || got[0] != (relaunch{"/opt/magpie/magpie", "", "settings", true}) {
		t.Fatalf("relaunched %+v, want this binary again on Settings", got)
	}
	if !quits(app) {
		t.Fatal("the app didn't quit for the restart")
	}

	got, fail = nil, errors.New("no helper to open it again")
	if rec := post(app, `{}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "couldn't start again") {
		t.Fatalf("a relaunch that failed: %d %s", rec.Code, rec.Body)
	}
	if quits(app) {
		t.Fatal("the app quit with nothing to open it again")
	}
	fail = nil

	// a browser on the app's page: the app restarts and quits
	got = nil
	inner := quitSays{quit: make(chan struct{}, 1)}
	if rec := post(appWebHost{webHost{}, inner}, `{"view":"usage"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("from a browser: %d %s", rec.Code, rec.Body)
	}
	if len(got) != 1 || got[0].view != "" {
		t.Fatalf("from a browser relaunched %+v: the app, not a tab of the browser's", got)
	}
	if !quits(inner) {
		t.Fatal("the app didn't quit for a browser's restart")
	}

	// magpie web: run again in its place, nothing relaunched
	got = nil
	quit := make(chan struct{}, 1)
	if rec := post(webHost{quit: func() { quit <- struct{}{} }}, `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("magpie web: %d %s", rec.Code, rec.Body)
	}
	if len(got) != 0 || !webReexec.Load() {
		t.Fatalf("magpie web relaunched %+v, re-exec %v", got, webReexec.Load())
	}
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("magpie web didn't end for its re-exec")
	}
}

// TestUpdateSaysItsRun: a page that restarted magpie with no update
// reloads once another run answers; /api/update says which run it is.
func TestUpdateSaysItsRun(t *testing.T) {
	if j := updates.json(); j.Run == 0 || j.Run != runStarted {
		t.Fatalf("run %d, want %d", j.Run, runStarted)
	}
}
