//go:build linux && cgo && gtk3 && !nogui

package gui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFontDPIUnset builds testdata/fontdpi_probe.c with the guard the app
// runs (fontdpi_gtk3.h) and opens a real WebKitGTK page with GDK's font
// DPI unset, as on #1371's NixOS/Niri desktop. Without the guard WebKitGTK
// 2.54.1 lays it out at viewport -40320 and font 9000000px. A positive DPI
// is the user's own and is kept. It needs a display (Xvfb will do).
func TestFontDPIUnset(t *testing.T) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("needs a display")
	}
	flags, err := exec.Command("pkg-config", "--cflags", "--libs", "gtk+-3.0", "webkit2gtk-4.1").Output()
	if err != nil {
		t.Skip("no GTK 3 / WebKitGTK 4.1 headers:", err)
	}
	here, _ := filepath.Abs(".")
	bin := filepath.Join(t.TempDir(), "probe")
	args := append([]string{"testdata/fontdpi_probe.c", "-DMAGPIE_FONT_DPI", "-I" + here, "-o", bin}, strings.Fields(string(flags))...)
	if out, err := exec.Command("cc", args...).CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	home := t.TempDir()
	for _, c := range []struct {
		name, dpi, scale string
		want             float64
	}{
		{"unset", "-1", "", 96},
		{"unset, font scale", "-1", "1.25", 120},
		{"unset, bad font scale", "-1", "nan", 96},
		{"the user's own", "144", "", 144},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, c.dpi)
			cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
				"XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "XDG_DATA_HOME="+filepath.Join(home, ".local/share"),
				"GSETTINGS_BACKEND=memory", "GDK_DPI_SCALE="+c.scale, "WEBKIT_DISABLE_COMPOSITING_MODE=1")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("probe: %v\n%s", err, out)
			}
			var dpi, viewport, font float64
			var allocated int
			for _, l := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(l, "layout ") {
					fmt.Sscanf(l, "layout dpi=%g allocated=%d viewport=%g font=%g", &dpi, &allocated, &viewport, &font)
				}
			}
			t.Logf("%s", out)
			if dpi != c.want {
				t.Errorf("font DPI %g, want %g", dpi, c.want)
			}
			if viewport <= 0 || font != 16 {
				t.Errorf("page laid out at viewport %g, body font %gpx; want a positive viewport and 16px", viewport, font)
			}
			if want := float64(allocated) * 96 / c.want; viewport < want-2 || viewport > want+2 {
				t.Errorf("viewport %g, want %g for %dpx at %g DPI", viewport, want, allocated, c.want)
			}
		})
	}
}
