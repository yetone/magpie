package gui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// backupRoutes are the Settings page's Export and Import, and the WebDAV
// sync set up there.
func backupRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("POST /api/backup/export", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Pass    string
			Keys    bool
			Library *bool // nil is yes
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		b, err := backup.Collect(in.Keys, Version)
		if err != nil {
			fail(rw, err)
			return
		}
		if in.Library != nil && !*in.Library {
			b.Library = nil
		}
		data, err := backup.Seal(b, in.Pass)
		if err != nil {
			fail(rw, err)
			return
		}
		dir := downloads()
		name := filepath.Join(dir, "magpie-"+time.Now().Format("2006-01-02")+backup.Ext)
		for i := 2; ; i++ { // never over an earlier one
			if _, err := os.Stat(name); err != nil {
				break
			}
			name = filepath.Join(dir, fmt.Sprintf("magpie-%s-%d%s", time.Now().Format("2006-01-02"), i, backup.Ext))
		}
		if err := edit.WriteAtomic(name, data); err != nil {
			fail(rw, err)
			return
		}
		os.Chmod(name, 0o600)
		_ = w.OpenFolder(dir) // saved either way; the path is in the answer
		writeJSON(rw, map[string]any{"path": tilde(name), "providers": len(b.Providers), "profiles": len(b.Profiles), "agents": len(b.Agents), "library": b.Library != nil})
	})
	mux.HandleFunc("POST /api/backup/import", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Data    string
			Pass    string
			Agents  bool
			Library *bool // nil is yes
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		data, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			fail(rw, err)
			return
		}
		b, err := backup.Open(data, in.Pass)
		if err != nil {
			fail(rw, err)
			return
		}
		parts := backup.All
		parts.Agents = in.Agents
		parts.Library = in.Library == nil || *in.Library
		res, err := backup.Restore(b, parts)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, res)
	})

	mux.HandleFunc("GET /api/davsync", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, davsync.Status())
	})
	mux.HandleFunc("POST /api/davsync/{action}", func(rw http.ResponseWriter, r *http.Request) {
		// no time limit: a large backup on a slow line takes minutes (#657),
		// and davsync ends a request that stops moving
		ctx := r.Context()
		var err error
		var brought []string
		switch r.PathValue("action") {
		case "save": // and sync at once, so a wrong address or password shows now
			var c davsync.Config
			if err = json.NewDecoder(r.Body).Decode(&c); err == nil {
				err = davsync.Configure(c)
			}
			if err != nil { // said in the form, what was typed in it kept
				fail(rw, err)
				return
			}
			err = busy(davsync.SyncNow(ctx)) // how it went is in the status
		case "now":
			err = busy(davsync.SyncNow(ctx))
		case "off":
			err = davsync.Off()
		case "dismiss":
			err = davsync.Dismiss()
		case "auto": // how often it syncs by itself, or never (#847)
			var in struct{ Minutes int }
			if err = json.NewDecoder(r.Body).Decode(&in); err == nil {
				err = davsync.SetAuto(in.Minutes)
			}
		case "restore": // the server's setup over this computer's, which is kept
			brought, err = davsync.Restore(ctx)
		case "undo":
			_, err = davsync.Undo()
		case "upload": // this computer's setup over a server file that isn't a backup, kept first
			err = davsync.Upload(ctx)
		case "reveal":
			err = w.OpenFolder(filepath.Join(settings.Dir(), "sync"))
		default:
			http.NotFound(rw, r)
			return
		}
		v := davsync.Status()
		switch {
		case errors.Is(err, davsync.ErrBusy):
			// nothing ran to put it in the status: it is said there all the same
			v.Error = err.Error()
		case err != nil:
			fail(rw, err)
			return
		}
		if r.PathValue("action") == "restore" { // and what it brought in: nothing when it was the same
			writeJSON(rw, struct {
				davsync.View
				Brought []string `json:"brought"`
			}{v, brought})
			return
		}
		writeJSON(rw, v)
	})
}

// busy is what of a sync's failure the status doesn't hold: that it waited
// its time out for another magpie's. The rest is in the status.
func busy(err error) error {
	if errors.Is(err, davsync.ErrBusy) {
		return err
	}
	return nil
}

// downloads is where an export is saved: the Downloads folder, or home
// when there is none.
func downloads() string {
	home, _ := os.UserHomeDir()
	if d := filepath.Join(home, "Downloads"); isDir(d) {
		return d
	}
	return home
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
