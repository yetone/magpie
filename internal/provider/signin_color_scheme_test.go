package provider

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The page a sign-in ends on draws a dark theme, and says so, so a browser
// that repaints light pages dark leaves it alone (xjp on X).
func TestSignInPageDeclaresBothColorSchemes(t *testing.T) {
	rec := httptest.NewRecorder()
	signInPage(rec, true, "Signed in", "You can close this tab.")
	if !strings.Contains(rec.Body.String(), `<meta name="color-scheme" content="light dark">`) {
		t.Errorf("no color-scheme meta:\n%s", rec.Body.String())
	}
}
