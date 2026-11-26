package gui

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// A magpie is named for its browser tab's title (starballl on X: several
// deployed, every tab titled "magpie"): MAGPIE_NAME names it until a name
// is set in Settings, which then wins and is never written over by the
// variable; boot.js hands the page the name in use and the variable's,
// and titles a browser tab from the first paint; a
// save of the rest of the page keeps it; sync never carries it.
func TestInstanceName(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv(settings.NameEnv, "  nas \n home ")
	srv := Handler(nil, nil)
	post := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("settings returned %d: %s", rec.Code, rec.Body)
		}
	}
	boot := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
		var got map[string]any
		// the prefs are boot.js's first line; the look's stylesheet may follow
		prefs, _, _ := strings.Cut(strings.TrimPrefix(rec.Body.String(), "window.bootPrefs = "), ";\n")
		if err := json.Unmarshal([]byte(prefs), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if b := boot(); b["name"] != "nas home" || b["nameEnv"] != "nas home" {
		t.Fatalf("named by the variable alone: boot %v %v", b["name"], b["nameEnv"])
	}
	if st := settingsState(); st.NameEnv != "nas home" || st.Name != "" {
		t.Fatalf("state: env %q, name %q", st.NameEnv, st.Name)
	}
	post(`{"theme":"dark","lang":"en","name":"  Tokyo   box  "}`)
	if got := settings.Load().Name; got != "Tokyo box" {
		t.Fatalf("saved %q", got)
	}
	if b := boot(); b["name"] != "Tokyo box" || b["nameEnv"] != "nas home" {
		t.Fatalf("the name set wins over the variable: boot %v %v", b["name"], b["nameEnv"])
	}
	// a browser tab is titled from the first paint; the app's window isn't
	tab := httptest.NewRecorder()
	Handler(webTab{}, nil).ServeHTTP(tab, httptest.NewRequest("GET", "/boot.js", nil))
	if !strings.Contains(tab.Body.String(), "\ndocument.title = \"Tokyo box · magpie\";\n") {
		t.Fatalf("a browser tab's boot.js sets no title: %.300s", tab.Body)
	}
	app := httptest.NewRecorder()
	srv.ServeHTTP(app, httptest.NewRequest("GET", "/boot.js", nil))
	if strings.Contains(app.Body.String(), "document.title") {
		t.Fatalf("the app window's boot.js sets a title: %.300s", app.Body)
	}
	post(`{"theme":"light","lang":"en"}`)
	if got := settings.Load().Name; got != "Tokyo box" {
		t.Fatalf("a save without the name changed it to %q", got)
	}
	if long := strings.Repeat("名", 80); settings.CleanName(long) != strings.Repeat("名", settings.NameMost) {
		t.Fatalf("a long name kept whole")
	}
	// another computer's name never comes with a sync or a backup
	from := settings.Settings{Name: "other"}
	from.KeepOwn(settings.Load())
	if from.Name != "Tokyo box" {
		t.Fatalf("KeepOwn took %q", from.Name)
	}
	post(`{"theme":"light","lang":"en","name":""}`)
	if b := boot(); settings.Load().Name != "" || b["name"] != "nas home" {
		t.Fatalf("cleared: saved %q, boot %v", settings.Load().Name, b["name"])
	}
	t.Setenv(settings.NameEnv, "")
	if _, ok := boot()["name"]; ok {
		t.Fatal("no name at all, still one at boot")
	}
}
