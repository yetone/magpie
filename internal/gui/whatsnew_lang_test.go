package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/update"
)

const (
	notesEn   = "### Features\n\n- One thing (#1)"
	notesZh   = "### 新功能\n\n- 一件事 (#1)"
	notesBoth = notesEn + "\n\n### Install\n\nDownload it.\n\n<!-- lang:zh -->\n\n" + notesZh
)

// The language a page names: zh for any Chinese, en for anything else.
func TestAskedLang(t *testing.T) {
	for q, want := range map[string]string{"": "", "?lang=zh": "zh", "?lang=zh-CN": "zh", "?lang=en": "en", "?lang=fr": "en", "?all=1&lang=zh": "zh"} {
		r := httptest.NewRequest("GET", "/api/whatsnew"+q, nil)
		if got := askedLang(r); got != want {
			t.Errorf("%q: %q, want %q", q, got, want)
		}
	}
}

// What's new follows the page's language (freecss on Discord): the notes
// are asked of the site in it, and kept for it alone; a page in another
// language gets its own.
func TestWhatsNewInPageLang(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "magpie")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, lastRunFile), []byte("0.1.603\n"), 0o644)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("lang"))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"releases": []update.Note{{Version: "0.1.604", Notes: notesBoth}}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_NOTES_FEED", srv.URL)
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1/")
	old := Version
	defer func() { Version = old }()
	Version = "0.1.604"
	n := &whatsNew{}
	n.start()

	if j := n.get(context.Background(), false, "zh"); !j.Show || len(j.Releases) != 1 || j.Releases[0].Notes != notesZh {
		t.Fatalf("zh: %+v", j)
	}
	if j := n.get(context.Background(), true, "zh"); len(j.Releases) != 1 || j.Releases[0].Notes != notesZh {
		t.Fatalf("zh again: %+v", j)
	}
	if j := n.get(context.Background(), true, "en"); len(j.Releases) != 1 || j.Releases[0].Notes != notesEn {
		t.Fatalf("en: %+v", j)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, ",") != "zh,en" {
		t.Fatalf("asked in %q", asked)
	}
}

// The waiting update's notes follow the page's language too: held in
// another, they are asked for again in the page's, once.
func TestUpdateNotesInPageLang(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("lang"))
		mu.Unlock()
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.605", Notes: notesBoth})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	u := &updater{state: "ready", latest: &update.Release{Version: "0.1.605", Notes: notesEn}, notesIn: "en"}
	if j := u.jsonIn("en"); j.Notes != notesEn {
		t.Fatalf("en: %q", j.Notes)
	}
	if j := u.jsonIn("zh"); j.Notes != notesEn { // until the Chinese is in
		t.Fatalf("zh at once: %q", j.Notes)
	}
	deadline := time.Now().Add(5 * time.Second)
	for u.jsonIn("zh").Notes != notesZh {
		if time.Now().After(deadline) {
			t.Fatalf("zh never came: %q", u.jsonIn("zh").Notes)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if u.json().Notes != notesZh || u.lang != "zh" {
		t.Fatalf("held %q in %q", u.json().Notes, u.lang)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, ",") != "zh" {
		t.Fatalf("asked in %q", asked)
	}
}

// Notes the release workflow couldn't translate yet (DeepSeek out of
// credit, #2260) come in English though asked in Chinese; once
// magpie-releases' notes.yml writes the Chinese, What's new shows it
// without a restart (yuu003348 on Discord: every release's notes in
// English). Notes all in Chinese, or asked in English, are kept for the
// run, and a failed ask again keeps the English had.
func TestWhatsNewAsksAgainForTranslation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "magpie")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, lastRunFile), []byte("0.1.603\n"), 0o644)
	var mu sync.Mutex
	asked := map[string]int{} // by path and lang
	translated, down := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked[r.URL.Query().Get("limit")+"|"+r.URL.Query().Get("lang")]++
		if down {
			http.Error(w, "down", 502)
			return
		}
		older := notesEn                                      // a release from before the marker
		newest := notesEn + "\n\n### Install\n\nDownload it." // DeepSeek turned away
		if translated {
			newest = notesBoth
		}
		json.NewEncoder(w).Encode(map[string]any{"releases": []update.Note{
			{Version: "0.1.604", Notes: newest},
			{Version: "0.1.603", Notes: older + "\n\n<!-- lang:zh -->\n\n" + notesZh},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_NOTES_FEED", srv.URL)
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1/")
	old, oldNow := Version, notesNow
	defer func() { Version, notesNow = old, oldNow }()
	now := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	notesNow = func() time.Time { return now }
	Version = "0.1.604"
	n := &whatsNew{}
	n.start()
	count := func(k string) int {
		mu.Lock()
		defer mu.Unlock()
		return asked[k]
	}

	if j := n.get(context.Background(), true, "zh"); len(j.Releases) != 1 || j.Releases[0].Notes != notesEn {
		t.Fatalf("zh untranslated: %+v", j)
	}
	if j := n.before(context.Background(), 10, "zh"); len(j.Releases) != 2 || j.Releases[0].Notes != notesEn || j.Releases[1].Notes != notesZh {
		t.Fatalf("zh back untranslated: %+v", j)
	}
	n.get(context.Background(), true, "en")
	n.before(context.Background(), 10, "en")
	// within notesAgain: what was had, the site not asked
	now = now.Add(notesAgain - time.Second)
	n.get(context.Background(), true, "zh")
	n.before(context.Background(), 10, "zh")
	if count("|zh") != 1 || count("11|zh") != 1 {
		t.Fatalf("asked again too soon: %v", asked)
	}
	// later, the site down: the English had, no error, asked again after
	mu.Lock()
	down = true
	mu.Unlock()
	now = now.Add(2 * time.Second)
	if j := n.get(context.Background(), true, "zh"); j.Error != "" || len(j.Releases) != 1 || j.Releases[0].Notes != notesEn {
		t.Fatalf("zh again, site down: %+v", j)
	}
	if j := n.before(context.Background(), 10, "zh"); j.Error != "" || len(j.Releases) != 2 {
		t.Fatalf("zh back again, site down: %+v", j)
	}
	// the Chinese written since: shown, in both lists
	mu.Lock()
	down, translated = false, true
	mu.Unlock()
	now = now.Add(notesAgain)
	if j := n.get(context.Background(), true, "zh"); len(j.Releases) != 1 || j.Releases[0].Notes != notesZh {
		t.Fatalf("zh once translated: %+v", j)
	}
	if j := n.before(context.Background(), 10, "zh"); len(j.Releases) != 2 || j.Releases[0].Notes != notesZh {
		t.Fatalf("zh back once translated: %+v", j)
	}
	// all in Chinese now, and English never needs it: kept for the run
	before := map[string]int{"|zh": count("|zh"), "11|zh": count("11|zh")}
	now = now.Add(24 * time.Hour)
	n.get(context.Background(), true, "zh")
	n.before(context.Background(), 10, "zh")
	n.get(context.Background(), true, "en")
	n.before(context.Background(), 10, "en")
	if count("|zh") != before["|zh"] || count("11|zh") != before["11|zh"] || count("|en") != 1 || count("11|en") != 1 {
		t.Fatalf("asked again with nothing to wait for: %v", asked)
	}
}

func TestUntranslated(t *testing.T) {
	zh := []update.Note{{Notes: notesZh}}
	en := []update.Note{{Notes: notesZh}, {Notes: notesEn}}
	if untranslated(zh, "zh") || !untranslated(en, "zh") || untranslated(en, "en") || untranslated(nil, "zh") {
		t.Fatal("untranslated")
	}
}
