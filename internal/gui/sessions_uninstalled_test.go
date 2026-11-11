package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// _SekiBetu_ on X: OpenCode was uninstalled and its sessions stayed on the
// management page with a Resume and no Delete. With its command gone, the
// page lists them with a Delete that takes them off the list (OpenCode's
// files are not touched) and no Resume; Restore in the trash lists them
// again. With the command there, as before: no Delete.
func TestManageDeleteOfAnUninstalledAgentTakesItOffTheList(t *testing.T) {
	h := sandboxHome(t)
	t.Setenv("XDG_DATA_HOME", "")
	if err := os.CopyFS(filepath.Join(h, ".local", "share", "opencode"), os.DirFS("../sessions/testdata/opencode")); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	mux := http.NewServeMux()
	sessionManageRoutes(mux, folderOnly{})
	type manage struct {
		Agents []struct {
			Agent     string `json:"agent"`
			Deletable bool   `json:"deletable"`
		} `json:"agents"`
		Sessions []struct {
			ID        string `json:"id"`
			Resume    string `json:"resume"`
			Deletable bool   `json:"deletable"`
			Forget    bool   `json:"forget"`
		} `json:"sessions"`
		Trash []struct {
			Key    string `json:"key"`
			Forgot bool   `json:"forgot"`
		} `json:"trash"`
	}
	call := func(method, path, body string) (m manage) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	m := call("GET", "/api/sessions/manage?agent=opencode", "")
	// the fixtures' two: one with a conversation, one nothing was said in
	if len(m.Sessions) != 2 || m.Sessions[0].Deletable || m.Sessions[0].Resume == "" {
		t.Fatalf("installed: %+v", m.Sessions)
	}
	id := m.Sessions[0].ID
	other := m.Sessions[1].ID

	t.Cleanup(sessions.ForceCLIGone("opencode"))
	m = call("GET", "/api/sessions/manage?agent=opencode", "")
	if len(m.Sessions) != 2 || !m.Sessions[0].Deletable || !m.Sessions[0].Forget || m.Sessions[0].Resume != "" || !m.Agents[0].Deletable {
		t.Fatalf("uninstalled: %+v %+v", m.Agents, m.Sessions)
	}
	call("POST", "/api/sessions/delete", `{"agent":"opencode","ids":["`+id+`"]}`)
	m = call("GET", "/api/sessions/manage?agent=opencode", "")
	if len(m.Sessions) != 1 || m.Sessions[0].ID != other || len(m.Trash) != 1 || !m.Trash[0].Forgot {
		t.Fatalf("after delete: %+v trash %+v", m.Sessions, m.Trash)
	}
	call("POST", "/api/sessions/restore", `{"key":"`+m.Trash[0].Key+`"}`)
	if m = call("GET", "/api/sessions/manage?agent=opencode", ""); len(m.Sessions) != 2 {
		t.Fatalf("after restore: %+v", m.Sessions)
	}
}
