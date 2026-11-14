package netproxy

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeGsettings puts a gsettings on PATH that prints out, or fails with
// stderr and code when code isn't 0; an empty dir is PATH with none.
func fakeGsettings(t *testing.T, out, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	if out != "" || code != 0 {
		script := "#!/bin/sh\ncat <<'MAGPIE_EOF'\n" + out + "MAGPIE_EOF\n"
		if code != 0 {
			script = "#!/bin/sh\necho '" + stderr + "' >&2\nexit " + string(rune('0'+code)) + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "gsettings"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if out != "" || code != 0 {
		// the fake comes first; /bin and /usr/bin for its cat
		t.Setenv("PATH", dir+":/bin:/usr/bin")
		return
	}
	t.Setenv("PATH", dir)
}

// #4147: Auto reads GNOME's proxy where Clash Verge set it, with nothing in
// magpie's environment.
func TestSystemReadsGnome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CURRENT_DESKTOP", "ubuntu:GNOME")
	fakeGsettings(t, gnomeClash, "", 0)
	if p, ok := system(); !ok || p.URL != "http://127.0.0.1:7897" {
		t.Fatalf("%+v %v", p, ok)
	}
}

func TestSystemReadsKDE(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.WriteFile(filepath.Join(cfg, "kioslaverc"), []byte(kdeClash), 0o600); err != nil {
		t.Fatal(err)
	}
	// a KDE session: kioslaverc, whatever GNOME's says
	t.Setenv("XDG_CURRENT_DESKTOP", "KDE")
	fakeGsettings(t, "org.gnome.system.proxy mode 'none'\n", "", 0)
	if p, ok := system(); !ok || p.URL != "http://127.0.0.1:7897" {
		t.Fatalf("KDE session: %+v %v", p, ok)
	}
	// another session with no gsettings: kioslaverc all the same
	t.Setenv("XDG_CURRENT_DESKTOP", "XFCE")
	fakeGsettings(t, "", "", 0)
	if p, ok := system(); !ok || p.URL != "http://127.0.0.1:7897" {
		t.Fatalf("no gsettings: %+v %v", p, ok)
	}
}

// No GNOME schema and no kioslaverc is no proxy, read; a gsettings that
// fails otherwise is a failed read, which keeps the last good one.
func TestSystemWithoutDesktopProxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CURRENT_DESKTOP", "XFCE")
	fakeGsettings(t, "", "", 0)
	if p, ok := system(); !ok || p.URL != "" {
		t.Fatalf("no gsettings: %+v %v", p, ok)
	}
	fakeGsettings(t, "", "No such schema “org.gnome.system.proxy”", 1)
	if p, ok := system(); !ok || p.URL != "" {
		t.Fatalf("no schema: %+v %v", p, ok)
	}
	fakeGsettings(t, "", "dconf: something broke", 1)
	if _, ok := system(); ok {
		t.Fatal("a failed gsettings read as none")
	}
}
