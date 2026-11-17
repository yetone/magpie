package gui

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Claude's cards can show Anthropic's A\ in the menu bar in place of
// Claude's logo (settings.TrayAnthropicMark, qingnianxiaozhe on X): the
// mark, a black glyph in the bar's colour, goes on Claude Code's card and a
// plugin's with Claude's logo, and every other card keeps its own.
func TestTrayAnthropicMark(t *testing.T) {
	cards := append(trayCards(), provider.SubscriptionQuota{Provider: "anthropic-plugin", Name: "Anthropic", Icon: "claudecode-color",
		Windows: []provider.QuotaWindow{{Name: "5-hour", Used: 3, Span: 5 * time.Hour}}})
	cells, _, _ := trayUsageView(cards, time.Now(), false, false)
	marked := trayAnthropic(cells)
	if len(marked) != len(cells) {
		t.Fatalf("%d cells, want %d", len(marked), len(cells))
	}
	for i, c := range marked {
		claude := cells[i].Logo == "claude-color" || cells[i].Logo == "claudecode-color"
		if claude != (i == 0 || i == len(cells)-1) {
			t.Fatalf("cell %d's logo %q", i, cells[i].Logo)
		}
		if claude {
			if !bytes.Equal(c.Icon, trayAnthropicMark) || !c.Mono {
				t.Errorf("cell %d (%s): mono %v, %d bytes, not the A\\ mark", i, c.ID, c.Mono, len(c.Icon))
			}
		} else if !bytes.Equal(c.Icon, cells[i].Icon) || c.Mono != cells[i].Mono {
			t.Errorf("cell %d (%s) lost its own logo", i, c.ID)
		}
		if c.ID != cells[i].ID || c.Letter != cells[i].Letter || !slices.Equal(c.Rows, cells[i].Rows) {
			t.Errorf("cell %d changed beside its logo: %+v", i, c)
		}
		// the cells drawn with Claude's logo are left as they were
		if claude && bytes.Equal(cells[i].Icon, trayAnthropicMark) {
			t.Errorf("cell %d: the cells given were changed", i)
		}
	}
	// without logos there is none to swap
	for i, c := range trayAnthropic(trayPlain(cells)) {
		if c.Icon != nil || c.Mono {
			t.Errorf("plain cell %d got a logo", i)
		}
	}
}

// The mark is an SVG AppKit reads (the menu bar's NSImage) and the page
// draws: well-formed, square, filled black, nothing fetched.
func TestTrayAnthropicMarkSVG(t *testing.T) {
	d := xml.NewDecoder(bytes.NewReader(trayAnthropicMark))
	paths := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "path" {
			paths++
		}
	}
	if paths != 2 || !bytes.Contains(trayAnthropicMark, []byte(`viewBox="0 0 24 24"`)) || bytes.Contains(trayAnthropicMark, []byte("href")) {
		t.Fatalf("the mark: %s", trayAnthropicMark)
	}
}

// Settings saves the choice, and the menu bar is drawn again at once with
// it, not at its next tick.
func TestTrayAnthropicMarkSaved(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	woke := 0
	was := onTrayUsage
	onTrayUsage = func() { woke++ }
	t.Cleanup(func() { onTrayUsage = was })
	// as the page posts: every setting it was drawn with (prefsKeep), one changed
	post := func(on bool) *httptest.ResponseRecorder {
		var body map[string]any
		b, _ := json.Marshal(settings.Load())
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		body["trayAnthropicMark"] = on
		b, _ = json.Marshal(body)
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", bytes.NewReader(b)))
		return rec
	}
	post(false) // the page's first save, everything as it is
	woke = 0
	for _, c := range []struct {
		on   bool
		woke int
	}{
		{true, 1},
		{true, 1}, // saved again unchanged
		{false, 2},
	} {
		rec := post(c.on)
		if rec.Code != http.StatusOK {
			t.Fatalf("%v: %d %s", c.on, rec.Code, rec.Body)
		}
		if got := settings.Load().TrayAnthropicMark; got != c.on {
			t.Fatalf("after %v the mark is %v", c.on, got)
		}
		if woke != c.woke {
			t.Fatalf("after %v the menu bar was woken %d times, want %d", c.on, woke, c.woke)
		}
	}
}
