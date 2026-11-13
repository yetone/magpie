package mcpauth

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The page an MCP sign-in ends on takes the reader's theme, and says so,
// so a browser that repaints light pages dark leaves it alone (xjp on X).
func TestPageDeclaresBothColorSchemes(t *testing.T) {
	rec := httptest.NewRecorder()
	page(rec, 200, "Signed in")
	b := rec.Body.String()
	if !strings.Contains(b, `<meta name="color-scheme" content="light dark">`) {
		t.Errorf("no color-scheme meta:\n%s", b)
	}
	if strings.Contains(b, "color:#222") {
		t.Errorf("a fixed dark text colour is unreadable on a dark canvas:\n%s", b)
	}
}
