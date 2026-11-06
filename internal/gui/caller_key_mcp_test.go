package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/library"
)

// The Gateway page lists the MCP servers a key can be held to, magpie's
// own and then the Library's, each with its tools, and keeps what was
// picked for it (wakaka on Discord).
func TestCallerKeyMCPRoutes(t *testing.T) {
	sandboxHome(t)
	if _, err := library.SaveServer("", library.Server{Name: "github", Transport: "http", URL: "http://127.0.0.1:1/mcp"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	var l struct{ Servers []keyMCPJSON }
	if w := request("GET", "/api/caller-keys/mcp", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal(w.Code, w.Body)
	}
	var names []string
	for _, s := range l.Servers {
		names = append(names, s.Name)
		if s.Tools == nil {
			t.Fatalf("%s: tools null", s.Name)
		}
		if s.Name == "magpie-browser" && (!s.Own || !slices.Equal(s.Tools, []string{"browser"})) {
			t.Fatalf("browser %+v", s)
		}
		if s.Name == "github" && s.Own {
			t.Fatalf("github %+v", s)
		}
	}
	if len(names) < 2 || names[len(names)-1] != "github" || !slices.Contains(names, "magpie-browser") {
		t.Fatal("servers", names)
	}
	var s struct{ Keys []access.Key }
	if w := request("POST", "/api/caller-keys/add-key", `{"name":"Phone"}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := s.Keys[0].ID
	if w := request("POST", "/api/caller-keys/mcp-key", `{"key":"`+id+`","mcp":["github/search_code","magpie-browser"]}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if !slices.Equal(s.Keys[0].MCP, []string{"github/search_code", "magpie-browser"}) {
		t.Fatal("kept", s.Keys[0].MCP)
	}
	if w := request("POST", "/api/caller-keys/mcp-key", `{"key":"`+id+`","mcp":["github/"]}`); w.Code == 200 {
		t.Fatal("took a bad entry", w.Body)
	}
	// a fresh value: an omitted mcp would keep the last decode's
	s = struct{ Keys []access.Key }{}
	if w := request("POST", "/api/caller-keys/mcp-key", `{"key":"`+id+`","mcp":[]}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil || len(s.Keys[0].MCP) != 0 {
		t.Fatal(w.Code, w.Body)
	}
}
