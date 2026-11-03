package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/daemon"
)

// zaidu259434 on X: magpie in WSL, its agents failed once magpie tui was
// closed, nothing serving the gateway. The terminal quitting with an agent
// connected hands the gateway to magpie's service, installing it where
// systemd runs it and the user never removed it, starting it where it is
// installed, else to a magpie serve in the background. Off Linux, with
// nothing connected, or where the app opens at login, it only says the
// gateway stopped.
func TestKeepGatewayAsTheTerminalQuits(t *testing.T) {
	was := keep
	t.Cleanup(func() { keep = was })
	type did struct{ installed, started, bg bool }
	setup := func(goos string, auto, installed, removed bool, installErr error) *did {
		d := &did{}
		keep = keeper{
			goos:        goos,
			autostartOn: func() bool { return auto },
			status:      func() (daemon.Status, error) { return daemon.Status{Installed: installed}, nil },
			removed:     func() bool { return removed },
			start:       func() error { d.started = true; return nil },
			install:     func() error { d.installed = true; return installErr },
			linger:      func() string { return "" },
			background:  func() (int, string, error) { d.bg = true; return 42, "/x/serve.log", nil },
		}
		return d
	}
	gone := gatewayGoneNote()

	d := setup("linux", false, false, false, nil)
	if msg := keepGateway(true); !d.installed || d.bg || !strings.Contains(msg, "magpie daemon uninstall") {
		t.Errorf("no service: %+v %q", *d, msg)
	}
	d = setup("linux", false, true, false, nil)
	if msg := keepGateway(true); !d.started || d.installed || d.bg || msg == gone {
		t.Errorf("installed: %+v %q", *d, msg)
	}
	d = setup("linux", false, false, true, nil)
	if msg := keepGateway(true); d.installed || !d.bg || !strings.Contains(msg, "pid 42") {
		t.Errorf("removed by the user: %+v %q", *d, msg)
	}
	d = setup("linux", false, false, false, errors.New("this system has no systemd"))
	if msg := keepGateway(true); !d.bg || !strings.Contains(msg, "background") {
		t.Errorf("no systemd: %+v %q", *d, msg)
	}
	for name, c := range map[string]struct {
		goos      string
		auto, con bool
	}{
		"mac":        {"darwin", false, true},
		"nothing on": {"linux", false, false},
	} {
		d = setup(c.goos, c.auto, false, false, nil)
		if msg := keepGateway(c.con); msg != gone || d.installed || d.bg || d.started {
			t.Errorf("%s: %+v %q", name, *d, msg)
		}
	}
	// magpie opens at login: the gateway is left to the app, and the
	// note says how to have it handed on where no desktop logs in, as
	// with a package's entry on NixOS-WSL (#3756)
	d = setup("linux", true, false, false, nil)
	if msg := keepGateway(true); d.installed || d.bg || d.started || !strings.HasPrefix(msg, gone) || !strings.Contains(msg, "magpie autostart off") {
		t.Errorf("app at login: %+v %q", *d, msg)
	}
}
