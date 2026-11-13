package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// An account's sign-in page is left out of the default browser when the
// user keeps sign-in pages to a browser of their own (max2020 on Discord:
// signing in from a separate, fingerprint browser, the default one never
// reaching the vendor). The sign-in still starts and gives its link, said
// to be unopened; with the setting off, as by default, the page opens as
// before. Both the built-in sign-ins and a plugin's.
func TestSignInLinkOnlyOpensNothing(t *testing.T) {
	bun := testenv.Bun(t)
	dir := t.TempDir()
	testenv.SetHome(t, dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	t.Cleanup(plugin.Settle)
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(t.Context(), abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(t.Context()); err != nil {
		t.Fatal(err)
	}
	type opened struct {
		provider.SignInState
		LinkOnly bool `json:"linkOnly"`
	}
	start := func(path string, body any) (int, opened) {
		t.Helper()
		w := &openCounter{}
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		Handler(w, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, bytes.NewReader(b)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		var st opened
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { provider.CancelSignIn(st.ID) })
		w.mu.Lock()
		defer w.mu.Unlock()
		return len(w.opened), st
	}
	ways := []struct {
		name, path string
		body       any
	}{
		{"built-in", "/api/signin", map[string]any{"agent": "gemini"}},
		{"plugin", "/api/plugin-signin", map[string]any{"provider": "fakeco", "method": 1, "inputs": map[string]string{"where": "work", "team": "blue"}}},
	}
	for _, linkOnly := range []bool{true, false} {
		s := settings.Load()
		s.SignInLinkOnly = linkOnly
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		for _, way := range ways {
			n, st := start(way.path, way.body)
			if !strings.HasPrefix(st.URL, "http") || st.State != "waiting" {
				t.Fatalf("%s, link only %v: no link to copy: %+v", way.name, linkOnly, st)
			}
			if linkOnly && (n != 0 || !st.LinkOnly) {
				t.Fatalf("%s: kept to its link, its page was opened %d times (linkOnly %v)", way.name, n, st.LinkOnly)
			}
			if !linkOnly && (n != 1 || st.LinkOnly) {
				t.Fatalf("%s: by default its page was opened %d times (linkOnly %v)", way.name, n, st.LinkOnly)
			}
		}
	}
}

// An MCP server's OAuth sign-in from the Library is kept to its link too
// when the user keeps sign-in pages to a browser of their own (hkxhkxhkx on
// X: it opened the default browser whatever the setting). It still starts
// and gives its link; with the setting off, as by default, it opens once.
func TestMCPSignInLinkOnlyOpensNothing(t *testing.T) {
	sandboxHome(t)
	f := mcpauthtest.New(t)
	if _, err := library.SaveServer("", library.Server{Name: "cloudflare-api", Transport: "http", URL: f.URL}); err != nil {
		t.Fatal(err)
	}
	for _, linkOnly := range []bool{true, false} {
		s := settings.Load()
		s.SignInLinkOnly = linkOnly
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		w := &openCounter{}
		rec := httptest.NewRecorder()
		Handler(w, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/library/mcp-signin", strings.NewReader(`{"name":"cloudflare-api"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
		}
		var st struct {
			ID, URL, State string
			LinkOnly       bool `json:"linkOnly"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		mcpauth.Cancel(st.ID)
		if st.State != "waiting" || !strings.HasPrefix(st.URL, f.Server.URL) {
			t.Fatalf("link only %v: not waiting on its page: %+v", linkOnly, st)
		}
		w.mu.Lock()
		n := len(w.opened)
		w.mu.Unlock()
		if linkOnly && (n != 0 || !st.LinkOnly) {
			t.Fatalf("kept to its link, its page was opened %d times (linkOnly %v)", n, st.LinkOnly)
		}
		if !linkOnly && (n != 1 || st.LinkOnly) {
			t.Fatalf("by default its page was opened %d times (linkOnly %v)", n, st.LinkOnly)
		}
	}
}
