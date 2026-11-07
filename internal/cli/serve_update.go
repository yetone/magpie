package cli

import (
	"context"
	"log"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// serveUpdating is an update serve was asked for and is putting in.
var serveUpdating atomic.Bool

// servePutIn downloads rel and puts it in where serve runs from, never
// asking for a password (nobody is there to type it); a var for tests.
var servePutIn = func(ctx context.Context, rel *update.Release, app, exe string) error {
	if app != "" {
		staged, err := update.Stage(ctx, rel, app)
		if err != nil {
			return err
		}
		return update.Install(staged, app)
	}
	staged, err := update.StageBinary(ctx, rel)
	if err != nil {
		return err
	}
	return update.InstallBinary(staged, exe)
}

// serveLatest is update.Latest; a var for tests.
var serveLatest = update.Latest

// serveUpdateAsked answers provider.RemoteUpdatePath for `magpie serve`,
// as the app's updater does (gui's updateAsked): another magpie using
// this one as its Remote magpie updated to want. The new version is put
// in where serve runs from, and serve restarts into it once the gateway
// is idle (serveFollow). Unattended, it never asks for a password: a
// folder it can't write is said, as is a container (whose image is
// pulled instead) and a package manager's copy. Follow nil is a serve
// that couldn't tell its own program, which wouldn't restart.
func serveUpdateAsked(follow *serveFollow) func(want string) provider.RemoteUpdate {
	return func(want string) provider.RemoteUpdate {
		switch {
		case !update.Released(version):
			return provider.RemoteUpdate{State: "unsupported", Why: "source"}
		case !update.Newer(want, version):
			return provider.RemoteUpdate{State: "current"}
		}
		if s := settings.Load(); s.NoAutoUpdate || s.NoAutoDownload {
			return provider.RemoteUpdate{State: "off"}
		}
		app := update.Bundle()
		exe, err := updateExecutable()
		switch {
		case follow == nil:
			return provider.RemoteUpdate{State: "unsupported", Why: "not-writable"}
		case app != "":
			if update.Stuck(app) != "" || !update.Writable(filepath.Dir(app)) {
				return provider.RemoteUpdate{State: "unsupported", Why: "not-writable"}
			}
		case err != nil:
			return provider.RemoteUpdate{State: "unsupported", Why: "not-writable"}
		default:
			if p := updatePackagedBy(exe); p != nil {
				return provider.RemoteUpdate{State: "unsupported", Why: "package", Manager: p.Name(), Command: p.Command()}
			}
			if !update.Writable(filepath.Dir(exe)) {
				if updateInContainer() {
					return provider.RemoteUpdate{State: "unsupported", Why: "container"}
				}
				return provider.RemoteUpdate{State: "unsupported", Why: "not-writable"}
			}
		}
		if !serveUpdating.CompareAndSwap(false, true) {
			return provider.RemoteUpdate{State: "updating"}
		}
		go func() {
			defer serveUpdating.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			rel, err := serveLatest(ctx)
			if err != nil {
				log.Println("update asked by a magpie using this one:", err)
				return
			}
			if !update.Newer(rel.Version, version) {
				return
			}
			if err := servePutIn(ctx, rel, app, exe); err != nil {
				log.Println("update asked by a magpie using this one:", err)
				return
			}
			log.Printf("updated to %s, asked by a magpie using this one; restarting once idle", rel.Version)
		}()
		return provider.RemoteUpdate{State: "updating"}
	}
}
