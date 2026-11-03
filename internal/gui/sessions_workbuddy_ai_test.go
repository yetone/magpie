package gui

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// WorkBuddy AI's sessions (~/.workbuddy-ai/projects, the international
// build's folder, here holding the sessions package's WorkBuddy fixture)
// reach the Sessions page's agents and Usage > Sessions under WorkBuddy
// AI's name and icon, beside the China build's, and the page offers them to
// the trash (maxleebeta on Discord: signed in to both, the international
// build was missing from the agents).
func TestWorkBuddyAISessionsListedUnderTheirName(t *testing.T) {
	h := sandboxHome(t)
	src := filepath.Join("..", "sessions", "testdata", "workbuddy")
	dst := filepath.Join(h, ".workbuddy-ai")
	if err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	// the stats another test kept are not this HOME's
	forgetStats()
	t.Cleanup(forgetStats)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	get := func(path string, v any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	type named struct {
		Agent  string `json:"agent"`
		Name   string `json:"name"`
		Icon   string `json:"icon"`
		Resume string `json:"resume"`
	}
	var list struct{ Sessions []named }
	get("/api/sessions", &list)
	n := 0
	for _, s := range list.Sessions {
		if s.Agent != "workbuddy-ai" {
			continue
		}
		n++
		if s.Name != "WorkBuddy AI" || s.Icon != "workbuddy-color" {
			t.Errorf("listed as %q (%s), want WorkBuddy AI (workbuddy-color)", s.Name, s.Icon)
		}
		// a desktop app: nothing resumes it from a terminal
		if s.Resume != "" {
			t.Errorf("resume %q", s.Resume)
		}
	}
	if n != 3 {
		t.Fatalf("want 3 WorkBuddy AI sessions, got %d: %+v", n, list.Sessions)
	}
	var manage struct {
		Agents []struct {
			named
			Deletable bool `json:"deletable"`
		}
	}
	get("/api/sessions/manage", &manage)
	found := false
	for _, a := range manage.Agents {
		if a.Agent == "workbuddy-ai" {
			found = true
			if a.Name != "WorkBuddy AI" {
				t.Errorf("the Sessions page's agent is %q, want WorkBuddy AI", a.Name)
			}
			if !a.Deletable {
				t.Error("the Sessions page offers no trash for WorkBuddy AI's sessions")
			}
		}
	}
	if !found {
		t.Fatalf("no workbuddy-ai among the Sessions page's agents: %+v", manage.Agents)
	}
	var stats struct {
		Agents map[string]string `json:"agents"`
	}
	get("/api/sessions/stats?days=0", &stats)
	if stats.Agents["workbuddy-ai"] != "WorkBuddy AI" {
		t.Errorf("Usage > Sessions' agent strip names it %q: %v", stats.Agents["workbuddy-ai"], stats.Agents)
	}
}
