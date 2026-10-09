package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

func TestSessionsReportsUnsupportedReasonixStore(t *testing.T) {
	home := sandboxHome(t)
	t.Setenv("REASONIX_STATE_HOME", home)
	d := filepath.Join(home, "projects", "fixture", "sessions-v4", "session-id")
	if err := os.MkdirAll(d, 0700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"manifest.json", "events.frames"} {
		if err := os.WriteFile(filepath.Join(d, n), []byte("unchanged"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sessions.Reset()
	forgetStats()
	t.Cleanup(sessions.Reset)
	t.Cleanup(forgetStats)
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions", nil))
	var out sessionsJSON
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("response: %d %s", w.Code, w.Body)
	}
	if out.UnsupportedReasonix != 1 || len(out.Sessions) != 0 {
		t.Fatalf("missing compatibility diagnostic: %+v", out)
	}
}
