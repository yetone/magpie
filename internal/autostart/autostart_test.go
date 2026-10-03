package autostart

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// On, the system's record names this program and the tray; off, it's gone,
// and turning it off again is no error.
func TestSet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Run key is the real user's")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("APPIMAGE", "/opt/my apps/magpie.AppImage")
	if Enabled() {
		t.Fatal("on before it was turned on")
	}
	if err := Set(true); err != nil {
		t.Fatal(err)
	}
	if !Enabled() {
		t.Fatal("not on after Set(true)")
	}
	path := record()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	arg := Arg
	if runtime.GOOS == "android" {
		arg = "serve"
	}
	for _, want := range []string{"/opt/my apps/magpie.AppImage", arg} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %q:\n%s", path, want, b)
		}
	}
	if err := Set(false); err != nil || Enabled() {
		t.Fatalf("still on after Set(false): %v", err)
	}
	if err := Set(false); err != nil {
		t.Fatal(err)
	}
}
