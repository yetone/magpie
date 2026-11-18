package gui

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// Tystem on Discord: connecting Codex with no provider or subscription in
// magpie answered "Codex can't be connected to magpie", untranslated. The
// answer carries code no_models and the agent's name, which the GUI says
// in the reader's language.
func TestAgentConnectNoModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-5.5\"\n"), 0o600)
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/connect/codex", strings.NewReader("{}")))
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 400 || out["code"] != "no_models" || out["agent"] != "Codex" || !strings.Contains(out["error"], "Add a provider or subscription") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Lumens on Discord: connecting WorkBuddy, whose one field puts magpie in
// its own picker, answered 200 with nothing added in magpie, so the page
// said it was connected and its switch went back off. It answers
// no_models now, as Codex does.
func TestAgentConnectNoModelsMagpieAsProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("WORKBUDDY_CONFIG_DIR", "")
	os.MkdirAll(filepath.Join(home, ".workbuddy"), 0o755)
	os.WriteFile(filepath.Join(home, ".workbuddy", "models.json"), []byte("[]\n"), 0o600)
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/connect/workbuddy", strings.NewReader("{}")))
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 400 || out["code"] != "no_models" || out["agent"] != "WorkBuddy" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// znuaibot (#1989): magpie has models, none left in the agent's lists. The
// answer carries code all_hidden and the agent's name, which the GUI says
// in the reader's language, pointing at the agent's Model list.
func TestAgentConnectAllHidden(t *testing.T) {
	rec := httptest.NewRecorder()
	fail(rec, fmt.Errorf("connect: %w", &agent.AllHiddenError{Agent: "OpenCode"}))
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 400 || out["code"] != "all_hidden" || out["agent"] != "OpenCode" || !strings.Contains(out["error"], "Model list › Pick") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
