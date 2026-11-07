package provider

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// A Remote magpie follows this one's version (nnlrl_75403 on Discord):
// once magpie here has updated, every Remote magpie it uses that is on an
// older release is asked to update itself too, as that magpie's own
// automatic update would, so the two don't drift apart and nobody has to
// go to the other computer. Nothing is set up for it: the provider's key
// is the owner's, and the remote takes the ask only with a gateway key of
// full access (gateway.remoteUpdate). A remote that can't update itself
// (Docker, a package manager's copy, a folder it can't write) says why,
// and the provider's row here shows it.

// RemoteUpdatePath has the gateway update this magpie to its channel's
// latest release, once it is newer than ?version= says the asker runs.
const RemoteUpdatePath = "/v1/magpie/update"

// RemoteUpdate is RemoteUpdatePath's answer, and what is kept here of a
// remote's last ask.
type RemoteUpdate struct {
	// updating: it is being put in, and magpie restarts into it once
	// idle; current: nothing newer to put in; off: automatic updates are
	// turned off there; unsupported: it can't update itself (Why);
	// old: a magpie from before RemoteUpdatePath; refused: the key isn't
	// one of full access there
	State string `json:"state"`
	// Version is what the remote runs (as answered), or is updating to
	// (kept here)
	Version string `json:"version,omitempty"`
	// Why it can't: container, package, not-writable, source
	Why string `json:"why,omitempty"`
	// Manager and Command, a package manager's copy: its name and what
	// updates it
	Manager string `json:"manager,omitempty"`
	Command string `json:"command,omitempty"`
}

// remoteUpdateWait is how long a remote is waited for to take the ask:
// it answers once the download has started, not when it is done.
const remoteUpdateWait = 20 * time.Second

// AskRemoteUpdate asks the Remote magpie p to update itself, this magpie
// running version.
func AskRemoteUpdate(ctx context.Context, p Provider, version string) RemoteUpdate {
	b, err := remoteBody(ctx, p, http.MethodPost, RemoteUpdatePath+"?"+url.Values{"version": {version}}.Encode(), remoteUpdateWait)
	switch {
	case err == nil:
		var out RemoteUpdate
		if json.Unmarshal(b, &out) != nil || out.State == "" {
			return RemoteUpdate{State: "unknown"}
		}
		return out
	case err.Error() == errRemoteNoShare:
		return RemoteUpdate{State: "old"}
	case errors.Is(err, errKeyHeld), err.Error() == errRemoteReadOnly, err.Error() == errRemoteKey, err.Error() == errRemoteNotShared:
		return RemoteUpdate{State: "refused"}
	}
	return RemoteUpdate{State: "unknown"}
}

// RemoteVersion is the version the Remote magpie p answers GET /v1 with,
// "" when it can't be told.
func RemoteVersion(ctx context.Context, p Provider) string {
	b, err := remoteBody(ctx, p, http.MethodGet, "/v1", RemoteHubWait)
	if err != nil {
		return ""
	}
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &info) != nil || info.Name != "magpie" {
		return ""
	}
	return info.Version
}

// remoteUpdates is the last ask of each remote, by provider id.
var remoteUpdates struct {
	sync.Mutex
	m map[string]remoteUpdateAsked
}

type remoteUpdateAsked struct {
	at   time.Time
	from string // the version this magpie ran when it asked
	out  RemoteUpdate
}

// remoteUpdateAgain is how long a remote that was asked isn't asked again
// for the same version; remoteUpdateShown, how long "updating" is shown.
const (
	remoteUpdateAgain = 6 * time.Hour
	remoteUpdateShown = 30 * time.Minute
)

// RemoteUpdateOf is what the Remote magpie id last answered an ask to
// update, when the row should say something of it: updating (for a
// while), or why it didn't.
func RemoteUpdateOf(id string, now time.Time) (RemoteUpdate, bool) {
	remoteUpdates.Lock()
	a, ok := remoteUpdates.m[id]
	remoteUpdates.Unlock()
	switch {
	case !ok, a.out.State == "current", a.out.State == "unknown":
		return RemoteUpdate{}, false
	case a.out.State == "updating" && now.Sub(a.at) > remoteUpdateShown:
		return RemoteUpdate{}, false
	}
	return a.out, true
}

// ForgetRemoteUpdatesForTest drops what remotes answered.
func ForgetRemoteUpdatesForTest() {
	remoteUpdates.Lock()
	remoteUpdates.m = nil
	remoteUpdates.Unlock()
}

// SetRemoteUpdateForTest keeps out as id's last answer.
func SetRemoteUpdateForTest(id string, out RemoteUpdate) {
	noteRemoteUpdate(id, "", out, time.Now())
}

func noteRemoteUpdate(id, from string, out RemoteUpdate, now time.Time) {
	remoteUpdates.Lock()
	if remoteUpdates.m == nil {
		remoteUpdates.m = map[string]remoteUpdateAsked{}
	}
	remoteUpdates.m[id] = remoteUpdateAsked{at: now, from: from, out: out}
	remoteUpdates.Unlock()
}

// UpdateRemotes asks every Remote magpie in use (on, not this one) on an
// older release than version to update itself, each at most once in
// remoteUpdateAgain for a version. Nothing is asked from a build that
// isn't a release, nor while automatic updates are off here: the user
// keeps this magpie where it is, and its remotes with it.
func UpdateRemotes(ctx context.Context, version string) {
	if !update.Released(version) {
		return
	}
	if s, err := settings.LoadChecked(); err != nil || s.NoAutoUpdate {
		return
	}
	var wg sync.WaitGroup
	for _, p := range remoteMagpies() {
		if p.SelfAnswered() {
			continue
		}
		remoteUpdates.Lock()
		a, asked := remoteUpdates.m[p.ID]
		remoteUpdates.Unlock()
		if asked && a.from == version && time.Since(a.at) < remoteUpdateAgain {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			have := RemoteVersion(ctx, p)
			if have == "" || !update.Released(have) {
				return // unknown, or a build from source: its own to keep
			}
			if !update.Newer(version, have) {
				noteRemoteUpdate(p.ID, version, RemoteUpdate{State: "current", Version: have}, time.Now())
				return
			}
			out := AskRemoteUpdate(ctx, p, version)
			if out.State == "updating" {
				out.Version = version
			}
			log.Printf("remote magpie %s on %s, asked to update to %s: %s %s", remoteName(p), have, version, out.State, out.Why)
			noteRemoteUpdate(p.ID, version, out, time.Now())
		}()
	}
	wg.Wait()
}

// KeepRemotesUpdated runs UpdateRemotes a little after start (this magpie
// may just have updated) and every hour after, until ctx ends.
func KeepRemotesUpdated(ctx context.Context, version string) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		UpdateRemotes(ctx, version)
		t.Reset(time.Hour)
	}
}
