package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// #200: switching a provider off moves Claude Code off its model, to the
// same model from a provider still on, and the page is told so.
func TestProviderOffMovesAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "cop", Name: "Cop", Chat: "https://cop.example/v1", Key: "k", Models: []string{"gpt-5"}},
		{ID: "ds", Name: "DS", Chat: "https://ds.example/v1", Key: "k", Models: []string{"gpt-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	c, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Apply("model", "cop/gpt-5"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/off", strings.NewReader(`{"id":"cop"}`)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var st providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	want := agent.Move{Agent: "Claude Code", Field: "model", From: "cop/gpt-5", To: "ds/gpt-5"}
	if len(st.Moved) != 1 || st.Moved[0] != want {
		t.Fatalf("moved: %+v", st.Moved)
	}
	if got := c.Field("model").Get(); got != "ds/gpt-5" {
		t.Fatalf("claude: %q", got)
	}
}
