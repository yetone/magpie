package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// A gateway session that spent nothing at all — Codex's failed title and
// review calls carry an id no file of its own has — has nothing to show and
// is no row on either list (usage.GatewaySession.SpentNothing; flandern:
// three empty rows under "Seen through the gateway").
func TestGatewaySessionThatSpentNothingIsNoRow(t *testing.T) {
	sandboxHome(t)
	now := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.MkdirAll(filepath.Dir(usage.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	enc.Encode(usage.Record{Time: now, Agent: "codex", Provider: "fake", Model: "m", Session: "spent", Input: 10, Status: 200})
	enc.Encode(usage.Record{Time: now, Agent: "codex", Provider: "fake", Model: "m", Session: "empty", Status: 400})
	f.Close()
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	ids := func(path string) []string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var out struct {
			Sessions []sessions.Session `json:"sessions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range out.Sessions {
			got = append(got, s.ID)
		}
		return got
	}
	for _, path := range []string{"/api/sessions", "/api/sessions/manage?agent=codex"} {
		got := ids(path)
		if len(got) != 1 || got[0] != "spent" {
			t.Errorf("%s lists %v, want the session that spent something only", path, got)
		}
	}
}
