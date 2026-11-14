package gui

import (
	"context"
	"log"
	"sync/atomic"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// A fresh install of the Stable channel's version stays on Stable
// (fishmagic80 on X). usemagpie.ai offers Stable's downloads beside the
// newest's, and Settings' Update channel is every version until changed,
// so an app put in from a Stable download was offered the newest at its
// first check. Before that check, a fresh install (whatsNew.start: no
// version run before, and magpie's folder new) asks the feed for Stable's
// version; when it is this one and a newer one is out, the channel becomes
// Stable. A channel already set is kept, and so is every version when
// this one is also the newest, where the download can't be told apart.

// freshInstall: this run is magpie's first here, and the channel isn't
// settled yet. A feed that can't be read leaves it for the next check.
var freshInstall atomic.Bool

// latestStable and latestEvery are update's; tests stand in for them.
var (
	latestStable = update.LatestStable
	latestEvery  = func(ctx context.Context) (*update.Release, error) { return update.LatestEvery(ctx, "") }
)

// takeStable runs before an update check reads the channel.
func takeStable(ctx context.Context) {
	if !freshInstall.Load() {
		return
	}
	if !update.Released(Version) || settings.Shared().UpdateChannel != "" {
		freshInstall.Store(false)
		return
	}
	st, err := latestStable(ctx)
	if err != nil {
		log.Println("stable on a fresh install:", err) // unknown: asked again next check
		return
	}
	if st.Version != Version {
		freshInstall.Store(false)
		return
	}
	every, err := latestEvery(ctx)
	if err != nil {
		log.Println("stable on a fresh install:", err)
		return
	}
	freshInstall.Store(false)
	if !update.Newer(every.Version, Version) {
		return
	}
	if took, err := settings.TakeStable(); err != nil {
		log.Println("stable on a fresh install:", err)
	} else if took {
		log.Printf("a fresh install of Stable's %s: the update channel is Stable", Version)
	}
}
