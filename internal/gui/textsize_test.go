package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The panel and the window grow with the text size, so the page inside
// has as many CSS pixels as at 100%; the panel no taller than the screen.
func TestTextSizeFrames(t *testing.T) {
	for _, c := range []struct {
		page       int
		z          float64
		room, w, h int
	}{
		{400, 1, 0, 440, 400},
		{400, 1.5, 0, 660, 600},
		{100, 1.25, 0, 550, 275},   // no shorter than panelMin, zoomed
		{900, 1.5, 0, 660, 840},    // no taller than panelMax, zoomed
		{900, 1.5, 700, 660, 700},  // nor than the screen has room for
		{400, 1.1, 1000, 484, 440}, // 440 at 110%
	} {
		if w, h := panelFrame(c.page, c.z, c.room); w != c.w || h != c.h {
			t.Errorf("panelFrame(%d, %v, %d) = %d×%d, want %d×%d", c.page, c.z, c.room, w, h, c.w, c.h)
		}
	}
	if w, h := windowMin(1.5, 0, 0); w != 840 || h != 630 {
		t.Errorf("window min at 150%%: %d×%d", w, h)
	}
	if w, h := windowMin(1.5, 800, 600); w != 800 || h != 600 {
		t.Errorf("window min on a small screen: %d×%d", w, h)
	}
	if w, h := windowMin(1.5, 400, 300); w != 560 || h != 420 {
		t.Errorf("window min is never under 100%%'s: %d×%d", w, h)
	}
	if zoomOf(0) != 1 || zoomOf(125) != 1.25 || zoomOf(9000) != 1 {
		t.Error("zoomOf")
	}
}

type zoomWindows struct {
	webHost
	sizes []int
}

func (z *zoomWindows) SetTextSize(n int) { z.sizes = append(z.sizes, n) }

// The text size is saved on its own, zooms the windows, and a save of the
// Settings page (which never sends it) leaves it as it is.
func TestTextSizeSetting(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	w := &zoomWindows{}
	srv := Handler(w, nil)
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	rec := post("/api/settings/text-size", `{"size":125}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct{ TextSize int }
	if json.Unmarshal(rec.Body.Bytes(), &got); got.TextSize != 125 || settings.Load().TextSize != 125 {
		t.Fatalf("answered %d, saved %d", got.TextSize, settings.Load().TextSize)
	}
	if len(w.sizes) != 1 || w.sizes[0] != 125 {
		t.Fatalf("windows zoomed to %v", w.sizes)
	}
	if rec := post("/api/settings/text-size", `{"size":133}`); rec.Code == http.StatusOK || settings.Load().TextSize != 125 || len(w.sizes) != 1 {
		t.Fatalf("a size not offered was taken: %d", rec.Code)
	}
	if rec := post("/api/settings", `{"theme":"dark","lang":"en"}`); rec.Code != http.StatusOK || settings.Load().TextSize != 125 {
		t.Fatalf("the Settings page's save changed the text size: %d %d", rec.Code, settings.Load().TextSize)
	}
	// the page is told it before it paints
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
	if !strings.Contains(rec.Body.String(), `"textSize":125`) {
		t.Fatalf("boot.js: %s", rec.Body)
	}
}
