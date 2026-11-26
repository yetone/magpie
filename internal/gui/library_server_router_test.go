package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// A server's row in the Library (MetalHqa on X) puts it through the MCP
// router or keeps it each agent's own entry, saved as the editor's switch
// saves it (Server.Hub), and the page's view says so at once; the rest of
// the server is kept as it was.
func TestServerRouterFromRow(t *testing.T) {
	sandboxHome(t)
	h := Handler(nil, nil)
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	if rec := post("/api/library/servers/save", `{"old":"","server":{"name":"index","transport":"stdio","command":"/opt/index/mcp","note":"mine"}}`); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	hub := func(on bool) (string, library.Server) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"name": "index", "on": on})
		rec := post("/api/library/servers/router", string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("router %v: %d %s", on, rec.Code, rec.Body)
		}
		// a change to the servers alone answers with their part of the page
		var a struct {
			Part string
			MCP  struct{ Servers []library.Server }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		v := a.MCP
		if a.Part != "mcp" || len(v.Servers) != 1 {
			t.Fatalf("servers in the view (%q): %+v", a.Part, v.Servers)
		}
		// read again from disk, as the next page load reads it
		get := httptest.NewRecorder()
		h.ServeHTTP(get, httptest.NewRequest("GET", "/api/library", nil))
		var again struct{ Servers []library.Server }
		if err := json.Unmarshal(get.Body.Bytes(), &again); err != nil || len(again.Servers) != 1 {
			t.Fatalf("read again: %v %s", err, get.Body)
		}
		return v.Servers[0].Hub, again.Servers[0]
	}
	if view, saved := hub(false); view != library.HubDirect || saved.Hub != library.HubDirect {
		t.Fatalf("off: view %q, saved %q", view, saved.Hub)
	}
	view, saved := hub(true)
	if view != "" || saved.Hub != "" {
		t.Fatalf("on: view %q, saved %q", view, saved.Hub)
	}
	if saved.Command != "/opt/index/mcp" || saved.Note != "mine" {
		t.Fatalf("the rest of the server changed: %+v", saved)
	}
	if rec := post("/api/library/servers/router", `{"name":"gone","on":false}`); rec.Code == http.StatusOK {
		t.Fatalf("no such server: %d %s", rec.Code, rec.Body)
	}
}
