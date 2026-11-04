package gui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// browserDown is a desktop whose browser doesn't start, as Windows'
// rundll32 under a PATHEXT of ".CPL" (#2043, xiaobaifly7).
type browserDown struct{ webHost }

func (browserDown) OpenURL(string) error {
	return errors.New(`exec: "rundll32": executable file not found in %PATH%`)
}

// A sign-in to an MCP server whose page the browser didn't open says so
// with the page's link, still waiting for it, instead of saying to finish
// in the browser and timing out ten minutes later (#2043). Opened, it says
// nothing of it.
func TestMCPSignInSaysTheBrowserDidntOpen(t *testing.T) {
	sandboxHome(t)
	f := mcpauthtest.New(t)
	if _, err := library.SaveServer("", library.Server{Name: "cloudflare-api", Transport: "http", URL: f.URL}); err != nil {
		t.Fatal(err)
	}
	start := func(w Windows) (st struct{ ID, URL, State, OpenError string }) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(w, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/library/mcp-signin", strings.NewReader(`{"name":"cloudflare-api"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mcpauth.Cancel(st.ID) })
		return st
	}
	st := start(browserDown{})
	if st.State != "waiting" || !strings.HasPrefix(st.URL, f.Server.URL) {
		t.Fatalf("not waiting on its page: %+v", st)
	}
	if !strings.Contains(st.OpenError, "rundll32") {
		t.Fatalf("the browser's not starting isn't said: %+v", st)
	}
	if st := start(webHost{}); st.OpenError != "" || st.URL == "" {
		t.Fatalf("a browser that opened: %+v", st)
	}
}

// Open again on a sign-in says why the browser didn't open, where it said
// nothing (#2043).
func TestOpenSaysTheBrowserDidntOpen(t *testing.T) {
	sandboxHome(t)
	rec := httptest.NewRecorder()
	Handler(browserDown{}, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/open", strings.NewReader(`{"url":"https://example.invalid/authorize"}`)))
	if rec.Code == http.StatusNoContent || !strings.Contains(rec.Body.String(), "rundll32") {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	webPageHandler(t, webHost{}, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/open", strings.NewReader(`{"url":"https://example.invalid/authorize"}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("an open that worked: %d %s", rec.Code, rec.Body)
	}
}
