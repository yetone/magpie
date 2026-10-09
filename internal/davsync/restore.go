package davsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/edit"
)

// ErrOff is a Restore, Undo or SetAuto asked for with sync off.
var ErrOff = errors.New("sync is off")

// SetAuto sets how many minutes apart the gateway's magpie syncs by
// itself, one of AutoChoices; Manual: never, only when asked (#847).
func SetAuto(minutes int) error {
	if !slices.Contains(AutoChoices, minutes) {
		return fmt.Errorf("sync by itself every 3, 15, 30 or 60 minutes, or never, not %d", minutes)
	}
	return locked(func() error {
		c, ok := Load()
		if !ok {
			return ErrOff
		}
		c.AutoEvery = minutes
		if minutes == int(Every/time.Minute) {
			c.AutoEvery = 0
		}
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		if err := edit.WriteAtomic(path("sync.json"), b); err != nil {
			return err
		}
		return os.Chmod(path("sync.json"), 0o600)
	})
}

// Restore makes this computer's setup the server's, whatever changed here:
// each part synced that differs is brought in, as a sync brings in a part
// changed only there. What was here first is sealed into the sync folder,
// with the API keys, and Undo brings it back. It is the parts brought in.
func Restore(ctx context.Context) ([]string, error) {
	var brought []string
	err := locked(func() error {
		c, ok := Load()
		if !ok {
			return ErrOff
		}
		d, err := newRemote(c)
		if err != nil {
			return err
		}
		data, ver, err := d.get(ctx, version{}) // the file as it is now, never the cached copy
		if err != nil {
			return err
		}
		if data == nil {
			return errors.New("there is no magpie setup on the server yet: nothing to restore")
		}
		remote, err := backup.Open(data, c.Passphrase)
		if errors.Is(err, backup.ErrPassphrase) {
			return errors.New("the passphrase doesn't open the file on the server: it was sealed with another one; use the passphrase set on your other computers")
		}
		if err != nil {
			return described(c, data, err)
		}
		local, err := collect(c)
		if err != nil {
			return err
		}
		L, R := hashes(local), hashes(remote)
		for _, p := range Parts {
			if L[p] == R[p] || !c.syncs(p) || (p == "library" && remote.Library == nil) {
				continue
			}
			brought = append(brought, p)
		}
		st := loadState()
		if st.Key != stateKey(c) {
			st = state{Key: stateKey(c)}
		}
		if len(brought) > 0 {
			kept, err := keepHere(c)
			if err != nil {
				return fmt.Errorf("keeping this computer's setup before restoring: %w", err)
			}
			for _, p := range brought {
				if err := bring(remote, p); err != nil {
					return fmt.Errorf("bringing in the %s: %w", p, err)
				}
			}
			if local, err = collect(c); err != nil {
				return err
			}
			st.Undo = kept
			st.Notice = &Notice{At: time.Now(), Here: brought, Saved: filepath.Dir(kept), Restored: true}
		}
		// as a sync that brought them in leaves it: the next one has
		// nothing to push back
		st.Local, st.Remote, st.Sum, st.Server = hashes(local), R, sum(data), ver
		remember(data)
		st.Last, st.Error = time.Now(), ""
		saveState(st)
		return nil
	})
	return brought, err
}

// Upload writes this computer's setup over the server's file when that
// file isn't a backup a sync can read — empty, cut short, another app's —
// so a sync stopped on it has a way on that the user chooses. A file that
// opens as a backup, one sealed with another passphrase, a newer magpie's,
// and a page the server answered with instead of a file are never written
// over. What the server held is kept in the sync folder first, and nothing
// is written when it can't be. The write is made over the version read, so
// a file another computer wrote meanwhile isn't.
func Upload(ctx context.Context) error {
	return locked(func() error {
		c, ok := Load()
		if !ok {
			return ErrOff
		}
		d, err := newRemote(c)
		if err != nil {
			return err
		}
		data, ver, err := d.get(ctx, version{})
		if err != nil {
			return err
		}
		if data != nil {
			_, err := backup.Open(data, c.Passphrase)
			switch {
			case err == nil:
				return errors.New("the file on the server is a magpie backup this computer can open: Sync now merges with it instead, and nothing was written over it")
			case errors.Is(err, backup.ErrPassphrase):
				return errors.New("the file on the server is a magpie backup sealed with another passphrase, so it wasn't written over: use the passphrase set on your other computers")
			}
			err = described(c, data, err)
			var nb *notBackup
			if !errors.As(err, &nb) || !nb.f.Replace {
				return err
			}
			if len(data) > 0 {
				if err := keepAside(data, "server-replaced", 5); err != nil {
					return fmt.Errorf("keeping the server's file before writing over it, so nothing was written: %w", err)
				}
			}
		}
		local, err := collect(c)
		if err != nil {
			return err
		}
		b := local
		b.Created, b.App = time.Now().UTC(), "magpie"
		sealed, err := backup.Seal(b, c.Passphrase)
		if err != nil {
			return err
		}
		v, err := d.put(ctx, sealed, ver.ETag)
		if errors.Is(err, errChanged) {
			return errors.New("the file on the server changed while it was being replaced, so it wasn't: sync now to see what it is")
		}
		if err != nil {
			return err
		}
		st := loadState()
		if st.Key != stateKey(c) {
			st = state{Key: stateKey(c)}
		}
		// as the first sync to an empty server leaves it
		st.Local, st.Remote, st.Sum, st.Server = hashes(local), hashes(b), sum(sealed), v
		remember(sealed)
		st.Last, st.Error, st.File = time.Now(), "", nil
		saveState(st)
		return nil
	})
}

// Undo puts back the setup the last Restore replaced. The next sync takes
// it to the server, as any change made here.
func Undo() ([]string, error) {
	var back []string
	err := locked(func() error {
		c, ok := Load()
		if !ok {
			return ErrOff
		}
		st := loadState()
		if st.Key != stateKey(c) || st.Undo == "" {
			return errors.New("there is no restore to undo")
		}
		data, err := os.ReadFile(st.Undo)
		if err != nil {
			return fmt.Errorf("the copy kept before the restore is gone: %w", err)
		}
		kept, err := backup.Open(data, c.Passphrase)
		if err != nil {
			return err
		}
		local, err := collect(withKeys(c))
		if err != nil {
			return err
		}
		L, K := hashes(local), hashes(kept)
		for _, p := range Parts {
			if L[p] == K[p] || !c.syncs(p) || (p == "library" && kept.Library == nil) {
				continue
			}
			if err := bring(kept, p); err != nil {
				return fmt.Errorf("bringing back the %s: %w", p, err)
			}
			back = append(back, p)
		}
		st.Undo, st.Notice = "", nil
		saveState(st)
		return nil
	})
	return back, err
}

// syncs is whether part goes to the server with c.
func (c Config) syncs(part string) bool {
	return (part != "agents" || c.Agents) && (part != "library" || c.library())
}

func withKeys(c Config) Config { c.Keys = true; return c }

// keepHere seals this computer's setup, keys and all, into the sync
// folder before a restore replaces it; the path. The newest few are kept.
func keepHere(c Config) (string, error) {
	b, err := collect(withKeys(c))
	if err != nil {
		return "", err
	}
	sealed, err := backup.Seal(b, c.Passphrase)
	if err != nil {
		return "", err
	}
	dir := path("sync")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Join(dir, time.Now().Format("2006-01-02-150405")+"-before-restore"+backup.Ext)
	if err := edit.WriteAtomic(name, sealed); err != nil {
		return "", err
	}
	os.Chmod(name, 0o600)
	old, _ := filepath.Glob(filepath.Join(dir, "*-before-restore"+backup.Ext))
	slices.Sort(old)
	for len(old) > 5 {
		os.Remove(old[0])
		old = old[1:]
	}
	return name, nil
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
