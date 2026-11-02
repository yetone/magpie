package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// copyTree copies a testdata folder to to.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// panelPlugins writes plugins.json with the example panel plugin
// (testdata/panel-plugin), on and a copy switched off, and two copies of
// the example page plugin that each ask to replace Usage; it answers
// their specs in that order.
func panelPlugins(t *testing.T) (on, off, first, second string) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	t.Setenv("MAGPIE_BUN", "")
	t.Setenv("PATH", "")
	on, off = filepath.Join(h, "src", "notes"), filepath.Join(h, "src", "notes-off")
	first, second = filepath.Join(h, "src", "usage-a"), filepath.Join(h, "src", "usage-b")
	copyTree(t, "testdata/panel-plugin", on)
	copyTree(t, "testdata/panel-plugin", off)
	for i, dir := range []string{first, second} {
		copyTree(t, "testdata/page-plugin", dir)
		pkg := `{"name":"usage-` + string(rune('a'+i)) + `","version":"0.1.0","magpie":{"page":{"title":"Usage ` + string(rune('A'+i)) + `","file":"page/index.html","replaces":"usage"},"permissions":["usage:summary"]}}`
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "secret.txt"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"plugins": []map[string]any{{"spec": on}, {"spec": off, "off": true}, {"spec": first}, {"spec": second}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return on, off, first, second
}

// A plugin's panels are listed with the page each goes in, and each is
// served by a token of its own, shut in as a page is: to its own folder,
// off the network, and none of a plugin switched off.
func TestPluginPanelsServedShutIn(t *testing.T) {
	on, off, _, _ := panelPlugins(t)
	h := pageMux()
	var panels []pluginPanel
	w := do(h, httptest.NewRequest("GET", "/api/plugin-panels", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &panels); err != nil || len(panels) != 3 {
		t.Fatalf("panels: %s %v", w.Body, err)
	}
	want := []struct{ slot, title, key string }{
		{"provider", "Notes", "plugin-magpie-provider-notes-0"},
		{"usage", "Top models today", "plugin-magpie-provider-notes-1"},
		{"settings", "Provider notes", "plugin-magpie-provider-notes-2"},
	}
	toks := map[string]bool{}
	for i, p := range panels {
		if p.Spec != on || p.Slot != want[i].slot || p.Title != want[i].title || p.Key != want[i].key || len(p.Reads) != 1 || p.Reads[0] != "usage:summary" {
			t.Errorf("panel %d: %+v", i, p)
		}
		tok := strings.Split(strings.TrimPrefix(p.URL, pluginPagePrefix), "/")[0]
		if toks[tok] || tok == pageToken(on) {
			t.Errorf("panel %d shares a token: %s", i, tok)
		}
		toks[tok] = true
		r := httptest.NewRequest("GET", p.URL, nil)
		r.Host = "127.0.0.1:3437"
		w := do(h, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<html") {
			t.Fatalf("panel %d: %d %s", i, w.Code, w.Body)
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, want := range []string{"sandbox allow-scripts", "connect-src 'none'", "script-src https://127.0.0.1:3437" + pluginPagePrefix + tok + "/ http://127.0.0.1:3437" + pluginPagePrefix + tok + "/;"} {
			if !strings.Contains(csp, want) {
				t.Errorf("panel %d: policy %q has no %q", i, csp, want)
			}
		}
		if strings.Contains(csp, "allow-same-origin") {
			t.Errorf("panel %d: policy %q lets it out", i, csp)
		}
		for _, path := range []string{pluginPagePrefix + tok + "/../package.json", pluginPagePrefix + tok + "/../../secret.txt"} {
			r := httptest.NewRequest("GET", "/", nil)
			r.URL.Path = path
			w := httptest.NewRecorder()
			servePluginPage(w, r, false)
			if w.Code == 200 || strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), `"name"`) {
				t.Errorf("%s: %d %s", path, w.Code, w.Body)
			}
		}
	}
	// the switched-off copy's panels, by the token they would have
	r := httptest.NewRequest("GET", "/", nil)
	r.URL.Path = pluginPagePrefix + pageToken(off+"\x00panel\x000") + "/notes.html"
	w = httptest.NewRecorder()
	servePluginPage(w, r, false)
	if w.Code == 200 {
		t.Errorf("a plugin switched off's panel: %d", w.Code)
	}
}

// A panels-only plugin reads what it declared and nothing else, through
// the same endpoint as a page, and sets its own options.
func TestPluginPanelsReadWhatTheyDeclared(t *testing.T) {
	on, off, _, _ := panelPlugins(t)
	h := pageMux()
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return do(h, r)
	}
	spec, _ := json.Marshal(on)
	if w := post("/api/plugin-pages/read", `{"spec":`+string(spec)+`,"read":"providers:list"}`); w.Code != http.StatusForbidden {
		t.Errorf("an undeclared read: %d %s", w.Code, w.Body)
	}
	if w := post("/api/plugin-pages/read", `{"spec":`+string(spec)+`,"read":"usage:summary"}`); w.Code != 200 {
		t.Errorf("a declared read: %d %s", w.Code, w.Body)
	}
	offSpec, _ := json.Marshal(off)
	if w := post("/api/plugin-pages/read", `{"spec":`+string(offSpec)+`,"read":"usage:summary"}`); w.Code == 200 {
		t.Errorf("a plugin switched off read: %d", w.Code)
	}
	if w := post("/api/plugin-pages/options", `{"spec":`+string(spec)+`,"options":{"notes":{"openai":"work"}}}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"openai":"work"`) {
		t.Errorf("options set: %d %s", w.Code, w.Body)
	}
}

// Two plugins asking to replace one built-in page: the first in
// plugins.json's order takes its place, the other is a page of its own.
func TestPluginPageReplacesFirstWins(t *testing.T) {
	_, _, first, second := panelPlugins(t)
	var pages []pluginPage
	w := do(pageMux(), httptest.NewRequest("GET", "/api/plugin-pages", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &pages); err != nil || len(pages) != 2 {
		t.Fatalf("pages: %s %v", w.Body, err)
	}
	if pages[0].Spec != first || pages[0].Replaces != "usage" {
		t.Errorf("the first: %+v", pages[0])
	}
	if pages[1].Spec != second || pages[1].Replaces != "" {
		t.Errorf("the second: %+v", pages[1])
	}
}
