package gui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/library"
)

// folderOnly is the app's windows as far as an export needs them.
type folderOnly struct{ Windows }

func (folderOnly) OpenFolder(string) error { return nil }

// sandboxHome gives the test a machine of its own: no agents, no magpie files.
func sandboxHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", "")
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	t.Setenv("APPDATA", "")
	t.Setenv("LOCALAPPDATA", "")
	return h
}

// The Settings page's Export and Import carry the library, and leave it
// when its box is unticked.
func TestBackupLibraryRoundTrip(t *testing.T) {
	h := sandboxHome(t)
	text := "Be brief."
	if _, err := library.SaveInstructions(library.InstructionsChange{Shared: &text}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	backupRoutes(mux, folderOnly{})
	post := func(path string, body any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(string(b))))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	export := func(lib bool) []byte {
		t.Helper()
		out := post("/api/backup/export", map[string]any{"pass": "pw", "keys": true, "library": lib})
		if out["library"] != lib {
			t.Fatalf("export: %v", out)
		}
		p, _ := out["path"].(string)
		data, err := os.ReadFile(filepath.Join(h, strings.TrimPrefix(p, "~")))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	with, without := export(true), export(false)

	sandboxHome(t) // another machine
	enc := base64.StdEncoding.EncodeToString
	if out := post("/api/backup/import", map[string]any{"data": enc(without), "pass": "pw", "agents": true, "library": true}); out["Library"] != false {
		t.Fatalf("a file without it: %v", out)
	}
	if out := post("/api/backup/import", map[string]any{"data": enc(with), "pass": "pw", "agents": true, "library": false}); out["Library"] != false {
		t.Fatalf("unticked: %v", out)
	}
	if iv, _ := library.ReadInstructions(); iv.Shared != "" {
		t.Fatalf("the library came in unticked: %q", iv.Shared)
	}
	if out := post("/api/backup/import", map[string]any{"data": enc(with), "pass": "pw", "agents": true, "library": true}); out["Library"] != true {
		t.Fatalf("import: %v", out)
	}
	if iv, _ := library.ReadInstructions(); iv.Shared != "Be brief." {
		t.Fatalf("instructions: %q", iv.Shared)
	}
}

// 1Shin on Discord: an export joined $HOME/Downloads. On a Linux desktop in
// Chinese, Downloads is ~/下载 (user-dirs.dirs); the export goes there, and
// to home\Downloads when the folder named there isn't one.
func TestExportGoesToTheSystemsDownloads(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("user-dirs.dirs is read on Linux and the BSDs (appdir's tests fake Windows' known folder)")
	}
	h := sandboxHome(t)
	os.MkdirAll(filepath.Join(h, ".config"), 0o700)
	os.WriteFile(filepath.Join(h, ".config", "user-dirs.dirs"), []byte("XDG_DOWNLOAD_DIR=\"$HOME/下载\"\n"), 0o600)
	mux := http.NewServeMux()
	backupRoutes(mux, folderOnly{})
	export := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/backup/export", strings.NewReader(`{"pass":"pw"}`)))
		if w.Code != 200 {
			t.Fatalf("export: %d %s", w.Code, w.Body)
		}
		var out struct{ Path string }
		json.Unmarshal(w.Body.Bytes(), &out)
		return filepath.Dir(filepath.Join(h, strings.TrimPrefix(out.Path, "~")))
	}
	os.MkdirAll(filepath.Join(h, "Downloads"), 0o700)
	if got, want := export(), filepath.Join(h, "Downloads"); got != want {
		t.Errorf("~/下载 not made yet: saved in %s, want %s", got, want)
	}
	os.MkdirAll(filepath.Join(h, "下载"), 0o700)
	if got, want := export(), filepath.Join(h, "下载"); got != want {
		t.Errorf("saved in %s, want %s", got, want)
	}
}

// pickFolder is the app's windows with the system's folder picker answering
// dir.
type pickFolder struct {
	folderOnly
	dir string
}

func (p pickFolder) ChooseFolder(string) (string, error) { return p.dir, nil }

// Settings' Back up by itself (jwaitq on Discord): a change sends only what
// it changes, so the rest stays as the user set it; Change… takes a folder
// from the picker; a backup made now is listed, and restoring it says which
// backup of what was here undoes it.
func TestAutoBackupRoutes(t *testing.T) {
	sandboxHome(t)
	picked := t.TempDir()
	mux := http.NewServeMux()
	backupRoutes(mux, pickFolder{dir: picked})
	call := func(path, body string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		method := "POST"
		if body == "-" {
			method, body = "GET", ""
		}
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if v := call("/api/autobackup", "-"); v["on"] != true || v["hours"] != 24.0 || v["keep"] != 7.0 || v["custom"] != false {
		t.Fatalf("the defaults: %v", v)
	}
	call("/api/autobackup/set", `{"hours":168}`)
	call("/api/autobackup/set", `{"keep":14}`)
	if v := call("/api/autobackup/set", `{"on":false}`); v["on"] != false || v["hours"] != 168.0 || v["keep"] != 14.0 {
		t.Fatalf("off kept the rest? %v", v)
	}
	if v := call("/api/autobackup/choose", ""); v["custom"] != true || v["dir"] != tilde(picked) {
		t.Fatalf("chosen: %v", v)
	}
	v := call("/api/autobackup/now", "")
	list, _ := v["backups"].([]any)
	if len(list) != 1 {
		t.Fatalf("after Back up now: %v", v)
	}
	name := list[0].(map[string]any)["name"].(string)
	if _, err := os.Stat(filepath.Join(picked, name)); err != nil {
		t.Fatal(err)
	}
	r := call("/api/autobackup/restore", `{"name":"`+name+`"}`)
	before, _ := r["before"].(string)
	if !strings.HasSuffix(before, "-restore.json") || len(r["view"].(map[string]any)["backups"].([]any)) != 2 {
		t.Fatalf("restore: %v", r)
	}
	if v := call("/api/autobackup/set", `{"dir":""}`); v["custom"] != false || v["hours"] != 168.0 {
		t.Fatalf("back to the default folder: %v", v)
	}
}
