package gui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gateway"
)

// pickAndQuit is the app's windows with the picker answering dir, and Quit
// said on quit.
type pickAndQuit struct {
	pickFolder
	quit chan struct{}
}

func (p pickAndQuit) Quit() { p.quit <- struct{}{} }

// Settings' Data folder (CHENGJIAHEGOXIS and buhaodeshiyehao on X): Move…
// takes a folder from the picker, asks for the move and starts magpie again
// to make it, as the app quits; nothing is moved under the running one.
// With another magpie serving the gateway, it is asked for and nothing
// restarts. Cancel forgets it.
func TestDataFolderMoveRestarts(t *testing.T) {
	h := sandboxHome(t)
	os.MkdirAll(appdir.Config(), 0o700)
	os.WriteFile(filepath.Join(appdir.Config(), "settings.json"), []byte("{}"), 0o600)
	disk := filepath.Join(h, "ssd")
	os.MkdirAll(disk, 0o755)
	restarts := 0
	oldRestart, oldServes := restartSelf, servedBy
	t.Cleanup(func() { restartSelf, servedBy = oldRestart, oldServes })
	restartSelf = func() error { restarts++; return nil }
	other := false
	servedBy = func() gateway.Served {
		if other {
			return gateway.Served{Running: true, Pid: os.Getpid() + 1}
		}
		return gateway.Served{Running: true, Pid: os.Getpid()} // this magpie's own
	}
	win := pickAndQuit{pickFolder{dir: disk}, make(chan struct{}, 1)}
	mux := http.NewServeMux()
	dataFolderRoutes(mux, win)
	call := func(path, body string, code int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		method := "POST"
		if body == "-" {
			method, body = "GET", ""
		}
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != code {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if v := call("/api/datafolder", "-", 200); v["config"] != tilde(appdir.Config()) || v["moved"] != nil || v["pending"] != nil {
		t.Fatalf("at home: %v", v)
	}
	out := call("/api/datafolder/move", "", 200)
	view, _ := out["view"].(map[string]any)
	if out["restart"] != true || view["pending"] != tilde(filepath.Join(disk, appdir.MoveName)) || restarts != 1 {
		t.Fatalf("move: %v, %d restarts", out, restarts)
	}
	select {
	case <-win.quit:
	case <-time.After(5 * time.Second):
		t.Fatal("the app didn't quit for the restart")
	}
	if _, ok := os.Readlink(appdir.Config()); ok == nil {
		t.Fatal("moved under the running magpie")
	}
	if !appdir.MoveAsked() {
		t.Fatal("no move asked for")
	}
	call("/api/datafolder/cancel", "", 200)
	if appdir.MoveAsked() {
		t.Fatal("cancel left the move asked for")
	}

	// another magpie serves: asked for, no restart, said
	other = true
	out = call("/api/datafolder/move", `{"dir":`+jsonString(disk)+`}`, 200)
	view, _ = out["view"].(map[string]any)
	if out["restart"] != false || view["other"] != true || restarts != 1 {
		t.Fatalf("with another magpie serving: %v, %d restarts", out, restarts)
	}
	call("/api/datafolder/cancel", "", 200)

	// a folder with files in it is refused, before anything quits
	other = false
	os.MkdirAll(filepath.Join(disk, appdir.MoveName), 0o755)
	os.WriteFile(filepath.Join(disk, appdir.MoveName, "x"), []byte("x"), 0o600)
	call("/api/datafolder/move", "", 400)
	if appdir.MoveAsked() || restarts != 1 {
		t.Fatal("a refused folder asked for a move or restarted")
	}
	// a restart that can't start leaves nothing asked for
	os.RemoveAll(filepath.Join(disk, appdir.MoveName))
	restartSelf = func() error { return errors.New("can't start") }
	call("/api/datafolder/move", "", 400)
	if appdir.MoveAsked() {
		t.Fatal("a move is asked for with no restart to make it")
	}
	// home when home: refused
	call("/api/datafolder/home", "", 400)
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }
