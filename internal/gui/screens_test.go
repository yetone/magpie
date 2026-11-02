//go:build !nogui

package gui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// screensWndProc (screens_windows.go) answers the display changes Wails
// reads the screens again for, on Wails' main-thread window, before Wails
// sees them. An upgrade that renames that window, asks the interceptor
// after its own read, or reads the screens on another message fails here.
func TestWailsScreensAreReadOnTheMainThreadWindow(t *testing.T) {
	// go list -m names no folder for a module not yet unpacked, as on CI;
	// go mod download unpacks it and names its folder
	out, err := exec.Command("go", "mod", "download", "-json", "github.com/wailsapp/wails/v3").Output()
	if err != nil {
		t.Skip("go mod download:", err)
	}
	var mod struct{ Dir string }
	if err := json.Unmarshal(out, &mod); err != nil || mod.Dir == "" {
		t.Fatalf("go mod download named no folder: %s", out)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(mod.Dir, "pkg", "application", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if !strings.Contains(read("mainthread_windows.go"), `w32.MustStringToUTF16Ptr("`+wailsMainThread+`")`) {
		t.Fatalf("Wails' main-thread window is no longer titled %q", wailsMainThread)
	}
	fn := regexp.MustCompile(`(?s)func \(m \*windowsApp\) wndProc\(.*?\n}\n`).FindString(read("application_windows.go"))
	if fn == "" {
		t.Fatal("Wails has no (*windowsApp).wndProc")
	}
	asked := strings.Index(fn, "m.parent.options.Windows.WndProcInterceptor(hwnd, msg, wParam, lParam)")
	reads := strings.Index(fn, "m.processAndCacheScreens()")
	if asked < 0 || reads < 0 || asked > reads || strings.Count(fn, "processAndCacheScreens") != 1 {
		t.Fatalf("Wails' wndProc no longer asks the interceptor before its one screens read:\n%s", fn)
	}
	if !strings.Contains(fn, "if msg == w32.WM_DISPLAYCHANGE || (msg == w32.WM_SETTINGCHANGE && wParam == w32.SPI_SETWORKAREA) {\n\t\t\terr := m.processAndCacheScreens()") {
		t.Fatalf("Wails reads the screens on other messages than screensChanged's:\n%s", fn)
	}
}
