package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// `magpie update` in the Docker image (/magpie, run as nonroot) failed with
// the rename's "permission denied"; it now says to pull the new image.
// Outside a container it goes on to the download as before.
func TestUpdateInContainerSaysPullTheImage(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user may not write to")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// no checksum: a download is refused before anything is fetched or written
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.11", URL: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.11",
			Assets: map[string]update.Asset{update.BinaryAsset(): {URL: "http://" + r.Host + "/asset"}}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	dir := t.TempDir()
	exe := filepath.Join(dir, "magpie")
	if err := os.WriteFile(exe, []byte("magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	wasV, wasX, wasE, wasC := version, updateExecutable, updateCanElevate, updateInContainer
	t.Cleanup(func() { version, updateExecutable, updateCanElevate, updateInContainer = wasV, wasX, wasE, wasC })
	version = "0.1.10"
	updateExecutable = func() (string, error) { return exe, nil }
	updateCanElevate = func() bool { return false }

	updateInContainer = func() bool { return true }
	err := updateCmd([]string{"update"})
	if err == nil || !strings.Contains(err.Error(), "docker pull ghcr.io/yetone/magpie:latest") {
		t.Fatalf("in a container: %v", err)
	}

	updateInContainer = func() bool { return false }
	if err := updateCmd([]string{"update"}); err == nil || strings.Contains(err.Error(), "docker pull") {
		t.Fatalf("outside one: %v", err)
	}
}
