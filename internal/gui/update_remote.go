package gui

import (
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// updateAsked answers provider.RemoteUpdatePath: another magpie using
// this one as its Remote magpie updated to want (nnlrl_75403 on Discord).
// This one updates as its own automatic update would, downloading the
// latest and restarting into it once nothing would be cut short
// (waitIdle), or says why it doesn't: nothing newer, automatic updates
// or downloads turned off here (the user's to keep), or a copy it can't
// replace (a container's image, a package manager's, a folder it can't
// write, a build from source).
func (u *updater) updateAsked(want string) provider.RemoteUpdate {
	switch {
	case !update.Released(Version):
		return provider.RemoteUpdate{State: "unsupported", Why: "source"}
	case !update.Newer(want, Version):
		return provider.RemoteUpdate{State: "current"}
	}
	if s := settings.Load(); s.NoAutoUpdate || s.NoAutoDownload {
		return provider.RemoteUpdate{State: "off"}
	}
	u.mu.Lock()
	restart, bundle, exe, stuck, packaged := u.autoRestart, u.bundle, u.exe, u.stuck, u.packaged
	u.mu.Unlock()
	if bundle == "" && exe == "" || restart == nil {
		out := provider.RemoteUpdate{State: "unsupported", Why: stuck}
		if stuck == "package" && packaged != nil {
			out.Manager, out.Command = packaged.Name(), packaged.Command()
		}
		if out.Why == "" || out.Why == "translocated" || out.Why == "read-only" {
			out.Why = "not-writable"
		}
		return out
	}
	u.download()
	u.waitIdle(restart, false)
	return provider.RemoteUpdate{State: "updating"}
}

// remoteUpdateJSON is a Remote magpie's row's word on its last ask to
// update (provider.UpdateRemotes): Say on the row, Tip over it, each an
// English key the page translates with the rest as its values.
type remoteUpdateJSON struct {
	Say     string `json:"say"`
	Tip     string `json:"tip"`
	V       string `json:"v,omitempty"`
	Manager string `json:"manager,omitempty"`
	Cmd     string `json:"cmd,omitempty"`
}

// remoteUpdateNote is what the Remote magpie id's row says of its last
// ask to update, nil when nothing: one that is current, or not asked.
func remoteUpdateNote(id string, now time.Time) *remoteUpdateJSON {
	u, ok := provider.RemoteUpdateOf(id, now)
	if !ok {
		return nil
	}
	j := &remoteUpdateJSON{Say: "can't update itself", V: u.Version, Manager: u.Manager, Cmd: u.Command}
	switch u.State {
	case "updating":
		j.Say, j.Tip = "updating to {v}", "This magpie updated, so that one was asked to update to {v} too. It restarts into it once nothing is in flight."
	case "off":
		j.Say, j.Tip = "automatic updates off", "Automatic updates are turned off on that computer, so it stays on its version."
	case "old":
		j.Say, j.Tip = "update it there", "That magpie is too old to be updated from here: update it on that computer once."
	case "refused":
		j.Say, j.Tip = "update it there", "Updating it from here takes a gateway key with full access there; this provider's key is read-only or held."
	default:
		switch u.Why {
		case "container":
			j.Tip = "It runs in a container: pull the new image (docker pull ghcr.io/yetone/magpie:latest) and recreate the container."
		case "package":
			j.Tip = "{manager} installed it there: update it with {manager}."
			if u.Command != "" {
				j.Tip = "{manager} installed it there: update it with {cmd}"
			}
		case "source":
			j.Tip = "It was built from source: update it the way it was built."
		default:
			j.Tip = "magpie can't replace itself where it runs on that computer: update it there."
		}
	}
	return j
}
