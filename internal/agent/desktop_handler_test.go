package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// geddy (11-03, Omarchy): xdg-mime named cindy.desktop for cindy://,
// but no such entry was there, so xdg-open fell back to the browser and
// a blank Chrome page opened instead of Cindy. The handler is only
// found when its entry and the entry's program are there.
func TestDesktopHandlerNeedsItsEntryAndProgram(t *testing.T) {
	home, sys := t.TempDir(), t.TempDir()
	prog := filepath.Join(t.TempDir(), "Cindy.AppImage")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir, rel, exec string) {
		p := filepath.Join(dir, "applications", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("[Desktop Entry]\nType=Application\nName=Cindy\nExec="+exec+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(sys, "cindy.desktop", `"`+prog+`" %U`)
	write(home, "gone.desktop", filepath.Join(t.TempDir(), "moved", "Cindy.AppImage")+" %u")
	write(sys, "vendor/cindy.desktop", prog+" %u")
	dirs := []string{home, sys}

	exe, found, err := desktopHandler("cindy.desktop", dirs)
	if err != nil || !found || exe != prog || gone(exe) {
		t.Errorf("an entry there: exe %q found %v err %v, want %q", exe, found, err, prog)
	}
	exe, found, err = desktopHandler("vendor-cindy.desktop", dirs)
	if err != nil || !found || exe != prog {
		t.Errorf("an entry in a subfolder: exe %q found %v err %v, want %q", exe, found, err, prog)
	}
	exe, found, err = desktopHandler("missing.desktop", dirs)
	if err != nil || !found || !gone(exe) {
		t.Errorf("no entry: exe %q found %v err %v, want a path that isn't there", exe, found, err)
	}
	exe, found, err = desktopHandler("gone.desktop", dirs)
	if err != nil || !found || !gone(exe) {
		t.Errorf("program moved: exe %q found %v err %v, want its old path", exe, found, err)
	}
	// and OpenAppLink then says so instead of handing the link to the browser
	old := linkHandler
	t.Cleanup(func() { linkHandler = old })
	linkHandler = func(string) (string, bool, error) { return desktopHandler("missing.desktop", dirs) }
	opened := false
	if err := OpenAppLink(CindyLink("http://127.0.0.1:3425"), func(string) error { opened = true; return nil }); err == nil || opened {
		t.Errorf("no entry: err %v opened %v, want an error and nothing opened", err, opened)
	}
}
