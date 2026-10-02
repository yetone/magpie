package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A ChatGPT backend slow to hand over its model list doesn't hold magpie's
// answer past the 5 s Codex waits for it: Codex then kept the list it was
// built with, none of magpie's models in it (#539). The answer comes in
// time, with magpie's models and Codex's own from its last list.
func TestCodexModelListBackendSlow(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	release := make(chan struct{})
	up := chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release); up.CloseClientConnections() })
	home, _ := os.UserHomeDir()
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"),
		[]byte(`{"etag":"\"v1\"","models":[{"slug":"gpt-5.5","priority":1,"base_instructions":"x"}]}`), 0o644)
	was := codexModelsWait
	codexModelsWait = 200 * time.Millisecond
	t.Cleanup(func() { codexModelsWait = was })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/models?client_version=0.159.2", nil)
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	done := make(chan struct{})
	go func() { New().Handler().ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("magpie's model list waited on the backend past the time Codex gives it")
	}
	var list struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	has := map[string]bool{}
	for _, m := range list.Models {
		s, _ := m["slug"].(string)
		has[s] = true
	}
	if rec.Code != 200 || !has["fake/m1"] || !has["gpt-5.5"] {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
