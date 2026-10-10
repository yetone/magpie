//go:build !darwin && !windows && !android

package autostart

import (
	"os"
	"strings"
	"testing"
)

// A record a write cut short is not one that opens magpie at login: the
// desktop starts no entry with no Exec, and none at all with nothing in it,
// so Open at login reads off rather than saying on for a record that never
// starts anything.
func TestSetThenTruncatedRecordReadsAsOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("APPIMAGE", "/opt/my apps/magpie.AppImage")
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	if !Enabled() {
		t.Fatal("not on after Set(true)")
	}
	p := record()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// the head of the entry, cut off before the Exec that names the
	// program, which is what a write that goes no further leaves behind
	head := b[:strings.Index(string(b), "Exec=")]
	if err := os.WriteFile(p, head, 0o644); err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Errorf("a desktop entry with no Exec reads as on:\n%s", head)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Error("an empty desktop entry reads as on")
	}
	// an Exec key with nothing after it names nothing to run either, and the
	// desktop starts no entry that names nothing
	if err := os.WriteFile(p, []byte("[Desktop Entry]\nType=Application\nName=magpie\nExec=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Error("a desktop entry with an empty Exec reads as on")
	}
}
