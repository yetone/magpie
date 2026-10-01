package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
}
