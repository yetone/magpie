package gui

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/update"
)

// A restart of magpie as it is, asked for from the tray menu or Settings'
// version row (yansheng1003 on X: "请在刷新旁边增加一个重启按钮"). What a
// user changes is meant to apply live, and most of it does; this is the
// way out for what doesn't yet, without quitting and opening magpie by
// hand. It goes the way the updater's restart goes: on a Mac or Linux the
// new magpie takes the gateway over while this one finishes its requests
// (handover.go); elsewhere it opens once this one has exited.

// runStarted is when this run of magpie started: /api/update's run.
var runStarted = time.Now().UnixNano()

// restartSelfNow arranges for this magpie to start again once it is gone;
// the caller then quits (leave). An update staged is installed on the way,
// as the updater's restart would. magpie web runs again in its own place
// (Web.Wait). False when it can't: the caller runs on.
func restartSelfNow(web, window bool, view string) bool {
	if pending.Load() != nil {
		return false // a handover under way restarts already
	}
	if updates.json().State == "ready" {
		return restartToUpdate(web, window, view)
	}
	unmarkQuietRestart() // one asked for comes to the front
	if web {
		webReexec.Store(true)
		return true
	}
	bundle := bundleOf()
	exe := ""
	if bundle == "" {
		var err error
		if exe, err = executable(); err != nil {
			log.Println("restart:", err)
			return false
		}
	}
	// the Mac's always opens its window, as the updater's asked restart does
	if bundle != "" {
		window = true
	}
	if beginHandover(bundle, exe, window, view, false) {
		return true
	}
	var err error
	if bundle != "" {
		err = update.Relaunch(bundle, window, false)
	} else {
		err = relaunchBinary(exe, "", window, view)
	}
	if err != nil {
		// nothing would open it again: it runs on, and the page says so
		log.Println("restart:", err)
		return false
	}
	return true
}

// restartAppFrom is restartSelfNow for a page: the app's, a browser's on
// the app's page (the app restarts, its window back if it was up), or
// magpie web's (it runs again in its place). view is the tab the window's
// page is on, to come back to it.
func restartAppFrom(w Windows, view string) (Windows, bool) {
	if a, ok := w.(appWebHost); ok {
		return a.app, restartSelfNow(false, mainShown(a.app), "")
	}
	return w, restartSelfNow(ownWeb(w), view != "" || mainShown(w), view)
}

func restartRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("POST /api/restart", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			View string `json:"view"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in) // nothing sent: no tab to come back to
		app, ok := restartAppFrom(w, in.View)
		if !ok {
			fail(rw, errors.New("magpie couldn't start again: quit it and open it again"))
			return
		}
		rw.WriteHeader(http.StatusNoContent)
		go leave(app.Quit)
	})
}
