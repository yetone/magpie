package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// The terminal build on a Mac (magpie-cli-darwin, which `magpie web` runs
// from) is a bare binary, in no .app. `magpie update` replaces it, but the
// page's updater only ever took a binary off the Mac, so its Update and
// Download opened the release page and nothing was downloaded. It now takes
// the binary as it does elsewhere; the desktop app's own binary copied out
// of its .app still isn't replaced by the terminal build.
func TestUpdateTakesTheMacTerminalBuild(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "magpie")
	if err := os.WriteFile(exe, []byte("magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	wasB, wasE, wasGUI := bundleOf, executable, update.GUI
	t.Cleanup(func() { bundleOf, executable, update.GUI = wasB, wasE, wasGUI })
	bundleOf = func() string { return "" }
	executable = func() (string, error) { return exe, nil }

	u := &updater{}
	u.place("darwin", false)
	if u.exe != exe || u.bundle != "" || u.stuck != "" {
		t.Fatalf("the Mac's terminal build: exe %q, bundle %q, stuck %q", u.exe, u.bundle, u.stuck)
	}

	// with a newer version out, it is downloaded rather than offered on the
	// release page: here the download fails, and a click tries it again
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.11", URL: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.11",
			Assets: map[string]update.Asset{
				update.BinaryAsset(): {URL: "http://" + r.Host + "/asset"}}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	old := Version
	Version = "0.1.10"
	defer func() { Version = old }()
	update.GUI = false
	u.check()
	if j := u.json(); j.State == "available" {
		t.Fatalf("the Mac's terminal build was sent to the release page: %+v", j)
	}

	// the desktop app's binary out of its .app: not the terminal build's to replace
	u = &updater{}
	u.place("darwin", true)
	if u.exe != "" || u.bundle != "" {
		t.Fatalf("the app's binary out of its .app: exe %q, bundle %q", u.exe, u.bundle)
	}
	// in its .app, the .app is what is replaced
	bundleOf = func() string { return filepath.Join(dir, "magpie.app") }
	u = &updater{}
	u.place("darwin", true)
	if u.bundle == "" || u.exe != "" {
		t.Fatalf("the app: exe %q, bundle %q", u.exe, u.bundle)
	}
	// off the Mac, either build takes its binary, as before
	bundleOf = func() string { return "" }
	for _, gui := range []bool{true, false} {
		u = &updater{}
		u.place("linux", gui)
		if u.exe != exe {
			t.Fatalf("linux gui=%v: exe %q", gui, u.exe)
		}
	}
}
