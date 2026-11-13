package gui

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Every page magpie draws says it has a dark theme of its own, so a
// browser that repaints light pages dark (Chrome's Auto Dark Theme,
// Safari's Adaptive Dark Mode) leaves it alone (xjp on X).
func TestPagesDeclareBothColorSchemes(t *testing.T) {
	const meta = `<meta name="color-scheme" content="light dark">`
	index, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), meta) {
		t.Errorf("index.html has no %s", meta)
	}
	css, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ":root { color-scheme: light dark; }") {
		t.Error("app.css's root doesn't take both color schemes")
	}
	rec := httptest.NewRecorder()
	webTurnedAway(rec, httptest.NewRequest("GET", "/", nil), "/", "why")
	if !strings.Contains(rec.Body.String(), meta) {
		t.Errorf("magpie web's sign-in page has no %s:\n%s", meta, rec.Body.String())
	}
}
