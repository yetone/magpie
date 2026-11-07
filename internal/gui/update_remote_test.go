package gui

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// nnlrl_75403 on Discord: the magpie using this one as its Remote magpie
// updated, and asks this one to follow. It updates as its own automatic
// update would, restarting once nothing is in flight, or says why not:
// already there, the user's updates off, a container, a package
// manager's copy, a folder it can't write, a build from source.
func TestUpdateAskedByAnotherMagpie(t *testing.T) {
	was := Version
	t.Cleanup(func() { Version = was })
	Version = "v0.1.100"

	busy := &fakeBusy{}
	busy.set(gateway.Busy{Requests: 1})
	// a download under way: begin finds it, and the ask waits for it
	u := &updater{state: "downloading", exe: "/opt/magpie/magpie", latest: &update.Release{Version: "v0.1.200"}}
	quick(t, busy, u)
	var restarts atomic.Int32
	u.autoRestart = func() bool { restarts.Add(1); return true }

	if got := u.updateAsked("v0.1.100"); got.State != "current" {
		t.Fatalf("same version: %+v", got)
	}
	if got := u.updateAsked("v0.1.99"); got.State != "current" {
		t.Fatalf("an older asker: %+v", got)
	}
	for _, s := range []settings.Settings{{NoAutoUpdate: true}, {NoAutoDownload: true}} {
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		if got := u.updateAsked("v0.1.200"); got.State != "off" {
			t.Fatalf("%+v: %+v", s, got)
		}
		if w, _ := u.waitingFor(); w {
			t.Fatalf("%+v: a restart waits", s)
		}
	}
	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}

	if got := u.updateAsked("v0.1.200"); got.State != "updating" {
		t.Fatalf("newer asker: %+v", got)
	}
	if w, _ := u.waitingFor(); !w {
		t.Fatal("no restart waits for the download")
	}
	u.mu.Lock()
	u.state = "ready"
	u.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if restarts.Load() != 0 {
		t.Fatal("restarted with a request in flight")
	}
	busy.set(gateway.Busy{Last: time.Now().Add(-time.Minute)})
	eventually(t, "never restarted once idle", func() bool { return restarts.Load() == 1 })

	Version = "dev"
	if got := (&updater{exe: "/x"}).updateAsked("v0.1.200"); got.State != "unsupported" || got.Why != "source" {
		t.Fatalf("a build from source: %+v", got)
	}
	Version = "v0.1.100"
	restart := func() bool { return true }
	for _, c := range []struct {
		u   *updater
		why string
	}{
		{&updater{stuck: "container", autoRestart: restart}, "container"},
		{&updater{stuck: "not-writable", autoRestart: restart}, "not-writable"},
		{&updater{stuck: "translocated", autoRestart: restart}, "not-writable"},
		{&updater{stuck: "package", packaged: &update.Packaged{Manager: "pacman", Package: "magpie-bin"}, autoRestart: restart}, "package"},
		// magpie serve's place, with nothing that restarts it here
		{&updater{exe: "/opt/magpie/magpie"}, "not-writable"},
	} {
		got := c.u.updateAsked("v0.1.200")
		if got.State != "unsupported" || got.Why != c.why {
			t.Errorf("%s: %+v", c.why, got)
		}
		if c.why == "package" && got.Manager == "" {
			t.Errorf("package: no manager named: %+v", got)
		}
		if w, _ := c.u.waitingFor(); w {
			t.Errorf("%s: a restart waits", c.why)
		}
	}
}

// The row of a Remote magpie says what its last ask came to, in words
// the page translates: updating for a while, or why it didn't, with the
// command a container or a package manager is updated by.
func TestRemoteUpdateNote(t *testing.T) {
	provider.ForgetRemoteUpdatesForTest()
	t.Cleanup(provider.ForgetRemoteUpdatesForTest)
	now := time.Now()
	if j := remoteUpdateNote("office", now); j != nil {
		t.Fatalf("never asked: %+v", j)
	}
	cases := []struct {
		out      provider.RemoteUpdate
		say, tip string
	}{
		{provider.RemoteUpdate{State: "updating", Version: "v0.1.200"}, "updating to {v}", "{v}"},
		{provider.RemoteUpdate{State: "unsupported", Why: "container"}, "can't update itself", "docker pull ghcr.io/yetone/magpie:latest"},
		{provider.RemoteUpdate{State: "unsupported", Why: "package", Manager: "Homebrew", Command: "brew upgrade magpie"}, "can't update itself", "{cmd}"},
		{provider.RemoteUpdate{State: "unsupported", Why: "not-writable"}, "can't update itself", "update it there"},
		{provider.RemoteUpdate{State: "off"}, "automatic updates off", "turned off"},
		{provider.RemoteUpdate{State: "old"}, "update it there", "too old"},
		{provider.RemoteUpdate{State: "refused"}, "update it there", "full access"},
	}
	for _, c := range cases {
		provider.SetRemoteUpdateForTest("office", c.out)
		j := remoteUpdateNote("office", now)
		if j == nil || j.Say != c.say || !strings.Contains(j.Tip, c.tip) {
			t.Errorf("%+v: %+v", c.out, j)
		}
	}
	provider.SetRemoteUpdateForTest("office", provider.RemoteUpdate{State: "current"})
	if j := remoteUpdateNote("office", now); j != nil {
		t.Fatalf("current: %+v", j)
	}
	provider.SetRemoteUpdateForTest("office", provider.RemoteUpdate{State: "updating", Version: "v0.1.200"})
	if j := remoteUpdateNote("office", now.Add(time.Hour)); j != nil {
		t.Fatalf("updating, an hour on: %+v", j)
	}
}
