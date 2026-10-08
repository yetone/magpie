package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// #1277: a magpie.exe kept at C:\ may not write there, so it can't replace
// itself. It still says a new version is out, and says why it opens the
// release page instead of updating, naming the folder.
func TestUpdateNotWritableSaysWhy(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user may not write to")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.11", Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset"},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	old := Version
	Version = "0.1.10"
	defer func() { Version = old }()
	was := canElevate
	defer func() { canElevate = was }()

	dir := t.TempDir()
	exe := filepath.Join(dir, "magpie.exe")
	if err := os.WriteFile(exe, []byte("magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)

	canElevate = func() bool { return false }
	u := &updater{}
	u.placeExe(exe)
	u.check()
	j := u.json()
	if j.State != "available" || j.Stuck != "not-writable" || j.StuckDir != dir {
		t.Fatalf("a binary in a folder magpie can't write to: %+v", j)
	}

	// where the system can ask for the password, it updates as before
	canElevate = func() bool { return true }
	u = &updater{}
	u.placeExe(exe)
	if u.stuck != "" || u.exe != exe {
		t.Fatalf("stuck %q, exe %q", u.stuck, u.exe)
	}
	// and a folder it may write to is never stuck
	canElevate = func() bool { return false }
	os.Chmod(dir, 0o755)
	u = &updater{}
	u.placeExe(exe)
	if u.stuck != "" || u.exe != exe {
		t.Fatalf("a writable folder: stuck %q, exe %q", u.stuck, u.exe)
	}
}
