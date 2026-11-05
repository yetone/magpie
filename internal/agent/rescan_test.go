package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	syncatomic "sync/atomic"
	"testing"
	"time"
)

// The Agents page's refresh looks again at once (#844): Cursor Private
// Inference installed after magpie last looked used to wait out the half
// minute its apps folder was kept for, and a WSL distro's agents a minute.
func TestRescanLooksAgain(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("LOCALAPPDATA", h)
	t.Setenv("ProgramFiles", filepath.Join(h, "none"))
	cursorLocalSeen.Lock()
	cursorLocalSeen.at = time.Time{}
	cursorLocalSeen.Unlock()
	t.Cleanup(Rescan)

	var res string
	switch runtime.GOOS {
	case "darwin":
		res = filepath.Join(h, "Applications", "Cursor Private Inference.app", "Contents", "Resources", "app")
	case "windows":
		res = filepath.Join(h, "Programs", "cursor-private", "resources", "app")
	default:
		res = filepath.Join(h, "Applications", "cursor-private", "resources", "app")
	}
	if got := cursorLocalApp(); got != "" {
		t.Skipf("a Cursor Private Inference is installed here already: %s", got)
	}
	if err := os.MkdirAll(res, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res, "product.json"), []byte(`{"nameShort":"Cursor Private Inference","applicationName":"cursor"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := cursorLocalApp(); got != "" {
		t.Fatalf("looked again before the half minute was out: %s", got)
	}
	Rescan()
	if got := cursorLocalApp(); got == "" {
		t.Fatal("the refresh didn't look again: Cursor Private Inference just installed isn't found")
	}

	wsl.Lock()
	wsl.at = time.Now()
	wsl.failed = map[string]time.Time{"Ubuntu": time.Now()}
	wsl.Unlock()
	almaRead.Lock()
	almaRead.at = map[string]time.Time{"x": time.Now()}
	almaRead.Unlock()
	Rescan()
	wsl.Lock()
	at, failed := wsl.at, len(wsl.failed)
	wsl.Unlock()
	if !at.IsZero() || failed != 0 {
		t.Errorf("WSL's distros aren't asked again: at %v, %d failed kept", at, failed)
	}
	almaRead.Lock()
	n := len(almaRead.at)
	almaRead.Unlock()
	if n != 0 {
		t.Errorf("Alma's answers are kept: %d", n)
	}
}

// The refresh asks where updates come from again, too: a version published
// after magpie started stayed invisible to its own re-detect until a
// restart, the newest kept for hours and nothing asking anew (#4980). An
// ask that fails after the refresh keeps the newest known before it.
func TestRescanAsksLatestAgain(t *testing.T) {
	var asks syncatomic.Int32
	var newest syncatomic.Value
	newest.Store("1.4.0")
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		v := newest.Load().(string)
		if v == "" {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"version": v})
	}))
	t.Cleanup(reg.Close)
	oldReg := npmRegistry
	npmRegistry = reg.URL + "/"
	t.Cleanup(func() { npmRegistry = oldReg; latests = memo{} })
	latests = memo{}

	u := &updater{via: "npm", pkg: "@earendil-works/pi-coding-agent"}
	if got := latestVersion(u); got != "1.4.0" {
		t.Fatalf("latest %q", got)
	}
	newest.Store("1.5.0") // published after magpie asked
	if got := latestVersion(u); got != "1.4.0" || asks.Load() != 1 {
		t.Fatalf("latest %q, npm asked %d times for one kept answer", got, asks.Load())
	}
	Rescan()
	if got := latestVersion(u); got != "1.5.0" || asks.Load() != 2 {
		t.Fatalf("after the refresh: latest %q, npm asked %d times", got, asks.Load())
	}
	newest.Store("") // npm down at the next refresh
	Rescan()
	if got := latestVersion(u); got != "1.5.0" || asks.Load() != 3 {
		t.Fatalf("a failed ask after the refresh: latest %q, npm asked %d times", got, asks.Load())
	}
}
