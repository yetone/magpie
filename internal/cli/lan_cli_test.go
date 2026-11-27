package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// magpie lan on|off is Settings' Share on local network from a terminal
// (rael_tao on X, a server with no window): it saves the setting the
// serving gateway follows, says when MAGPIE_ADDR keeps the gateway on the
// network all the same, and a value it doesn't know writes nothing.
func TestLanCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MAGPIE_ADDR", "")

	out, err := stdoutOf(t, func() error { return lanCmd([]string{"on"}) })
	if err != nil || !settings.Load().LAN || !strings.Contains(out, "Share on local network: on") {
		t.Fatalf("on: %v %v %q", err, settings.Load().LAN, out)
	}
	out, err = stdoutOf(t, func() error { return lanCmd([]string{"off"}) })
	if err != nil || settings.Load().LAN || !strings.Contains(out, "Share on local network: off") {
		t.Fatalf("off: %v %v %q", err, settings.Load().LAN, out)
	}
	if strings.Contains(out, "MAGPIE_ADDR") {
		t.Errorf("MAGPIE_ADDR named while unset: %q", out)
	}
	for _, bad := range [][]string{{"--help"}, {"yes"}, {"on", "off"}} {
		if _, err := stdoutOf(t, func() error { return lanCmd(bad) }); err == nil || !strings.Contains(err.Error(), "usage: magpie lan") {
			t.Errorf("%q: %v, want the usage", bad, err)
		}
		if settings.Load().LAN {
			t.Fatalf("%q turned sharing on", bad)
		}
	}
	// Docker's image: on the network whatever the setting says, and said so
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	out, err = stdoutOf(t, func() error { return lanCmd(nil) })
	if err != nil || !strings.Contains(out, "MAGPIE_ADDR=0.0.0.0:3425 has the gateway listen on the network") || !strings.Contains(out, "127.0.0.1:3425") {
		t.Fatalf("MAGPIE_ADDR on the network: %v %q", err, out)
	}
}
