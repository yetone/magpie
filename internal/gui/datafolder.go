package gui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// Settings › Sync's Data folder (CHENGJIAHEGOXIS on X: magpie's files on
// an external SSD rather than the Mac's own disk; buhaodeshiyehao on X):
// where magpie keeps its files and cache, and moving them to a folder on
// another disk, or back. The move is appdir's (move.go). It is never made
// under this magpie: it is asked for, magpie starts itself again, and the
// new one makes it before it reads anything.

type dataFolderView struct {
	appdir.Place
	// Other is a magpie other than this one serving the gateway (magpie
	// serve, a launch agent): the move waits for a start without it.
	Other bool `json:"other,omitempty"`
}

func dataFolder() dataFolderView {
	p := appdir.Where()
	for _, s := range []*string{&p.Config, &p.Cache, &p.ConfigAt, &p.CacheAt, &p.Moved} {
		if *s != "" {
			*s = tilde(*s)
		}
	}
	if p.Pending != "" && p.Pending != "home" {
		p.Pending = tilde(p.Pending)
	}
	v := dataFolderView{Place: p}
	if s := servedBy(); s.Running && s.Pid != os.Getpid() {
		v.Other = true
	}
	return v
}

// servedBy is gateway.ServedBy; tests stand in for it.
var servedBy = gateway.ServedBy

// restartSelf starts this magpie again once it has exited, with its window
// up: the new one makes the move asked for, then opens as before. Tests
// stand in for it.
var restartSelf = func() error {
	if b := bundleOf(); b != "" {
		return update.Relaunch(b, true, false)
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	return relaunchBinary(exe, "", true, "")
}

func dataFolderRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/datafolder", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, dataFolder())
	})
	mux.HandleFunc("POST /api/datafolder/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Dir string } // move: the folder picked; "" asks with the system's picker
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			fail(rw, err)
			return
		}
		app := w
		if a, ok := w.(appWebHost); ok { // a browser on the app's page: the app restarts
			app = a.app
		}
		var err error
		switch r.PathValue("action") {
		case "move", "home":
			dir := ""
			if r.PathValue("action") == "move" {
				if dir = in.Dir; dir == "" {
					if dir, err = w.ChooseFolder("Choose a folder for magpie's files"); err != nil || dir == "" {
						break // cancelled: nothing asked
					}
				}
			}
			if _, err = appdir.AskMove(dir); err != nil {
				break
			}
			v := dataFolder()
			// magpie web, or another magpie serving: the user starts it
			// again; the app starts itself again now
			restart := !ownWeb(app) && !v.Other
			if restart {
				if err = restartSelf(); err != nil {
					appdir.CancelMove()
					break
				}
			}
			writeJSON(rw, map[string]any{"view": v, "restart": restart})
			if restart {
				go leave(app.Quit)
			}
			return
		case "cancel":
			err = appdir.CancelMove()
		case "reveal":
			p := appdir.Where()
			dir := p.Config
			if p.ConfigAt != "" {
				dir = p.ConfigAt
			}
			err = w.OpenFolder(dir)
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"view": dataFolder()})
	})
}
