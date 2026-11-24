package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestImportIsReadOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	mux := http.NewServeMux()
	importRoutes(mux)
	get := func(id string) (*httptest.ResponseRecorder, importJSON) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/import/"+id, nil))
		var in importJSON
		_ = json.Unmarshal(rec.Body.Bytes(), &in)
		return rec, in
	}

	id := stashImport("magpie://import?preset=deepseek&key=sk-test")
	rec, in := get(id)
	if rec.Code != 200 || in.Provider.ID != "deepseek" || in.Provider.Key != "sk-test" || in.Error != "" {
		t.Fatalf("%d %+v", rec.Code, in)
	}
	if rec, _ := get(id); rec.Code != 404 {
		t.Fatalf("second read: %d", rec.Code)
	}
	if _, in := get(stashImport("magpie://import?name=x")); in.Error == "" {
		t.Fatal("a bad link reported no error")
	}
}

func TestImportIconRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	mux := http.NewServeMux()
	importRoutes(mux)
	post := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"url":"` + url + `"}`)
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/import/icon", body))
		return rec
	}
	// a loopback URL is refused before any request goes out
	if rec := post("https://127.0.0.1/logo.png"); rec.Code == 200 {
		t.Fatalf("loopback icon accepted: %s", rec.Body.String())
	}
	if rec := post("http://relay.example/logo.svg"); rec.Code == 200 {
		t.Fatalf("plain http icon accepted: %s", rec.Body.String())
	}
	if rec := post("not a url"); rec.Code == 200 {
		t.Fatalf("junk accepted: %s", rec.Body.String())
	}
}

func TestImportLink(t *testing.T) {
	if ImportLink([]string{"/usr/bin/magpie", "tray"}) != "" {
		t.Fatal("found a link in plain args")
	}
	if ImportLink([]string{"MAGPIE://import?preset=x"}) == "" {
		t.Fatal("missed an upper-case link")
	}
	// ccswitch:// is CC Switch's scheme, not magpie's
	if ImportLink([]string{"ccswitch://v1/import?resource=provider&app=claude&name=x"}) != "" {
		t.Fatal("took a CC Switch link")
	}
}

// A plugin link opens Plugins on the package's sheet, and a page handing
// the system link after link opens the window once each pluginLinkGap at
// most. A link that names no package goes to the import dialog, which
// says why.
func TestPluginLinkView(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	pluginLinks.Lock()
	pluginLinks.at = time.Time{}
	pluginLinks.Unlock()
	t.Cleanup(func() { pluginLinks.Lock(); pluginLinks.at = time.Time{}; pluginLinks.Unlock() })

	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	link := "magpie://plugin?name=%40magpie-community%2Fopencode-zed-auth"
	view, ok := pluginView(link, now)
	if !ok || view != "plugins&plugin=%40magpie-community%2Fopencode-zed-auth" {
		t.Fatalf("pluginView = %q, %v", view, ok)
	}
	if v, ok := pluginView("magpie://plugin?name=other", now.Add(pluginLinkGap/2)); !ok || v != "" {
		t.Fatalf("a link soon after the last opened %q, %v; want nothing", v, ok)
	}
	if v, ok := pluginView("magpie://plugin?name=other", now.Add(pluginLinkGap)); !ok || v != "plugins&plugin=other" {
		t.Fatalf("a link after the gap: %q, %v", v, ok)
	}
	if _, ok := pluginView("magpie://plugin?name=github%3Aowner%2Frepo", now.Add(time.Hour)); ok {
		t.Fatal("a link naming no npm package opened Plugins")
	}
	if _, ok := pluginView("magpie://import?preset=deepseek", now.Add(time.Hour)); ok {
		t.Fatal("an import link opened Plugins")
	}
	mux := http.NewServeMux()
	importRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/import/"+stashImport("magpie://plugin?name=Evil%40latest"), nil))
	var in importJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &in)
	if !strings.Contains(in.Error, "npm package") || in.Provider.ID != "" {
		t.Fatalf("a bad plugin link's dialog: %+v", in)
	}
}
