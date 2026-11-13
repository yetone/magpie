package gui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
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
			Memory  *bool // nil is yes
			SignIns bool  // the subscriptions' and plugins' sign-ins too
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
		if in.Memory != nil && !*in.Memory {
			b.Memory = nil
		}
		if in.SignIns {
			if err := backup.WithSignIns(&b); err != nil {
				fail(rw, fmt.Errorf("the sign-ins couldn't be read, so no backup was made: %w", err))
				return
			}
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
		writeJSON(rw, map[string]any{"path": tilde(name), "providers": len(b.Providers), "profiles": len(b.Profiles), "agents": len(b.Agents), "library": b.Library != nil, "signIns": signInCount(b.SignIns)})
	})
	mux.HandleFunc("POST /api/backup/import", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Data    string
			Pass    string
			Agents  bool
			Library *bool // nil is yes
			Memory  *bool // nil is yes: merged with the memories here
			SignIns *bool // nil is yes: the backup's sign-ins, when it has them
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
		parts.Memory = in.Memory == nil || *in.Memory
		parts.SignIns = in.SignIns == nil || *in.SignIns
		res, err := backup.Restore(b, parts)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, res)
	})

	// backups made by themselves into a folder here, and putting one back
	// (jwaitq on Discord)
	mux.HandleFunc("GET /api/autobackup", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, autoView())
	})
	mux.HandleFunc("POST /api/autobackup/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On    *bool
			Hours *int
			Keep  *int
			Dir   *string // "" is the default folder
			Name  string
			// SignIns puts the sign-ins in the backups, sealed with
			// Passphrase ("" keeps the one set)
			SignIns    *bool
			Passphrase string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			fail(rw, err)
			return
		}
		set := func(f func(*backup.AutoConfig)) error {
			c, err := backup.LoadAuto()
			if err != nil {
				return err
			}
			f(&c)
			return backup.SaveAuto(c)
		}
		var err error
		switch r.PathValue("action") {
		case "set": // only what was sent: the rest stays as the user set it
			err = set(func(c *backup.AutoConfig) {
				if in.On != nil {
					c.Off = !*in.On
				}
				if in.Hours != nil {
					c.Hours = *in.Hours
				}
				if in.Keep != nil {
					c.Keep = *in.Keep
				}
				if in.Dir != nil && *in.Dir == "" {
					c.Dir = ""
				}
				if in.Passphrase != "" {
					c.Passphrase = in.Passphrase
				}
				if in.SignIns != nil {
					c.SignIns = *in.SignIns
				}
			})
		case "choose": // a folder from the system's picker, checked writable
			var dir string
			if dir, err = w.ChooseFolder("Choose a folder for backups"); err == nil && dir != "" {
				if err = writable(dir); err == nil {
					err = set(func(c *backup.AutoConfig) { c.Dir = dir })
				}
			}
		case "now":
			_, err = backup.AutoNow(Version)
		case "reveal":
			dir := backup.AutoStatus().Dir
			if err = os.MkdirAll(dir, 0o700); err == nil {
				err = w.OpenFolder(dir)
			}
		case "restore": // what is here is backed up first, to undo it
			res, before, err := backup.AutoRestore(in.Name, Version)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]any{"view": autoView(), "result": res, "before": before.Name})
			return
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, autoView())
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
		case "preview": // what syncing would do (#6428): the setup saved, or the form's when one is sent
			var in struct{ Form *davsync.Config }
			var p davsync.Plan
			if err = json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
				fail(rw, err)
				return
			}
			if in.Form != nil {
				p, err = davsync.PreviewWith(ctx, *in.Form)
			} else {
				p, err = davsync.Preview(ctx)
			}
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, p)
			return
		case "sync-previewed": // the sync a preview showed, nothing else
			var in struct{ Token string }
			if err = json.NewDecoder(r.Body).Decode(&in); err == nil {
				if err = davsync.SyncPreviewed(ctx, in.Token); !errors.Is(err, davsync.ErrPlanChanged) {
					err = busy(err) // how it went is in the status
				}
			}
		case "undo-sync": // the parts the last sync brought in, as they were before it
			var in struct{ Force bool }
			if err = json.NewDecoder(r.Body).Decode(&in); err == nil {
				_, err = davsync.UndoSync(in.Force)
			}
			var since *davsync.ChangedSince
			if errors.As(err, &since) { // the page asks before writing over them
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusConflict)
				json.NewEncoder(rw).Encode(map[string]any{"error": err.Error(), "changed": since.Parts})
				return
			}
		case "off":
			err = davsync.Off()
		case "dismiss":
			err = davsync.Dismiss()
		case "auto": // how often it syncs by itself, or never (#847)
			var in struct{ Minutes int }
			if err = json.NewDecoder(r.Body).Decode(&in); err == nil {
				err = davsync.SetAuto(in.Minutes)
			}
		case "follow-server": // this computer takes the server's setup and sends none (Ramon317317 on X)
			var in struct{ On bool }
			if err = json.NewDecoder(r.Body).Decode(&in); err == nil {
				if err = davsync.SetFollowServer(in.On); err == nil {
					err = busy(davsync.SyncNow(ctx)) // how it went is in the status
				}
			}
		case "restore": // the server's setup over this computer's, which is kept
			brought, err = davsync.Restore(ctx)
		case "undo":
			_, err = davsync.Undo()
		case "upload": // this computer's setup over a server file that isn't a backup, kept first
			err = davsync.Upload(ctx)
		case "follow": // the agent fields kept on this computer follow the others again
			err = busy(davsync.Follow(ctx))
		case "reveal":
			err = w.OpenFolder(filepath.Join(settings.Dir(), "sync"))
		case "setup-text": // this setup as text sealed with its passphrase, to paste on another computer
			text, err := davsync.SetupText()
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]string{"text": text})
			return
		case "setup-open": // a setup pasted from another computer, for the form: nothing is saved
			var in struct{ Text, Passphrase string }
			if err = json.NewDecoder(r.Body).Decode(&in); err != nil {
				fail(rw, err)
				return
			}
			s, err := davsync.OpenSetupText(in.Text, in.Passphrase)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, s)
			return
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

// autoView is backup.AutoStatus with the folder as the page shows it.
func autoView() backup.AutoView {
	v := backup.AutoStatus()
	v.Dir = tilde(v.Dir)
	return v
}

// writable says whether a file can be made in dir.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".magpie-backup-*.tmp")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

// busy is what of a sync's failure the status doesn't hold: that it waited
// its time out for another magpie's. The rest is in the status.
func busy(err error) error {
	if errors.Is(err, davsync.ErrBusy) {
		return err
	}
	return nil
}

// downloads is where an export is saved: the Downloads folder the system
// keeps (appdir.Downloads: Windows' known folder, the XDG one on Linux,
// which 1Shin on Discord had moved off home\Downloads), else
// home\Downloads, else home when there is none.
func downloads() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{appdir.Downloads(), filepath.Join(home, "Downloads")} {
		if d != "" && isDir(d) {
			return d
		}
	}
	return home
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// signInCount is how many sign-ins a backup carries.
func signInCount(s *provider.SignIns) int {
	if s == nil {
		return 0
	}
	return len(s.Logins) + len(s.Plugins)
}
