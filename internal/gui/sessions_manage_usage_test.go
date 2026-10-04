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

// The Sessions page shows what each session spent, as the Usage page's
// list did (#752): sessions/manage carries a session's tokens, cost and
// models, and where magpie's gateway sent its calls (via).
func TestSessionsManageCarriesUsage(t *testing.T) {
	h := sandboxHome(t)
	claude := filepath.Join(h, ".claude")
	src := filepath.Join("..", "sessions", "testdata", "claude")
	if err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(claude, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	const id = "11111111-2222-3333-4444-555555555555"
	usage.Append(usage.Record{Time: time.Now(), Agent: "claude", Session: id, Provider: "relay", Model: "opus-x", Effort: "high", Input: 30, Output: 5, Status: 200})

	mux := http.NewServeMux()
	sessionManageRoutes(mux, folderOnly{})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/manage?agent=claude", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var out struct {
		Sessions []struct {
			ID     string `json:"id"`
			Input  int    `json:"input"`
			Output int    `json:"output"`
			Models []struct {
				Model string `json:"model"`
			} `json:"models"`
			Via []usage.Via `json:"via"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range out.Sessions {
		if s.ID != id {
			continue
		}
		if s.Input == 0 || s.Output == 0 || len(s.Models) == 0 || s.Models[0].Model == "" {
			t.Fatalf("no usage on the session: %+v", s)
		}
		if len(s.Via) != 1 || s.Via[0].Provider != "relay" || s.Via[0].Model != "opus-x" || s.Via[0].Effort != "high" || s.Via[0].Calls != 1 {
			t.Fatalf("via %+v", s.Via)
		}
		return
	}
	t.Fatalf("session %s not listed: %s", id, w.Body)
}
