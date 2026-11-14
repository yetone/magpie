package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The limit editor's list of the accounts' windows a key can follow
// (ljayic on X) is a list even with no account read: the page reads
// "windows" and says none can be read now, never fails on null.
func TestCallerKeyAccountWindowsRoute(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/caller-keys/account-windows", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"windows":[]}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
